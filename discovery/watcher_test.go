package discovery

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/hashicorp/go-hclog"
	"github.com/stretchr/testify/require"

	"github.com/hashicorp/consul-server-connection-manager/internal/consul-proto/pbserverdiscovery"
	"github.com/hashicorp/consul/sdk/testutil"
	"github.com/hashicorp/consul/sdk/testutil/retry"
)

const testServerManagementToken = "12345678-90ab-cdef-0000-123456789abcd"

// TestRun starts a Consul server cluster and starts a Watcher.
func TestRun(t *testing.T) {
	cases := map[string]struct {
		config         Config
		serverConfigFn testutil.ServerConfigCallback
	}{
		"no acls": {
			config: Config{},
		},
		"static token": {
			config: Config{
				Credentials: Credentials{
					Type: CredentialsTypeStatic,
					Static: StaticTokenCredential{
						Token: testServerManagementToken,
					},
				},
			},
			serverConfigFn: enableACLsConfigFn,
		},
		"server watch disabled": {
			config: Config{
				ServerWatchDisabled:         true,
				ServerWatchDisabledInterval: 1 * time.Second,
			},
		},
	}
	for name, c := range cases {
		c := c
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			servers, serverAddrs := consulServers(t, 3, c.serverConfigFn)
			serversByNodeId := map[string]*testutil.TestServer{}
			for _, srv := range servers {
				serversByNodeId[srv.Config.NodeID] = srv
			}

			ctx := context.Background()
			w, err := NewWatcher(ctx, c.config, hclog.New(&hclog.LoggerOptions{
				Name:  fmt.Sprintf("watcher/%s", name),
				Level: hclog.Debug,
			}),
			)
			require.NoError(t, err)

			// To test with local Consul servers, we inject custom server ports. The Config struct,
			// go-netaddrs, and the server watch stream do not support per-server ports.
			w.discoverer = &testDiscoverer{addrs: serverAddrs}
			w.nodeToAddrFn = func(nodeID, addr string) (Addr, error) {
				if srv, ok := serversByNodeId[nodeID]; ok {
					return MakeAddr(addr, srv.Config.Ports.GRPC)
				}
				return Addr{}, fmt.Errorf("no test server with node id %q", nodeID)
			}

			// Start the Watcher.
			go w.Run()
			t.Cleanup(w.Stop)

			// Get initial state. This blocks until initialization is complete.
			state, err := w.State()
			require.NoError(t, err)
			require.NotNil(t, state)
			require.NotNil(t, state, state.GRPCConn)

			// check the token we get back.
			switch c.config.Credentials.Type {
			case CredentialsTypeStatic:
				require.Equal(t, state.Token, testServerManagementToken)
			case CredentialsTypeLogin:
				require.FailNow(t, "TODO: support acl token login")
			default:
				require.Equal(t, state.Token, "")
			}

			client := pbserverdiscovery.NewServerDiscoveryServiceClient(state.GRPCConn)

			unaryRequest := func(t require.TestingT) {
				req := &pbserverdiscovery.GetSupportedDataplaneFeaturesRequest{}
				resp, err := client.GetSupportedDataplaneFeatures(ctx, req)
				require.NoError(t, err, "error from unary request")
				require.NotNil(t, resp)
			}

			streamRequest := func(t require.TestingT) {
				// It seems like the stream will not automatically switch servers via the resolver.
				// It gets an address once when the stream is created.
				stream, err := client.WatchServers(ctx, &pbserverdiscovery.WatchServersRequest{})
				require.NoError(t, err, "opening stream")
				_, err = stream.Recv()
				require.NoError(t, err, "error from stream")
			}

			// Make a gRPC request to check that the gRPC connection is working.
			// This validates that the custom interceptor is injecting the ACL token.
			unaryRequest(t)
			streamRequest(t)

			// Stop the current server. The Watcher should switch servers.
			currentServer, ok := servers[state.Address.String()]
			require.True(t, ok)
			t.Logf("stop server: %s", state.Address.String())
			err = currentServer.Stop()
			if err != nil {
				// this seems to to just be "exit code 1"
				t.Logf("warn: server stop error: %v", err)
			}

			// Wait for requests to eventually succeed after the Watcher switches servers.
			retry.RunWith(retryTimeout(5*time.Second), t, func(r *retry.R) {
				unaryRequest(r)
				streamRequest(r)
			})

			newState, err := w.State()
			require.NoError(t, err)
			require.NotEqual(t, newState.Address, state.Address)

			t.Logf("test successful")
		})
	}
}

func retryTimeout(timeout time.Duration) *retry.Timer {
	return &retry.Timer{
		Timeout: timeout,
		Wait:    250 * time.Millisecond,
	}
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
// server gRPC addresses (ip+port) to each server object, and the list of parsed
// server gRPC addresses.
func consulServers(t *testing.T, n int, cb testutil.ServerConfigCallback) (map[string]*testutil.TestServer, []Addr) {
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
			if cb != nil {
				cb(c)
			}
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

func enableACLsConfigFn(c *testutil.TestServerConfig) {
	c.ACL.Enabled = true
	c.ACL.Tokens.InitialManagement = testServerManagementToken
	c.ACL.DefaultPolicy = "deny"
}
