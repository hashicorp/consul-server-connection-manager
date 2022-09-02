package discovery

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cenkalti/backoff/v4"
	"github.com/hashicorp/consul/proto-public/pbdataplane"
	"github.com/hashicorp/consul/proto-public/pbserverdiscovery"
	"github.com/hashicorp/go-hclog"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
)

// State is the info a caller wants to know after initialization.
type State struct {
	// GRPCConn is the gRPC connection shared with this library. Use
	// this to create your gRPC clients. The gRPC connection is
	// automatically updated to switch to a new server, so you can
	// use this connection for the lifetime of the associated
	// Watcher.
	GRPCConn *grpc.ClientConn

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

type Watcher struct {
	// This is the "top-level" internal context. This is used to cancel the
	// Watcher's Run method, including the gRPC connection.
	ctx       context.Context
	ctxCancel context.CancelFunc

	// This is an internal sub-context used to cancel operations in order to
	// switch servers.
	ctxForSwitch    context.Context
	cancelForSwitch context.CancelFunc
	switchLock      sync.Mutex

	config Config
	log    hclog.Logger

	currentServer atomic.Value

	initComplete *event
	runComplete  *event
	runOnce      sync.Once

	backoff backoff.BackOff
	conn    *grpc.ClientConn
	token   atomic.Value

	resolver *watcherResolver
	balancer *watcherBalancer

	// interface to inject custom server ports for tests
	discoverer Discoverer
	// function to inject custom server ports for tests
	nodeToAddrFn func(nodeID, addr string) (Addr, error)
}

type serverState struct {
	addr              Addr
	dataplaneFeatures map[string]bool
}

func NewWatcher(ctx context.Context, config Config, log hclog.Logger) (*Watcher, error) {
	if log == nil {
		log = hclog.NewNullLogger()
	}

	// TODO: config for backoff values
	backoff := backoff.NewExponentialBackOff()
	backoff.MaxElapsedTime = 0 // Allow backing off forever.

	config = config.withDefaults()

	w := &Watcher{
		config:       config,
		log:          log,
		backoff:      backoff,
		resolver:     newResolver(log),
		discoverer:   NewNetaddrsDiscoverer(config, log),
		initComplete: newEvent(),
		runComplete:  newEvent(),
		nodeToAddrFn: func(_, addr string) (Addr, error) {
			return MakeAddr(addr, config.GRPCPort)
		},
	}
	w.ctx, w.ctxCancel = context.WithCancel(ctx)
	w.currentServer.Store(serverState{})
	w.token.Store("")

	var cred credentials.TransportCredentials
	if tls := w.config.TLS; tls != nil {
		cred = credentials.NewTLS(tls)
	} else {
		cred = insecure.NewCredentials()
	}
	dialOpts := []grpc.DialOption{
		grpc.WithTransportCredentials(cred),
		grpc.WithUnaryInterceptor(makeUnaryInterceptor(w)),
		grpc.WithStreamInterceptor(makeStreamInterceptor(w)),
		// note: experimental apis
		grpc.WithResolvers(w.resolver),
		grpc.WithDefaultServiceConfig(
			fmt.Sprintf(`{"loadBalancingPolicy": "%s"}`, registerBalancer(w, log)),
		),
	}

	// Dial with "consul://" to trigger our custom resolver. We don't
	// provide a server address. The connection will be updated by the
	// Watcher via the custom resolver once an address is known.
	conn, err := grpc.DialContext(w.ctx, "consul://", dialOpts...)
	if err != nil {
		return nil, err
	}
	w.conn = conn

	return w, nil
}

func (w *Watcher) Subscribe() chan State {
	// TODO: add this
	panic("unimplemented")
}

// Run watches for Consul server set changes forever. Run should be called in a
// goroutine. Run can be aborted by cancelling the context passed to NewWatcher
// or by calling Stop. Call State after Run in order to wait for initialization
// to complete.
//
//	w, _ := NewWatcher(ctx, ...)
//	go w.Run()
//	state, err := w.State()
func (w *Watcher) Run() {
	w.runOnce.Do(w.run)
}

// State returns the current state. This blocks for initialization to complete,
// after which it will have found a Consul server, completed ACL token login
// (if applicable), and retrieved supported dataplane features.
//
// Run must be called or State will never return. State can be aborted by
// cancelling the context passed to NewWatcher or by calling Stop.
func (w *Watcher) State() (State, error) {
	err := w.initComplete.Wait(w.ctx)
	if err != nil {
		return State{}, err
	}
	return w.currentState(), nil
}

func (w *Watcher) currentState() State {
	current := w.currentServer.Load().(serverState)
	return State{
		GRPCConn:          w.conn,
		Token:             w.token.Load().(string),
		Address:           current.addr,
		DataplaneFeatures: current.dataplaneFeatures,
	}
}

// Stop stops the Watcher after Run is called.
// This cancels the Watcher's internal context.
func (w *Watcher) Stop() {
	// canceling the context will abort w.run()
	w.ctxCancel()
	// w.run() sets runComplete when it returns
	<-w.runComplete.Done()

	w.conn.Close()
	// TODO: acl token logout?
}

func (w *Watcher) run() {
	defer w.runComplete.SetDone()

	// addrs is the current set of servers we know about.
	var addrs *addrSet
	var err error

	for {
		// Find and connect to a server.
		//
		// When this successfully connects to a server, it returns the chosen
		// server and the latest set of server addresses. If we get an error,
		// then we retry with backoff.
		addrs, err = w.nextServer(addrs)
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
		case <-w.ctx.Done():
			w.log.Warn("aborting", "err", w.ctx.Err())
			return
		case <-time.After(duration):
		}
	}
}

