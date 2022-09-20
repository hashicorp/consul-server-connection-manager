# WORK IN PROGRESS

This library is still under development and hence subject to breaking changes.

## Summary

This is a Go library used to connect to a [HashiCorp Consul](https://www.consul.io/) server. It
implements server discovery and provides a gRPC connection to a Consul server.

It was designed to maintain a current connection to a Consul server in absence of a Consul client
agent.

It supports the following:

* Discovering Consul server addresses using
  [go-netaddrs](https://github.com/hashicorp/go-netaddrs) and Consul's [ServerWatch gRPC
  stream](https://github.com/hashicorp/consul/blob/main/proto-public/pbserverdiscovery/serverdiscovery.proto)
* Connecting to a Consul server over gRPC
* Automatic rediscovery and reconnection to another Consul server
* Consul ACL token authentication
* Compatibility with Consul server xDS load balancing
* Optional custom server filtering

## Usage

First, import the library:

```go
import "github.com/hashicorp/consul-server-connection-manager/discovery"
```

The following shows how to configure and start a `Watcher`. The
`Watcher` runs continually to discover Consul server addresses
and to maintain a current gRPC connection to a Consul server.

```go
watcher, err := discovery.NewWatcher(
    ctx,
    discovery.Config{
        Addresses: "exec=./discover-ips.sh",
        GRPCPort:  8502,
        TLS:       tlsCfg,
        Credentials: discovery.Credentials{
            Static: discovery.StaticTokenCredential{
                Token: testToken,
            },
        },
    },
    hclog.New(&hclog.LoggerOptions{
        Name: "server-connection-manager",
    }),
)
if err != nil {
    log.Fatal(err)
}

// Start the Watcher. It runs continually to maintain a current gRPC connection
// to one of the Consul servers.
go watcher.Run()

// Stop the Watcher when we are done.
// This will close the gRPC connection for you.
defer watcher.Stop()

// Get initial state. This blocks until a Consul server is discovered and until
// the Watcher has a successful connection to a Consul server.
state, err := watcher.State()
if err != nil {
    log.Fatal(err)
}

// Now use the gRPC connection to initalize your gRPC clients.
// This gRPC connection is valid as long as the Watcher is running.
// The connection automatically switches to another Consul as needed.
// If applicable, the ACL token is automatically injected into requests on the
// connection.
client := myclient.NewSampleClient(state.GRPCConn)
```

### Usage for HTTP Clients

The library does not currently integrate directly with HTTP clients, so
you must rebuild or update your HTTP client when the Watcher switches to
a new Consul server.

The following shows how to subscribe to receive an update each time the Watcher
switches to another Consul server.

```go
// Configure and start the Watcher.
watcher, err := discovery.NewWatcher(...)
if err != nil {
    log.Fatal(err)
}
go watcher.Run()
defer watcher.Stop()

// Wait for initial state.
state, err := watcher.State()
if err != nil {
    log.Fatal(err)
}

// Sample function to create a Consul HTTP API client,
// when using "github.com/hashicorp/consul/api".
//
// The state contains the server address and ACL token (if applicable).
makeClient := func(s discovery.State) *api.Client {
    cfg := api.DefaultConfig()
    // Append the Consul HTTP(S) port (8500 or 8501)
    cfg.Address = fmt.Sprintf("%s:%d", s.Address, 8501)
    cfg.Token = s.Token
    return api.NewClient(cfg)
}

// Create a client the first time.
client := makeClient(state)

// Subscribe to the Watcher. This returns a channel that receives
// a new discovery.State whenever the Watches connects to another
// Consul server
ch := watcher.Subscribe()

// Monitor the channel and rebuild the client when needed
for {
    select {
    case state := <-ch:
        client = makeClient(state)
    case <-ctx.Done():
        log.Fatal(ctx.Err())
    }
}
```

### Disabling the Server Watch

By default, the Watcher opens a
[`WatchServers`](https://github.com/hashicorp/consul/blob/main/proto-public/pbserverdiscovery/serverdiscovery.proto)
gRPC stream after connecting to a Consul server. This stream notifies us of new or changed Consul
server addresses.

The server watch stream should be disabled for cases where `WatchServers` returns different
addresses than those we should connect to. For example, if your Consul servers are behind a load
balancer, the library should connect to the load balancer address rather than directly to one of the
Consul server addresses.

Additionally, the `WatchServers` stream is not used if a particular Consul server does not support
`WatchServers`. The Watcher determines feature support by fetching the [supported dataplane
features](https://github.com/hashicorp/consul/blob/main/proto-public/pbdataplane/dataplane.proto)
and checking for the `DATAPLANE_FEATURES_WATCH_SERVERS` feature in the response.

When the server watch stream is disabled, the Watcher periodically makes a gRPC request to its
current server to check if it can still connect to that server. By default, it makes a gRPC request
once per minute, which can be changed by setting the `ServerWatchDisabledInterval`.

```go
cfg := discovery.Config{
    Addresses: "my.loadbalancer.example.com",
    // This disables use of the WatchServers stream.
    ServerWatchDisabled: true,
    // The following is the default value for a periodic connect to the server.
    ServerWatchDisabledInterval: 1 * time.Minute,
}
```

### Server Filtering

By default, the Watcher will choose any server at random. You can pass `ServerEvalFn` to have the
Watcher ignore certain servers. This function is called each time the Watcher selects a new server.
When the function returns false, the Watcher will ignore that server.

A common reason to set `ServerEvalFn` is to filter servers based on supported dataplane features,
for which you can use the `discovery.SupportsDataplaneFeatures` helper:

```go
cfg := discovery.Config{
    ServerEvalFn: discovery.SupportsDataplaneFeatures(
        pbdataplane.DataplaneFeatures_DATAPLANE_FEATURES_ENVOY_BOOTSTRAP_CONFIGURATION.String(),
    ),
}
```
