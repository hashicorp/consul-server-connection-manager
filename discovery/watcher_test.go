package discovery

import (
	"context"
	"fmt"
	"testing"

	"github.com/hashicorp/consul-server-connection-manager/internal/consul-proto/pbdataplane"
	"github.com/hashicorp/consul/sdk/testutil"
	"github.com/hashicorp/go-hclog"
	"github.com/stretchr/testify/require"
)

// TestRun starts a Consul server cluster.
func TestRun(t *testing.T) {
	_, serverAddrs := consulServers(t, 3)

	ctx := context.Background()
	w, err := NewWatcher(
		ctx,
		Config{},
		hclog.New(&hclog.LoggerOptions{
			Name:  "watcher",
			Level: hclog.Debug,
		}),
	)
	require.NoError(t, err)

	// In order to test with local Consul tests servers, we set custom server ports,
	// which we inject through custom interfaces. The Config and go-netaddrs and the
	// server watch stream do not support per-server ports.
	w.discoverer = &testDiscoverer{
		addrs: serverAddrs,
	}

	// Start the Watcher. This blocks until initialization is complete.
	go w.Run()
	t.Cleanup(w.Stop)

	state, err := w.State()
	require.NoError(t, err)
	require.NotNil(t, state)
	require.NotNil(t, state, state.GRPCConn)
	require.Equal(t, state.Token, "")

	// Make a gRPC request to check that the gRPC connection is working.
	unaryClient := pbdataplane.NewDataplaneServiceClient(state.GRPCConn)
	resp, err := unaryClient.GetSupportedDataplaneFeatures(ctx, &pbdataplane.GetSupportedDataplaneFeaturesRequest{})
	require.NoError(t, err, "error from unary request")
	require.NotNil(t, resp)

	t.Logf("test successful")
}

// A custom Discoverer allows us to inject custom addresses with ports, so that
// we can test with multiple local Consul test servers. go-netaddrs doesn't
// support per-server ports.
type testDiscoverer struct {
	addrs []Addr
}

var _ Discoverer = (*testDiscoverer)(nil)

func (t *testDiscoverer) Discover(ctx context.Context) ([]Addr, error) {
	return t.addrs, nil
}

// consulServers starts a multi-server Consul test cluster. It returns map of the
// server addresses (ip+port) to each server object, and the list of parsed
// server gRPC addresses.
func consulServers(t *testing.T, n int) (map[string]*testutil.TestServer, []Addr) {
	require.Greater(t, n, 0)

	servers := map[string]*testutil.TestServer{}
	var addrs []Addr
	for i := 0; i < n; i++ {
		server, err := testutil.NewTestServerConfigT(t, func(c *testutil.TestServerConfig) {
			c.Bootstrap = len(servers) == 0
			for _, srv := range servers {
				addr := fmt.Sprintf("%s:%d", srv.Config.Bind, srv.Config.Ports.SerfLan)
				c.RetryJoin = append(c.RetryJoin, addr)
			}
			// TODO: ACL config
		})
		require.NoError(t, err)
		t.Cleanup(func() {
			_ = server.Stop()
		})

		addr, err := MakeAddr(server.Config.Bind, server.Config.Ports.GRPC)
		require.NoError(t, err)

		servers[addr.String()] = server
		addrs = append(addrs, addr)
	}

	for _, server := range servers {
		server.WaitForLeader(t)
	}
	return servers, addrs
}
