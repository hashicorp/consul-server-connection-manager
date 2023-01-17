package discovery

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/cenkalti/backoff/v4"
	"github.com/hashicorp/go-hclog"
	"google.golang.org/grpc/balancer"
	// "google.golang.org/grpc/balancer/base"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/resolver"
)

// This implements a custom gRPC balancer.
//
// We don't actually balance load with this in the typical sense. Instead, we
// use the custom balancer to hook into sub-connection state and addresses in
// order to know when a connection is done switching from one server to another
// (since there doesn't seem to be an easier way to find that, or otherwise
// know which server a response/error came from).
//
// We use this in conjunction with the custom resolver to facilite server
// switching.
//
//  1. The custom resolver tells the connection to switch to another server.
//     It always tells the connection to use one particular server, so we know
//     to expect at most one ready sub-connection and all other sub-connections
//     to be shutdown.
//  2. Track sub-connection states and addresses through the custom balancer.
//     gRPC calls into our custom balancer each time a sub-conn changes states.
//  3. Wait for the server switch to finish. Our balancer should see the "new"
//     sub-conn in READY status (on success) and any/all other sub-conns in a
//     SHUTDOWN status.
type watcherBalancer struct {
	balancer.Balancer

	log hclog.Logger

	cc         *clientConnWrapper
	subConn    balancer.SubConn
	state      connectivity.State
	activeAddr resolver.Address
}

// Ensure our watcherBalancer implements the gRPC Balancer interface
var _ balancer.Balancer = (*watcherBalancer)(nil)

// wrap balancer.ClientConn in order to know the address for each sub-connection.
type clientConnWrapper struct {
	balancer.ClientConn
	rcc resolver.ClientConn

	log hclog.Logger

	// State to track sub-connections
	lock sync.Mutex
	scs  map[balancer.SubConn]*subConnState
}

type subConnState struct {
	state balancer.SubConnState
	addr  resolver.Address
}

// Ensure our clientConnWrapper implements the gRPC ClientConn interface
var _ balancer.ClientConn = (*clientConnWrapper)(nil)

// NewSubConn is called by the base Balancer when it creates a new sub connection.
func (c *clientConnWrapper) NewSubConn(addrs []resolver.Address, opts balancer.NewSubConnOptions) (balancer.SubConn, error) {
	c.log.Trace("clientConnWrapper.NewSubConn", "addrs", addrs)

	if len(addrs) > 1 {
		// We expect only one address per subconnection, which should always be
		// the case for us (no nested resolver / balancer setup).
		c.log.Error("received multiple addresses for gRPC sub-connection")
		return nil, fmt.Errorf("invalid use of gRPC balancer; expected only one address for gRPC sub-connection")
	}

	sc, err := c.ClientConn.NewSubConn(addrs, opts)

	if err == nil && len(addrs) == 1 {
		// Store the address for this sub-connection.
		c.lock.Lock()
		defer c.lock.Unlock()
		c.scs[sc] = &subConnState{addr: addrs[0]}
	}
	return sc, err
}

// UpdateAddresses is called by the base Balancer when the sub connection address changes.
func (c *clientConnWrapper) UpdateAddresses(sc balancer.SubConn, addrs []resolver.Address) {
	c.log.Trace("clientConnWrapper.UpdateAddresses", "sub-conn", sc, "addrs", addrs)

	if len(addrs) > 1 {
		// We expect only one address per subconnection, which should always be
		// the case for us (no nested resolver / balancer setup).
		c.log.Error("received multiple addresses for gRPC sub-connection")
		return
	}

	c.ClientConn.UpdateAddresses(sc, addrs)

	// Update the address for this sub-connection.
	if len(addrs) == 1 {
		c.lock.Lock()
		defer c.lock.Unlock()
		_, ok := c.scs[sc]
		if !ok {
			c.scs[sc] = &subConnState{}
		}
		c.scs[sc].addr = addrs[0]
	}
}

// We do not implement RemoveSubConn. It would stop tracking the SubConn too
// early. We need to wait until we've seen the sub-connection go into a
// shutdown state before we stop tracking it.
//func (c *clientConnWrapper) RemoveSubConn(sc balancer.SubConn) { }

