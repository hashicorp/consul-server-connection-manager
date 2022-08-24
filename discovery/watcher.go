package discovery

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cenkalti/backoff/v4"
	"github.com/hashicorp/go-hclog"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/hashicorp/consul-server-connection-manager/internal/consul-proto/pbdataplane"
)

// InitState is the info a caller wants to know after initialization.
type InitState struct {
	// GRPCConn is the gRPC connection shared with this library. Use
	// this to create your gRPC clients. The gRPC connection is
	// automatically updated to switch to a new server, so you can
	// use this connection for the lifetime of the associated
	// Watcher.
	GRPCConn grpc.ClientConnInterface

	// Token is the ACL token obtain from logging in (if applicable).
	// If login is not supported this will be set to the static token
	// from the Config object.
	Token string

	// Address is the address of current the Consul server the Watcher is using.
	Address Addr

	// DataplaneFeatures contains the dataplane features supported by the
	// current Consul server.
	DataplaneFeatures map[string]bool
}

// SubscribeState is the info callers need to know when the Watcher's
// current server changes.
// TODO: Fill this in.
type SubscribeState struct{}

type Watcher struct {
	config Config
	log    hclog.Logger

	currentServer atomic.Value

	initComplete atomic.Value
	conn         *grpc.ClientConn
	resolver     *watcherResolver
	token        string

	backoff backoff.BackOff

	once sync.Once

	// interface to allow us to inject custom server ports for tests
	discoverer Discoverer
}

type serverState struct {
	addr              Addr
	dataplaneFeatures map[string]bool
}

func NewWatcher(config Config, log hclog.Logger) *Watcher {
	if log == nil {
		log = hclog.NewNullLogger()
	}

	// TODO: config for backoff values
	backoff := backoff.NewExponentialBackOff()
	backoff.MaxElapsedTime = 0 // Allow backing off forever.

	w := &Watcher{
		config:     config,
		log:        log,
		backoff:    backoff,
		resolver:   newResolver(log),
		discoverer: NewNetaddrsDiscoverer(config, log),
	}
	w.initComplete.Store(false)
	w.currentServer.Store(serverState{})
	return w
}

func (w *Watcher) Subscribe() chan SubscribeState {
	// TODO: add this
	panic("unimplemented")
}

// Run spawns a goroutine to watch for Consul server set changes.
// It blocks to wait for initialization to complete, and returns
// initial state or an error on failure.
func (w *Watcher) Run(ctx context.Context) (*InitState, error) {
	if w.conn == nil {
		var cred credentials.TransportCredentials
		if tls := w.config.TLS; tls != nil {
			cred = credentials.NewTLS(tls)
		} else {
			cred = insecure.NewCredentials()
		}

		dialOpts := []grpc.DialOption{
			grpc.WithTransportCredentials(cred),
			grpc.WithResolvers(w.resolver), // note: experimental api.
			// TODO: Add interceptors here.
			// TODO: Add custom grpc balancer here.
		}

		// Dial with "consul://" to trigger our custom resolver. We don't
		// provide a server address. The connection will be updated by the
		// Watcher via the custom resolver once an address is known.
		conn, err := grpc.DialContext(ctx, "consul://", dialOpts...)
		if err != nil {
			return nil, err
		}
		w.conn = conn
	}

	// Spawn our goroutine.
	w.once.Do(func() { go w.run(ctx) })

	// Wait for init to complete.
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}

		if w.initComplete.Load().(bool) {
			break
		}
	}

	current := w.currentServer.Load().(serverState)
	return &InitState{
		GRPCConn:          w.conn,
		Token:             w.token,
		Address:           current.addr,
		DataplaneFeatures: current.dataplaneFeatures,
	}, nil
}

func (w *Watcher) run(ctx context.Context) {
	// addrs is the current set of servers we know about.
	var addrs *addrSet
	var err error

	for {
		// Find and connect to a server.
		//
		// When this successfully connects to a server, it returns the chosen
		// server and the latest set of server addresses. If we get an error,
		// then we retry with backoff.
		addrs, err = w.nextServer(ctx, addrs)
		if err != nil {
			w.log.Error("run", "err", err.Error())
		}

		// Retry with backoff.
		//
		// TODO: If we are in an good state (no errors) for long enough, reset
		// the backoff so we aren't stuck with a long backoff interval forever.
		duration := w.backoff.NextBackOff()
		if duration == backoff.Stop {
			// We should not hit this since we set MaxElapsedTime = 0.
			w.log.Warn("backoff stopped; aborting")
			return
		}
		select {
		case <-ctx.Done():
			w.log.Warn("aborting", "err", ctx.Err())
			return
		case <-time.After(duration):
		}
	}
}

