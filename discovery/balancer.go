package discovery

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/cenkalti/backoff/v4"
	"github.com/hashicorp/go-hclog"
	"google.golang.org/grpc/balancer"
	"google.golang.org/grpc/balancer/base"
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

	// State to track sub-connections
	lock sync.Mutex
	scs  map[balancer.SubConn]*subConnState
}

type subConnState struct {
	state balancer.SubConnState
	addr  resolver.Address
}

var _ balancer.Balancer = (*watcherBalancer)(nil)
var _ base.PickerBuilder = (*watcherBalancer)(nil)
var _ balancer.Picker = (*watcherBalancer)(nil)

// wrap balancer.ClientConn in order to know the address for each sub-connection.
type clientConnWrapper struct {
	balancer.ClientConn
	balancer *watcherBalancer
	log      hclog.Logger
}

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
		c.balancer.lock.Lock()
		defer c.balancer.lock.Unlock()
		c.balancer.scs[sc] = &subConnState{addr: addrs[0]}
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
		c.balancer.lock.Lock()
		defer c.balancer.lock.Unlock()
		_, ok := c.balancer.scs[sc]
		if !ok {
			c.balancer.scs[sc] = &subConnState{}
		}
		c.balancer.scs[sc].addr = addrs[0]
	}
}

// We do not implement RemoveSubConn. It would stop tracking the SubConn too
// early. We need to wait until we've seen the sub-connection go into a
// shutdown state before we stop tracking it.
//func (c *clientConnWrapper) RemoveSubConn(sc balancer.SubConn) { }

// WaitForTransition waits to see that a connection has transitioned to the
// given address. It expects to see one sub-connection in Ready state for the
// given address, and all other sub-connections (if any) in Shutdown state.
func (b *watcherBalancer) WaitForTransition(ctx context.Context, to Addr) error {
	// 50 * 200ms = 10s
	var bo backoff.BackOff = backoff.NewConstantBackOff(200 * time.Millisecond)
	bo = backoff.WithMaxRetries(bo, 50)
	bo = backoff.WithContext(bo, ctx)

	// wait until we have the sub conns we are interested in.
	return backoff.Retry(func() error { return b.hasTransitioned(to) }, bo)
}

// hasTransitioned checks if we've finished transitioning to the given address.
func (b *watcherBalancer) hasTransitioned(to Addr) error {
	b.lock.Lock()
	defer b.lock.Unlock()

	if len(b.scs) == 0 {
		return fmt.Errorf("no known sub-connections")
	}

	// note: using two for loops to make the error messages deterministic for tests.
	for _, state := range b.scs {
		if state.addr.Addr != to.String() && state.state.ConnectivityState != connectivity.Shutdown {
			return fmt.Errorf("old sub-connection is not shutdown (state=%s)", state.state.ConnectivityState)
		}
	}

	foundTarget := false
	for _, state := range b.scs {
		if state.addr.Addr == to.String() {
			foundTarget = true
			if state.state.ConnectivityState != connectivity.Ready {
				return fmt.Errorf("target sub-connection is not ready (state=%s)", state.state.ConnectivityState)
			}
		}
	}

	if !foundTarget {
		return fmt.Errorf("no sub-connection found for target address %q", to.String())
	}
	return nil
}

// UpdateSubConnState is called by gRPC when the state of a SubConn changes.
//
// Once a sub-conn is shutdown, we stop tracking it.
func (b *watcherBalancer) UpdateSubConnState(sc balancer.SubConn, state balancer.SubConnState) {
	b.log.Trace("balancer.UpdateSubConnState", "sc", sc, "state", state)
	b.Balancer.UpdateSubConnState(sc, state)

	b.lock.Lock()
	defer b.lock.Unlock()

	// Store/update the sub-conn and its state.
	_, ok := b.scs[sc]
	if !ok {
		b.scs[sc] = &subConnState{}
	}
	b.scs[sc].state = state

	// Stop tracking sub-connections in shutdown state.
	for sc, state := range b.scs {
		if state.state.ConnectivityState == connectivity.Shutdown {
			delete(b.scs, sc)
		}
	}
}

// Build implements base.PickerBuilder
//
// This is called by the base Balancer when the set of ready sub-connections
// has changed.
func (b *watcherBalancer) Build(info base.PickerBuildInfo) balancer.Picker {
	b.log.Trace("pickerBuilder.Build", "sub-conns ready", len(info.ReadySCs))

	b.lock.Lock()
	defer b.lock.Unlock()

	for sc, info := range info.ReadySCs {
		_, ok := b.scs[sc]
		if !ok {
			b.scs[sc] = &subConnState{}
		}
		b.scs[sc].addr = info.Address
		b.scs[sc].state.ConnectivityState = connectivity.Ready
	}
	return b
}

// Pick implements balancer.Picker
//
// This is called prior to each gRPC message to choose the sub-conn to use.
// This picks any ready sub-connection.
func (b *watcherBalancer) Pick(info balancer.PickInfo) (balancer.PickResult, error) {
	b.lock.Lock()
	defer b.lock.Unlock()

	// Pick any READY address.
	for sc, state := range b.scs {
		if state.state.ConnectivityState == connectivity.Ready {
			return balancer.PickResult{SubConn: sc}, nil
		}
	}
	return balancer.PickResult{}, fmt.Errorf("no ready sub-connections")
}
