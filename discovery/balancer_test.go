package discovery

import (
	"testing"

	"github.com/hashicorp/go-hclog"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/balancer"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/resolver"
)

type fakeBalancerClientConn struct{}

var _ balancer.ClientConn = (*fakeBalancerClientConn)(nil)

// NewSubConn implements balancer.ClientConn
func (*fakeBalancerClientConn) NewSubConn(addrs []resolver.Address, opts balancer.NewSubConnOptions) (balancer.SubConn, error) {
	return &fakeSubConn{addrs[0]}, nil
}

func (*fakeBalancerClientConn) RemoveSubConn(balancer.SubConn) {}

func (*fakeBalancerClientConn) ResolveNow(resolver.ResolveNowOptions) {}

func (*fakeBalancerClientConn) Target() string { return "" }

func (*fakeBalancerClientConn) UpdateAddresses(balancer.SubConn, []resolver.Address) {}

func (*fakeBalancerClientConn) UpdateState(balancer.State) {}

type fakeSubConn struct {
	addr resolver.Address
}

var _ balancer.SubConn = (*fakeSubConn)(nil)

func (*fakeSubConn) Connect() {}

func (*fakeSubConn) UpdateAddresses([]resolver.Address) {}

func TestBalancerOneAddress(t *testing.T) {
	watcher := &Watcher{log: hclog.NewNullLogger()}
	blr := makeBalancerBuilder(t, watcher)

	addr, err := MakeAddr("127.0.0.1", 1234)
	require.NoError(t, err)

	// Initially there are no sub-connections.
	err = blr.hasTransitioned(addr)
	require.Error(t, err)
	require.Equal(t, "no known sub-connections", err.Error())

	// Update the connection with an address.
	// This simulates the resolver updating the ClientConn.
	require.NoError(t, blr.UpdateClientConnState(balancer.ClientConnState{
		ResolverState: resolver.State{
			Addresses: []resolver.Address{{Addr: addr.String()}},
		},
	}))

	// The balancer knows about the new sub-connection and its address.
	sc := findSubConn(t, blr, addr)
	requireSubConnState(t, blr, sc, connectivity.Idle, addr, "target sub-connection is not ready")

	// Simulate sub-conn state changes.
	for _, state := range []connectivity.State{
		connectivity.Idle,
		connectivity.Connecting,
		connectivity.TransientFailure,
	} {
		blr.UpdateSubConnState(sc, balancer.SubConnState{ConnectivityState: state})
		require.Len(t, blr.scs, 1)
		requireSubConnState(t, blr, sc, state, addr, "target sub-connection is not ready")
	}

	// When the sub-conn is ready and there are no other sub-conns.
	blr.UpdateSubConnState(sc, balancer.SubConnState{ConnectivityState: connectivity.Ready})
	require.Len(t, blr.scs, 1)
	requireSubConnState(t, blr, sc, connectivity.Ready, addr, "")

	// Update the connection with no addresses.
	err = blr.UpdateClientConnState(balancer.ClientConnState{
		ResolverState: resolver.State{Addresses: nil},
	})
	require.Equal(t, balancer.ErrBadResolverState, err)

	// Still ready. The balancer keeps tracking the address until it is shutdown.
	require.Len(t, blr.scs, 1)
	requireSubConnState(t, blr, sc, connectivity.Ready, addr, "")

	// Mark the connection shutdown. The balancer stops tracking it.
	blr.UpdateSubConnState(sc, balancer.SubConnState{ConnectivityState: connectivity.Shutdown})
	require.Len(t, blr.scs, 0)
}