func (w *Watcher) nextServer(addrs *addrSet) (*addrSet, error) {
	w.log.Debug("Watcher.nextServer", "addrs", addrs.String())

	w.switchLock.Lock()
	w.ctxForSwitch, w.cancelForSwitch = context.WithCancel(w.ctx)
	w.switchLock.Unlock()

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

	// Choose a server from the known "healthy" servers.
	// If none are healthy, re-run address discovery.
	w.currentServer.Store(serverState{})

	var healthy []Addr
	if addrs != nil {
		healthy = addrs.Get(OK)
	}
	if len(healthy) == 0 {
		// No healthy servers. Re-run discovery.
		found, err := w.discover()
		if err != nil {
			return nil, err
		}
		addrs = found
		healthy = addrs.Get(OK)
	}

	if len(healthy) > 0 {
		// Choose a server as "current" and connect to it.
		addr := healthy[0]
		server, err := w.connect(addr)
		if err != nil {
			addrs.Put(NotOK, addr)
			// Return here in order to backoff between attempts to each server.
			return addrs, err
		}
		w.currentServer.Store(server)

	}

	current := w.currentServer.Load().(serverState)
	if current.addr.Empty() {
		return addrs, fmt.Errorf("unable to connect to a server")
	}

	if eval := w.config.ServerEvalFn; eval != nil {
		state := w.currentState()
		if !eval(state) {
			addrs.Put(NotOK, state.Address)
			w.currentServer.Store(serverState{})
			return addrs, fmt.Errorf("ServerEvalFn returned false for server: %q", state.Address.String())
		}
	}

	// Set init complete here. This indicates to Run() that initialization
	// completed: we found a server, have a token (if any), fetched dataplane
	// features, and the ServerEvalFn (if any) did not reject the server.
	w.initComplete.SetDone()

	w.log.Debug("connected to server", "addr", current.addr)
	// TODO: if the current server changed, notify subscribers at this point.

	newAddrs, err := w.watch()
	if newAddrs != nil {
		addrs = newAddrs
	}
	if err != nil {
		addrs.Put(NotOK, current.addr)
	}
	return addrs, err
}