func (w *Watcher) nextServer(ctx context.Context, addrs *addrSet) (*addrSet, error) {
	w.log.Debug("Watcher.nextServer", "addrs", addrs.String())

	defer func() {
		// If we return without picking a server, then clear the gRPC connection's
		// address list. This prevents gRPC from retrying the connection to the server
		// faster than our own exponential backoff. While the gRPC connection has an
		// empty list of addresses, callers will see an error like "resolver error:
		// produced zero addresses" from their gRPC clients.
		current := w.currentServer.Load().(serverState)
		if current.addr.Empty() {
			_ = w.resolver.SetAddress(Addr{})
		}
	}()

	// Reuse the current server if it is still "OK"
	current := w.currentServer.Load().(serverState)
	if addrs != nil && addrs.Status(current.addr) == OK {
		// current server is okay
	} else {
		// Choose a server from the known "healthy" servers.
		// If none are healthy, re-run address discovery.
		// TODO: supporting filtering servers (by dataplane features)
		w.currentServer.Store(serverState{})

		var healthy []Addr
		if addrs != nil {
			healthy = addrs.Get(OK)
		}
		if len(healthy) == 0 {
			// No healthy servers. Re-run discovery.
			found, err := w.discover(ctx)
			if err != nil {
				return nil, err
			}
			addrs = found
			healthy = addrs.Get(OK)
		}

		if len(healthy) > 0 {
			// Choose a server as "current" and connect to it.
			addr := healthy[0]
			server, err := w.connect(ctx, addr)
			if err != nil {
				addrs.Put(NotOK, addr)
				// Return here in order to backoff between attempts to each server.
				return addrs, err
			}
			w.currentServer.Store(server)
		}
	}

	current = w.currentServer.Load().(serverState)
	if current.addr.Empty() {
		return addrs, fmt.Errorf("unable to connect to a server")
	}

	w.log.Debug("connected to server", "addr", current.addr)
	// TODO: if the current server changed, notify subscribers at this point.

	// TODO: wait for changes here (open the server watch stream, or sleep).
	// For now, just sleep.
	select {
	case <-ctx.Done():
		return addrs, ctx.Err()
	case <-time.After(5 * time.Second):
	}
	return addrs, nil
}

// discover runs (go-netaddrs) discovery to find server addresses.
// It returns the set of found addresses, all marked "OK".
func (w *Watcher) discover(ctx context.Context) (*addrSet, error) {
	addrs, err := w.discoverer.Discover(ctx)
	if err != nil {
		return nil, err
	}

	set := newAddrSet()
	set.Put(OK, addrs...)
	return set, nil
}

// connect does initialization for the given address. This includes updating the
// gRPC connection to use that address, doing the ACL token login (one time
// only) and grabbing dataplane features for this server.
func (w *Watcher) connect(ctx context.Context, addr Addr) (serverState, error) {
	w.log.Debug("Watcher.connect", "addr", addr)

	// Tell the gRPC connection to switch to the selected server.
	err := w.switchServer(ctx, addr)
	if err != nil {
		return serverState{}, err
	}

	// One time, do the ACL token login.
	if !w.initComplete.Load().(bool) && w.token == "" {
		switch w.config.Credentials.Type {
		case CredentialsTypeStatic:
			w.token = w.config.Credentials.Static.Token
		case CredentialsTypeLogin:
			// TODO: Support ACL token login.
			panic("acl token login is unimplemented")
		}
	}

	// Fetch dataplane features for this server.
	features, err := w.getDataplaneFeatures(ctx)
	if err != nil {
		return serverState{}, err
	}

	// Set init complete here. This indicates to Run() that initialization
	// we found a server, have a token, and fetched dataplane features.
	w.initComplete.Store(true)

	return serverState{addr: addr, dataplaneFeatures: features}, nil
}

// switchServer updates the gRPC connection to use the given server. It blocks
// until the connection has switched over to the new server and is no longer
// trying to use any "old" server(s). We want to be pretty sure that, after
// this returns, the gRPC connection will send requests to the given server,
// since the actual address the conection is using is abstracted away.
func (w *Watcher) switchServer(ctx context.Context, to Addr) error {
	w.log.Debug("Watcher.switchServer", "to", to)
	err := w.resolver.SetAddress(to)
	if err != nil {
		return err
	}
	// TODO: This sleep will be replaced with a custom grpc loadbalancer that
	// that looks at the state of underlying sub-connections.
	time.Sleep(5 * time.Second)
	return nil
}

func (w *Watcher) getDataplaneFeatures(ctx context.Context) (map[string]bool, error) {
	client := pbdataplane.NewDataplaneServiceClient(w.conn)
	resp, err := client.GetSupportedDataplaneFeatures(ctx, &pbdataplane.GetSupportedDataplaneFeaturesRequest{})
	if err != nil {
		return nil, fmt.Errorf("checking supported features: %w", err)
	}

	// Translate features to a map, so that we don't have to pass gRPC
	// types back to users.
	features := map[string]bool{}
	for _, feat := range resp.SupportedDataplaneFeatures {
		nameStr := pbdataplane.DataplaneFeatures_name[int32(feat.FeatureName)]
		supported := feat.GetSupported()
		w.log.Debug("feature", "supported", supported, "name", nameStr)
		features[nameStr] = supported
	}

	return features, nil
}