func TestBalancerTwoAddressses(t *testing.T) {
	watcher := &Watcher{log: hclog.NewNullLogger()}
	blr := makeBalancerBuilder(t, watcher)

	fromAddr := mustMakeAddr(t, "127.0.0.1", 1234)
	toAddr := mustMakeAddr(t, "127.0.0.2", 2345)

	// Put the first connection in ready state.
	require.NoError(t, blr.UpdateClientConnState(balancer.ClientConnState{
		ResolverState: resolver.State{
			Addresses: []resolver.Address{{Addr: fromAddr.String()}},
		},
	}))
	require.Len(t, blr.scs, 1)
	fromSC := findSubConn(t, blr, fromAddr)
	requireSubConnState(t, blr, fromSC, connectivity.Idle, fromAddr, "target sub-connection is not ready")

	blr.UpdateSubConnState(fromSC, balancer.SubConnState{ConnectivityState: connectivity.Ready})
	requireSubConnState(t, blr, fromSC, connectivity.Ready, fromAddr, "")

	// Switch to the second address.
	require.NoError(t, blr.UpdateClientConnState(balancer.ClientConnState{
		ResolverState: resolver.State{
			Addresses: []resolver.Address{{Addr: toAddr.String()}},
		},
	}))
	require.Len(t, blr.scs, 2)
	toSC := findSubConn(t, blr, toAddr)
	require.NotEqual(t, fromSC, toSC)
	requireSubConnState(t, blr, toSC, connectivity.Idle, toAddr, "old sub-connection is not shutdown")

	// Simulate all possible states of both connections (minus shutdown)
	nonShutdownStates := []connectivity.State{
		connectivity.Idle,
		connectivity.Connecting,
		connectivity.Ready,
		connectivity.TransientFailure,
	}
	for _, state := range nonShutdownStates {
		blr.UpdateSubConnState(fromSC, balancer.SubConnState{ConnectivityState: state})
		require.Len(t, blr.scs, 2)

		for _, state := range nonShutdownStates {
			blr.UpdateSubConnState(toSC, balancer.SubConnState{ConnectivityState: state})
			require.Len(t, blr.scs, 2)
			// The transition to second conn is not complete until the the first conn is shutdown.
			requireSubConnState(t, blr, toSC, state, toAddr, "old sub-connection is not shutdown")
		}
	}

	// Shutdown the first connection.
	blr.UpdateSubConnState(fromSC, balancer.SubConnState{ConnectivityState: connectivity.Shutdown})
	require.Len(t, blr.scs, 1)

	// Simulate non-Ready state changes on the second connection.
	for _, state := range []connectivity.State{
		connectivity.Idle,
		connectivity.Connecting,
		connectivity.TransientFailure,
	} {
		blr.UpdateSubConnState(toSC, balancer.SubConnState{ConnectivityState: state})
		require.Len(t, blr.scs, 1)
		// The transition to second conn is not complete until the second conn is ready.
		requireSubConnState(t, blr, toSC, state, toAddr, "target sub-connection is not ready")
	}

	// Put the second connection in ready state.
	blr.UpdateSubConnState(toSC, balancer.SubConnState{ConnectivityState: connectivity.Ready})
	require.Len(t, blr.scs, 1)
	// Transition complete!
	requireSubConnState(t, blr, toSC, connectivity.Ready, toAddr, "")
}
func mustMakeAddr(t *testing.T, addr string, port int) Addr {
	a, err := MakeAddr(addr, port)
	require.NoError(t, err)
	return a
}

func makeBalancerBuilder(t *testing.T, watcher *Watcher) *watcherBalancer {
	t.Helper()

	hackPolicyId := registerBalancer(func() hclog.Logger { return watcher.log })

	builder := balancer.Get(hackPolicyId)
	require.IsType(t, &balancerBuilder{}, builder)
	// bb := builder.(*balancerBuilder)
	// require.Equal(t, watcher, bb.watcher)

	// Build is called by gRPC normally.
	wb := builder.Build(&fakeBalancerClientConn{}, balancer.BuildOptions{})
	watcher.balancer = wb.(*watcherBalancer)
	// require.Equal(t, wb, watcher.balancer)
	require.IsType(t, &watcherBalancer{}, wb)

	return wb.(*watcherBalancer)
}

func findSubConn(t *testing.T, wb *watcherBalancer, addr Addr) *fakeSubConn {
	var found balancer.SubConn
	for sc, state := range wb.scs {
		if state.addr.Addr == addr.String() {
			found = sc
			break
		}
	}
	require.NotNil(t, found, "balancer has no sub-conn for address: %s", addr)
	require.IsType(t, &fakeSubConn{}, found)
	return found.(*fakeSubConn)
}

func requireSubConnState(t *testing.T, wb *watcherBalancer, sc balancer.SubConn, state connectivity.State, addr Addr, transitionErr string) {
	require.Equal(t, wb.scs[sc].state.ConnectivityState, state)
	require.Equal(t, wb.scs[sc].addr.Addr, addr.String())

	err := wb.hasTransitioned(addr)
	if transitionErr != "" {
		require.Error(t, err)
		require.Contains(t, err.Error(), transitionErr)
	} else {
		require.NoError(t, err)
	}
}