// WaitForTransition waits to see that a connection has transitioned to the
// given address. It expects to see one sub-connection in Ready state for the
// given address, and all other sub-connections (if any) in Shutdown state.
func (c *clientConnWrapper) WaitForTransition(ctx context.Context, to Addr) error {
	// 50 * 200ms = 10s
	var bo backoff.BackOff = backoff.NewConstantBackOff(200 * time.Millisecond)
	bo = backoff.WithMaxRetries(bo, 50)
	bo = backoff.WithContext(bo, ctx)

	// wait until we have the sub conns we are interested in.
	return backoff.Retry(func() error { return c.hasTransitioned(to) }, bo)
}

// hasTransitioned checks if we've finished transitioning to the given address.
func (c *clientConnWrapper) hasTransitioned(to Addr) error {
	c.lock.Lock()
	defer c.lock.Unlock()

	if len(c.scs) == 0 {
		return fmt.Errorf("no known sub-connections")
	}

	foundTarget := false
	var targetState connectivity.State
	for _, state := range c.scs {
		if state.addr.Addr == to.String() {
			foundTarget = true

			// Return after checking old sub-connections to make the error messages
			// deterministic for tests.
			targetState = state.state.ConnectivityState
		} else {
			if state.state.ConnectivityState != connectivity.Shutdown {
				return fmt.Errorf("old sub-connection is not shutdown (state=%s)", state.state.ConnectivityState)
			}
		}
	}

	if foundTarget {
		if targetState != connectivity.Ready {
			return fmt.Errorf("target sub-connection is not ready (state=%s)", targetState)
		}
	} else {
		return fmt.Errorf("no sub-connection found for target address %q", to.String())
	}
	return nil
}

// UpdateClientConnState is called by gRPC when the state of the ClientConn
// changes.  If the error returned is ErrBadResolverState, the ClientConn
// will begin calling ResolveNow on the active name resolver with
// exponential backoff until a subsequent call to UpdateClientConnState
// returns a nil error.  Any other errors are currently ignored.
func (b *watcherBalancer) UpdateClientConnState(state balancer.ClientConnState) error {
	b.log.Trace("balancer.UpdateClientConnState", "state", state)

	for _, a := range state.ResolverState.Addresses {
		// This hack preserves an existing behavior in our client-side
		// load balancing where if the first address in a shuffled list
		// of addresses matched the currently connected address, it would
		// be an effective no-op.
		if a.Equal(b.activeAddr) {
			return nil
		}

		// Attempt to make a new SubConn with a single address so we can
		// track a successful connection explicitly. If we were to pass
		// a list of addresses, we cannot assume the first address was
		// successful and there is no way to extract the connected address.
		sc, err := b.cc.NewSubConn([]resolver.Address{a}, balancer.NewSubConnOptions{})
		if err != nil {
			b.log.Warn("balancer.customPickfirstBalancer: failed to create new SubConn: %v", err)
			continue
		}

		// clientConnWrapper does not implement RemoveSubConn, it will be cleaned up
		// later by UpdateSubConnState
		// if b.subConn != nil {
		// 	b.cc.RemoveSubConn(b.subConn)
		// }

		// Copy-pasted from pickfirstBalancer.UpdateClientConnState.
		{
			b.subConn = sc
			b.state = connectivity.Idle
			b.cc.UpdateState(balancer.State{
				ConnectivityState: connectivity.Idle,
				Picker:            &picker{result: balancer.PickResult{SubConn: b.subConn}},
			})
			b.subConn.Connect()
		}

		b.activeAddr = a

		// We now have a new subConn with one address.
		// Break the loop and call UpdateClientConnState
		// with the full set of addresses.
		break
	}

	// This will load the full set of addresses but leave the
	// newly created subConn alone.
	return b.Balancer.UpdateClientConnState(state)
}

// UpdateSubConnState is called by gRPC when the state of a SubConn changes.
//
// Once a sub-conn is shutdown, we stop tracking it.
func (b *watcherBalancer) UpdateSubConnState(sc balancer.SubConn, state balancer.SubConnState) {
	b.log.Trace("balancer.UpdateSubConnState", "sc", sc, "state", state)
	b.Balancer.UpdateSubConnState(sc, state)

	b.cc.lock.Lock()
	defer b.cc.lock.Unlock()

	// Store/update the sub-conn and its state.
	_, ok := b.cc.scs[sc]
	if !ok {
		b.cc.scs[sc] = &subConnState{}
	}
	b.cc.scs[sc].state = state

	// Stop tracking sub-connections in shutdown state.
	for sc, state := range b.cc.scs {
		if state.state.ConnectivityState == connectivity.Shutdown {
			delete(b.cc.scs, sc)
		}
	}
}