// discover runs (go-netaddrs) discovery to find server addresses.
// It returns the set of found addresses, all marked "OK".
func (w *Watcher) discover() (*addrSet, error) {
	addrs, err := w.discoverer.Discover(w.ctx)
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
func (w *Watcher) connect(addr Addr) (serverState, error) {
	w.log.Debug("Watcher.connect", "addr", addr)

	// Tell the gRPC connection to switch to the selected server.
	err := w.switchServer(addr)
	if err != nil {
		return serverState{}, err
	}

	// One time, do the ACL token login.
	select {
	case <-w.initComplete.Done():
		// already done
	default:
		if w.token.Load().(string) == "" {
			switch w.config.Credentials.Type {
			case CredentialsTypeStatic:
				w.token.Store(w.config.Credentials.Static.Token)
			case CredentialsTypeLogin:
				// TODO: Support ACL token login.
				panic("acl token login is unimplemented")
			}
		}
	}

	// Fetch dataplane features for this server.
	features, err := w.getDataplaneFeatures()
	if err != nil {
		return serverState{}, err
	}

	return serverState{addr: addr, dataplaneFeatures: features}, nil
}

// switchServer updates the gRPC connection to use the given server. It blocks
// until the connection has switched over to the new server and is no longer
// trying to use any "old" server(s). We want to be pretty sure that, after
// this returns, the gRPC connection will send requests to the given server,
// since the actual address the conection is using is abstracted away.
func (w *Watcher) switchServer(to Addr) error {
	w.log.Debug("Watcher.switchServer", "to", to)
	w.switchLock.Lock()
	defer w.switchLock.Unlock()

	err := w.resolver.SetAddress(to)
	if err != nil {
		return err
	}
	return w.balancer.WaitForTransition(w.ctx, to)
}

// requestServerSwitch requests a switch to some other server. This is safe to
// call from other goroutines. It does not block to wait for the server switch.
//
// This works by canceling a context (w.ctxForSwitch) to abort certain types of
// Watcher operations. This induces an error that causes the Watcher to mark
// the server it's currently using "NotOK", which is  the same logic as for any
// other type of error. This is kind of indirect, but also nice in that we
// don't have to special case much here.
//
// Note that when a server is marked "NotOK", that status only persists until
// the next time the Watcher refreshes server IPs (via either discover() or
// watchStream()). If there is only one Consul server, or if the Watcher
// currently has only one "OK" server, then we would mark it "NotOK", re-run
// address discovery to get a fresh list of "OK" addresses, and then
// potentially reconnect to that same server we just switched away from (at
// random).
func (w *Watcher) requestServerSwitch() {
	w.log.Debug("Watcher.requestServerSwitch")
	if !w.switchLock.TryLock() {
		// switch currently in progress.
		return
	}
	defer w.switchLock.Unlock()

	// interrupt the Watcher.
	if w.cancelForSwitch != nil {
		w.cancelForSwitch()
	}
}

func (w *Watcher) getDataplaneFeatures() (map[string]bool, error) {
	client := pbdataplane.NewDataplaneServiceClient(w.conn)
	resp, err := client.GetSupportedDataplaneFeatures(w.ctxForSwitch, &pbdataplane.GetSupportedDataplaneFeaturesRequest{})
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

// watch blocks to wait for server set changes. This aborts on receiving an
// error from the server, when the Watcher's context is cancelled, or (TODO)
// when the Watcher is told to switch servers.
//
// This returns an addrSet containing the most recent set of servers. If the
// addrSet is nil, then there were not changes seen to the server set. (This is
// the case if the server watch stream is disabled.
//
// When this returns with an error, the current server should no longer be
// considered "OK". This may return a non-nil addrSet and a non-nil error (it
// will usually do this when the server watch stream is aborted).
func (w *Watcher) watch() (*addrSet, error) {
	current := w.currentServer.Load().(serverState)
	if current.dataplaneFeatures["DATAPLANE_FEATURES_WATCH_SERVERS"] && !w.config.ServerWatchDisabled {
		return w.watchStream()
	} else {
		return w.watchSleep()
	}
}

// watchStream opens a gRPC stream to receive server set changes. This blocks
// potentially forever.
//
// This may be aborted when the gRPC stream receives some error, when the
// Watcher's context is cancelled, or (TODO) when the Watcher is told to switch
// servers.
//
// If an error that aborts the gRPC stream, we pass the non-nil error back,
// and the server is marked unhealthy elsewhere.
func (w *Watcher) watchStream() (*addrSet, error) {
	w.log.Debug("Watcher.watchStream")
	client := pbserverdiscovery.NewServerDiscoveryServiceClient(w.conn)
	serverStream, err := client.WatchServers(w.ctxForSwitch, &pbserverdiscovery.WatchServersRequest{})
	if err != nil {
		w.log.Error("unable to open server watch stream", "error", err)
		return nil, err
	}

	var set *addrSet
	for {
		// This blocks until there is a change from the server.
		resp, err := serverStream.Recv()
		if err != nil {
			if !errors.Is(err, context.Canceled) {
				w.log.Error("unable to receive from server watch stream", "error", err)
			}
			return set, err
		}

		// The set of servers from the stream is the best known set of servers to use.
		set = newAddrSet()
		for _, srv := range resp.Servers {
			addr, err := w.nodeToAddrFn(srv.Id, srv.Address)
			if err != nil {
				// ignore the server on failure.
				w.log.Warn("failed to parse server address from watch stream", "error", err)
				continue
			}
			set.Put(OK, addr)
		}
	}
}

// watchSleep is used when the server watch stream is not supported.
// It may be interrupted if we are told to switch servers.
func (w *Watcher) watchSleep() (*addrSet, error) {
	w.log.Debug("Watcher.watchSleep", "interval", w.config.ServerWatchDisabledInterval)

	for {
		select {
		case <-w.ctxForSwitch.Done():
			return nil, w.ctxForSwitch.Err()
		case <-time.After(w.config.ServerWatchDisabledInterval):
		}

		// is the server still OK?
		_, err := w.getDataplaneFeatures()
		if err != nil {
			return nil, err
		}
	}
}
