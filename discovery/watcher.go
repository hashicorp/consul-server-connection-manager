// Copyright IBM Corp. 2022, 2025
// SPDX-License-Identifier: MPL-2.0

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
	"github.com/hashicorp/go-metrics/compat"
	"google.golang.org/grpc"
	backoff2 "google.golang.org/grpc/backoff"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	expcredentials "google.golang.org/grpc/experimental/credentials"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/status"
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

	// reloginLock serializes auth method re-logins, and lastRelogin records when
	// the most recent one was attempted so that they can be rate limited.
	// Guarded the same way as switchLock, with a non-blocking TryLock, so that
	// a burst of Unauthenticated responses collapses into a single re-login.
	//
	// lastRelogin records attempts rather than successes, so that a bearer
	// token Consul keeps rejecting is rate limited too. It deliberately does
	// not track the initial login performed by connect(), since rate limiting
	// the first recovery would defeat the purpose.
	reloginLock sync.Mutex
	lastRelogin time.Time

	config Config
	log    hclog.Logger

	currentServer atomic.Value

	initComplete *event
	runComplete  *event
	runOnce      sync.Once

	conn  *grpc.ClientConn
	token atomic.Value

	resolver *watcherResolver

	acls *ACLs

	subscribers       []chan State
	subscriptionMutex sync.RWMutex

	// discoverer discovers IP addresses. In tests, we use mock this interface
	// to inject custom server ports.
	discoverer Discoverer
	// nodeToAddrFn parses and returns the address for the given Consul node
	// ID. In tets, we use this to inject custom server ports.
	nodeToAddrFn func(nodeID, addr string) (Addr, error)
	// clock provides time functions. Mocking this enables us to control time
	// for unit tests.
	clock Clock
}

type serverState struct {
	addr              Addr
	dataplaneFeatures map[string]bool
}

func NewWatcher(ctx context.Context, config Config, log hclog.Logger) (*Watcher, error) {
	if log == nil {
		log = hclog.NewNullLogger()
	}

	config = config.withDefaults()

	w := &Watcher{
		config:       config,
		log:          log,
		resolver:     newResolver(log),
		discoverer:   NewNetaddrsDiscoverer(config, log),
		initComplete: newEvent(),
		runComplete:  newEvent(),
		nodeToAddrFn: func(_, addr string) (Addr, error) {
			return MakeAddr(addr, config.GRPCPort)
		},
		clock: &SystemClock{},
	}
	w.ctx, w.ctxCancel = context.WithCancel(ctx)
	w.currentServer.Store(serverState{})
	w.token.Store("")

	var cred credentials.TransportCredentials
	if tls := w.config.TLS; tls != nil {
		cred = expcredentials.NewTLSWithALPNDisabled(tls)
	} else {
		cred = insecure.NewCredentials()
	}
	dialOpts := []grpc.DialOption{
		grpc.WithTransportCredentials(cred),
		grpc.WithUnaryInterceptor(makeUnaryInterceptor(w)),
		grpc.WithStreamInterceptor(makeStreamInterceptor(w)),
		// These keepalive parameters were chosen to match the existing behavior of
		// Consul agents [1].
		//
		// Consul servers have a policy to terminate connections that send keepalive
		// pings more frequently than every 15 seconds [2] so we need to choose an
		// interval larger than that.
		//
		// Some users choose to front their Consul servers with an AWS Network Load
		// Balancer, which has a hard idle timeout of 350 seconds [3] so we need to
		// choose an interval smaller than that.
		//
		//	1. https://github.com/hashicorp/consul/blob/e6b55d1d81c6e90dd5d09e7dfb24d1db7604b7b5/agent/grpc-internal/client.go#L137-L151
		//	2. https://github.com/hashicorp/consul/blob/e6b55d1d81c6e90dd5d09e7dfb24d1db7604b7b5/agent/grpc-external/server.go#L44-L47
		//	3. https://docs.aws.amazon.com/elasticloadbalancing/latest/network/network-load-balancers.html#connection-idle-timeout
		grpc.WithKeepaliveParams(keepalive.ClientParameters{
			Time:    30 * time.Second,
			Timeout: 30 * time.Second,
		}),
		// note: experimental apis
		grpc.WithResolvers(w.resolver),
		grpc.WithConnectParams(grpc.ConnectParams{
			Backoff:           backoff2.DefaultConfig,
			MinConnectTimeout: 10 * time.Second,
		}),
		// Note: We rely on the behavior of the default pick_first balancer [1]
		// to track the currently active address. Update loadBalancing policy with caution.
		//
		// [1]: https://github.com/grpc/grpc/blob/master/doc/load-balancing.md#pick_first
	}

	// Dial with "consul://" to trigger our custom resolver. We don't
	// provide a server address. The connection will be updated by the
	// Watcher via the custom resolver once an address is known.
	//nolint:staticcheck
	// We want to retain the legacy behavior of grpc.DialContext.
	conn, err := grpc.DialContext(ctx, "consul://", dialOpts...)
	if err != nil {
		return nil, err
	}

	w.conn = conn

	return w, nil
}

func (w *Watcher) Subscribe() <-chan State {
	w.subscriptionMutex.Lock()
	defer w.subscriptionMutex.Unlock()

	ch := make(chan State, 1)
	w.subscribers = append(w.subscribers, ch)
	return ch
}

func (w *Watcher) notifySubscribers() {
	w.subscriptionMutex.RLock()
	defer w.subscriptionMutex.RUnlock()

	state := w.currentState()
	for _, ch := range w.subscribers {
		select {
		case ch <- state:
			// success
		default:
			// could not send; updated dropped!
		}
	}
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
	w.runOnce.Do(func() {
		defer w.runComplete.SetDone()
		w.run(w.config.BackOff.getPolicy(), w.nextServer)
	})
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

// Stop stops the Watcher after Run is called. This logs out of the auth method,
// if applicable, waits for the Watcher's Run method to return, and closes the
// gRPC connection. After calling Stop, the Watcher and the gRPC connection are
// no longer valid to use.
func (w *Watcher) Stop() {
	w.log.Info("stopping")

	// If applicable, attempt to log out. This must be done prior to the
	// connection being closed. We ignore errors since we must continue to shut
	// down.
	//
	// Do not use w.ctx which is likely already cancelled at this point.
	if w.acls != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		err := w.acls.Logout(ctx)
		if err != nil {
			if err != ErrAlreadyLoggedOut {
				w.log.Error("ACL auth method logout failed", "error", err)
			}
		} else {
			w.log.Info("ACL auth method logout succeeded")
		}
	}

	// canceling the context will abort w.run()
	w.ctxCancel()
	// w.run() sets runComplete when it returns
	<-w.runComplete.Done()

	w.conn.Close()
}

func (w *Watcher) run(bo backoff.BackOff, nextServer func(*addrSet) error) {
	// addrs is the current set of servers we know about.
	addrs := newAddrSet()

	for {
		resetTime := w.clock.Now().Add(w.config.BackOff.ResetInterval)

		// Find and connect to a server.
		//
		// nextServer picks a server and tries to connect to it. It blocks
		// while connected to the server. It always returns an error on failure
		// or when disconnected.
		metrics.SetGauge([]string{"consul_connected"}, 0)
		w.log.Info("trying to connect to a Consul server")
		err := nextServer(addrs)
		if err != nil {
			logNonContextError(w.log, "connection error", err)
		}
		metrics.SetGauge([]string{"consul_connected"}, 0)

		// Reset the backoff if nextServer took longer than the reset interval.
		if w.clock.Now().After(resetTime) {
			bo.Reset()
		}

		// Retry with backoff.
		duration := bo.NextBackOff()
		if duration == backoff.Stop {
			// We should not hit this since we set MaxElapsedTime = 0.
			w.log.Warn("backoff stopped; aborting")
			return
		}

		w.log.Debug("backoff", "retry after", duration)
		select {
		case <-w.ctx.Done():
			w.log.Debug("aborting", "error", w.ctx.Err())
			return
		case <-w.clock.After(duration):
		}
	}
}

// nextServer does everything necessary to find and connect to a server.
// It runs discovery, selects a server, connects to it, and then blocks
// while it is connected, watching for server set changes.
//
// nextServer returns on any error, such as failure to connect or upon being
// disconnected for any reason. It should always return with a non-nil erorr.
func (w *Watcher) nextServer(addrs *addrSet) error {
	w.log.Trace("Watcher.nextServer", "addrs", addrs.String())

	w.switchLock.Lock()
	w.ctxForSwitch, w.cancelForSwitch = context.WithCancel(w.ctx)
	w.switchLock.Unlock()
	start := w.clock.Now()

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

	w.currentServer.Store(serverState{})

	if addrs.AllAttempted() {
		// Re-run discovery. We've attempted all servers since the last time
		// addresses were updated (by discovery or by the server watch stream,
		// if enabled). Or, we have no addresses.
		//
		// We run discovery at these times:
		// - At startup: when we have no addresses.
		// - If the server watch is disabled: After attempting each address once.
		// - If the server watch is enabled: Only after attempting each address
		//   once since the last successful update from the server watch stream.
		//   If the server watch stream is enabled and generally healthy
		//   then we will rarely ever re-run discovery.
		found, err := w.discoverer.Discover(w.ctx)
		if err != nil {
			return fmt.Errorf("failed to discover Consul server addresses: %w", err)
		}
		w.log.Info("discovered Consul servers", "addresses", found)

		// This preserves lastAttempt timestamps for addresses currently in
		// addrs, so that we try the least-recently attempted server. This
		// ensures we try all servers once before trying the same server twice.
		addrs.SetAddrs(found...)
	}

	// Choose a server as "current" and connect to it.
	//
	// Addresses are sorted first by unattempted, and then by lastAttempt timestamp
	// so that we always try the least-recently attempted address.
	sortedAddrs := addrs.Sorted()
	w.log.Info("current prioritized list of known Consul servers", "addresses", sortedAddrs)

	if !addrs.AllAttempted() {
		addr := sortedAddrs[0]
		addrs.SetAttemptTime(addr)

		server, err := w.connect(addr)
		if err != nil {
			// Return here in order to backoff between attempts to each server.
			return err
		}
		w.currentServer.Store(server)
	}

	current := w.currentServer.Load().(serverState)
	if current.addr.Empty() {
		return fmt.Errorf("unable to connect to a Consul server")
	}

	if eval := w.config.ServerEvalFn; eval != nil {
		state := w.currentState()
		if !eval(state) {
			w.currentServer.Store(serverState{})
			return fmt.Errorf("skipping Consul server %q because ServerEvalFn returned false", state.Address.String())
		}
	}
	metrics.MeasureSince([]string{"connect_duration"}, start)
	metrics.SetGauge([]string{"consul_connected"}, 1)

	w.log.Info("connected to Consul server", "address", current.addr)

	// Set init complete here. This indicates to Run() that initialization
	// completed: we found a server, have a token (if any), fetched dataplane
	// features, and the ServerEvalFn (if any) did not reject the server.
	w.initComplete.SetDone()

	w.notifySubscribers()

	// Wait forever while connected to this server, until an error or
	// disconnect for any reason.
	return w.watch(addrs)
}

// connect does initialization for the given address. This includes updating the
// gRPC connection to use that address, doing the ACL token login (one time
// only) and grabbing dataplane features for this server.
func (w *Watcher) connect(addr Addr) (serverState, error) {
	w.log.Trace("Watcher.connect", "addr", addr)

	// Tell the gRPC connection to switch to the selected server.
	w.log.Debug("switching to Consul server", "address", addr)
	err := w.switchServer(addr)
	if err != nil {
		return serverState{}, fmt.Errorf("failed to switch to Consul server %q: %w", addr, err)
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
				// Guarded because the gRPC error interceptor may read w.acls
				// concurrently in order to trigger a re-login.
				w.reloginLock.Lock()
				if w.acls == nil {
					w.acls = newACLs(w.conn, w.config)
				}
				acls := w.acls
				w.reloginLock.Unlock()

				accessorId, secretId, err := acls.Login(w.ctx)
				if err != nil {
					if err != ErrAlreadyLoggedIn {
						w.log.Error("ACL auth method login failed", "error", err)
						return serverState{}, err
					}
					// A concurrent re-login already obtained a token and stored
					// it in w.token. secretId is empty in this case, so it must
					// not be stored: doing so would discard that token and leave
					// the Watcher with no credentials at all.
					w.log.Debug("ACL auth method login skipped; already logged in")
				} else {
					w.log.Info("ACL auth method login succeeded", "accessorID", accessorId)
					w.token.Store(secretId)
				}
			}
		}
	}

	// Fetch dataplane features for this server. This is the first request that
	// uses the token we just obtained, so it is where a token that was deleted
	// between login and first use surfaces, as codes.Unauthenticated.
	//
	// Recover by discarding the cached credentials and logging in again. Without
	// this, the caller's retry loop would keep presenting the same dead token
	// indefinitely, because both w.token and ACLs.token are still populated and
	// suppress any further login attempt.
	staleToken, _ := w.token.Load().(string)
	features, err := w.getDataplaneFeatures()
	if err != nil {
		if !w.recoverFromUnauthenticated(err, staleToken) {
			return serverState{}, err
		}
		features, err = w.getDataplaneFeatures()
		if err != nil {
			return serverState{}, err
		}
	}

	for name, supported := range features {
		w.log.Debug("feature", "supported", supported, "name", name)
	}

	return serverState{addr: addr, dataplaneFeatures: features}, nil
}

// switchServer updates the gRPC connection to use the given server.
// If switchServer returns without error, we are guaranteed that
// subsequent gRPC requests will not use the old subconnection because
// the [pick_first] balancer synchronously removes the subconnection
// before establishing a new one. The subsequent request will block until
// the new subconnection is ready.
//
// [pick_first]: https://github.com/grpc/grpc/blob/master/doc/load-balancing.md#pick_first
func (w *Watcher) switchServer(to Addr) error {
	w.log.Trace("Watcher.switchServer", "to", to)
	w.switchLock.Lock()
	defer w.switchLock.Unlock()

	return w.resolver.SetAddress(to)
}

// requestServerSwitch requests a switch to some other server. This is safe to
// call from other goroutines. It does not block to wait for the server switch.
//
// This works by canceling a context (w.ctxForSwitch) to abort certain types of
// Watcher operations. This induces an error that causes the Watcher to
// disconnect from it's current server and pick a new one, which is the same
// logic as for any other type of error. This is kind of indirect, but also
// nice in that we don't have to special case much here.
func (w *Watcher) requestServerSwitch() {
	w.log.Trace("Watcher.requestServerSwitch")
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

// recoverFromUnauthenticated obtains a new ACL token if err is a
// codes.Unauthenticated status and the Watcher is using auth method
// credentials. It reports whether a usable new token is now in place, so the
// caller can retry the request that failed.
//
// staleToken is the token the failed request was made with. If a re-login is
// declined because one just happened on another goroutine, typically from the
// gRPC error interceptor, the token is compared against staleToken so that the
// caller still retries with the credentials that concurrent login produced.
//
// It returns false, without attempting anything, for any other error code and
// for static credentials, where there is nothing to re-acquire and
// Unauthenticated means a misconfiguration that should stay visible.
func (w *Watcher) recoverFromUnauthenticated(err error, staleToken string) bool {
	if status.Code(err) != codes.Unauthenticated {
		return false
	}
	if w.config.Credentials.Type != CredentialsTypeLogin {
		return false
	}
	if w.relogin() {
		return true
	}

	current, _ := w.token.Load().(string)
	return current != "" && current != staleToken
}

// reloginOnUnauthenticated is the entry point used by the gRPC error
// interceptor, which sees Unauthenticated responses to every request on the
// connection, not just the ones connect() makes.
func (w *Watcher) reloginOnUnauthenticated() {
	if w.config.Credentials.Type != CredentialsTypeLogin {
		return
	}

	// Only react if we actually presented a token. With nothing in w.token the
	// request went out unauthenticated, so the rejection does not mean "our
	// token was deleted": it is the Login call itself being refused, or a
	// request issued before the initial login completed. Logging in again is
	// not the right response to either, and doing so would turn a rejected
	// bearer token into a second login attempt on every failed request.
	if token, _ := w.token.Load().(string); token == "" {
		return
	}

	w.relogin()
}

// relogin discards the cached ACL token and obtains a new one from the auth
// method. It reports whether a fresh token was stored.
//
// Both caches have to be cleared for this to work. ACLs.Reset clears the token
// inside ACLs, which otherwise makes Login return ErrAlreadyLoggedIn, and
// w.token is cleared because connect() only attempts a login while it is empty.
// Clearing just one of them leaves the Watcher stuck on the old token.
//
// This is safe to call from any goroutine, including from the gRPC error
// interceptor. It does not block: if a re-login is already running, or if one
// completed less than MinReloginInterval ago, it returns immediately.
func (w *Watcher) relogin() bool {
	if !w.reloginLock.TryLock() {
		// re-login currently in progress. This also stops the Login call below
		// from recursing, since that request passes back through the gRPC error
		// interceptor that may have called us.
		return false
	}
	defer w.reloginLock.Unlock()

	if w.acls == nil {
		// Not logged in yet; connect() has not run the initial login.
		return false
	}

	// Rate limit, so that a burst of rejected requests, or a bearer token that
	// is itself invalid, cannot become a login storm against the auth method.
	if !w.lastRelogin.IsZero() && w.clock.Now().Sub(w.lastRelogin) < w.config.MinReloginInterval {
		w.log.Debug("skipping ACL auth method re-login; too soon since the last one")
		metrics.IncrCounterWithLabels([]string{"relogins"}, 1,
			[]metrics.Label{{Name: "result", Value: "rate_limited"}})
		return false
	}

	w.log.Warn("ACL token rejected as Unauthenticated; it may have been deleted, re-logging in")

	// Record the attempt before making it, not after it succeeds. A bearer
	// token that Consul rejects makes Login fail every time, and if only
	// successes were recorded the rate limit would never engage and each
	// rejected request would trigger another login.
	w.lastRelogin = w.clock.Now()

	// Only the ACLs cache is cleared here. w.token deliberately keeps the old
	// value until a replacement is available: interceptContext reads it on
	// every outbound request, so blanking it first would send untokenized
	// requests for the duration of the login.
	w.acls.Reset()

	accessorId, secretId, err := w.acls.Login(w.ctx)
	if err != nil {
		w.log.Error("ACL auth method re-login failed", "error", err)
		metrics.IncrCounterWithLabels([]string{"relogins"}, 1,
			[]metrics.Label{{Name: "result", Value: "failure"}})
		return false
	}
	w.log.Info("ACL auth method re-login succeeded", "accessorID", accessorId)
	metrics.IncrCounterWithLabels([]string{"relogins"}, 1,
		[]metrics.Label{{Name: "result", Value: "success"}})
	w.token.Store(secretId)

	// Consumers hold their own copy of the token from Subscribe/State, so they
	// must be told about the new one or they will keep using the deleted token.
	w.notifySubscribers()
	return true
}

func (w *Watcher) getDataplaneFeatures() (map[string]bool, error) {
	client := pbdataplane.NewDataplaneServiceClient(w.conn)
	resp, err := client.GetSupportedDataplaneFeatures(w.ctxForSwitch, &pbdataplane.GetSupportedDataplaneFeaturesRequest{})
	if err != nil {
		return nil, fmt.Errorf("fetching supported dataplane features: %w", err)
	}

	// Translate features to a map, so that we don't have to pass gRPC
	// types back to users.
	features := map[string]bool{}
	for _, feat := range resp.SupportedDataplaneFeatures {
		nameStr := pbdataplane.DataplaneFeatures_name[int32(feat.FeatureName)]
		features[nameStr] = feat.GetSupported()
	}

	return features, nil
}

// watch blocks to wait for server set changes. It aborts on receiving an error
// from the server, including when the Watcher's context is cancelled, and
// including when the Watcher is told to switch servers.
//
// This updates addrs in place to add or remove servers found from the server
// watch stream, if applicable.
func (w *Watcher) watch(addrs *addrSet) error {
	current := w.currentServer.Load().(serverState)
	if current.dataplaneFeatures["DATAPLANE_FEATURES_WATCH_SERVERS"] && !w.config.ServerWatchDisabled {
		return w.watchStream(addrs)
	} else {
		return w.watchSleep()
	}
}

// watchStream opens a gRPC stream to receive server set changes. This blocks
// potentially forever. It updates addrs in place, adding or removing servers
// to match the response from the server watch stream.
//
// This may be aborted when the gRPC stream receives some error, when the
// Watcher's context is cancelled, or when the Watcher is told to switch
// servers.
func (w *Watcher) watchStream(addrs *addrSet) error {
	w.log.Trace("Watcher.watchStream")
	client := pbserverdiscovery.NewServerDiscoveryServiceClient(w.conn)
	serverStream, err := client.WatchServers(w.ctxForSwitch, &pbserverdiscovery.WatchServersRequest{})
	if err != nil {
		logNonContextError(w.log, "failed to open server watch stream", err)
		return err
	}

	for {
		// This blocks until there is a change from the server.
		resp, err := serverStream.Recv()
		if err != nil {
			return err
		}

		// Collect addresses from the stream.
		streamAddrs := []Addr{}
		for _, srv := range resp.Servers {
			addr, err := w.nodeToAddrFn(srv.Id, srv.Address)
			if err != nil {
				// ignore the server on failure.
				w.log.Warn("failed to parse server address from server watch stream; ignoring address", "error", err)
				continue
			}
			streamAddrs = append(streamAddrs, addr)
		}

		// Update the addrSet. This sets the lastUpdated timestamp for each address,
		// and removes servers not present in the server watch stream.
		addrs.SetAddrs(streamAddrs...)
		w.log.Info("updated known Consul servers from watch stream", "addresses", addrs.Sorted())
	}
}

// watchSleep is used when the server watch stream is not supported.
// It may be interrupted if we are told to switch servers.
func (w *Watcher) watchSleep() error {
	w.log.Trace("Watcher.watchSleep", "interval", w.config.ServerWatchDisabledInterval)

	for {
		select {
		case <-w.ctxForSwitch.Done():
			return w.ctxForSwitch.Err()
		case <-w.clock.After(w.config.ServerWatchDisabledInterval):
		}

		// is the server still OK?
		_, err := w.getDataplaneFeatures()
		if err != nil {
			return fmt.Errorf("failed to reach Consul server with server watch stream disabled: %w", err)
		}
	}
}

func logNonContextError(log hclog.Logger, msg string, err error, args ...interface{}) {
	args = append(args, "error", err)

	s, ok := status.FromError(err)
	if errors.Is(err, context.Canceled) || (ok && s.Code() == codes.Canceled) {
		log.Trace("context error: "+msg, args...)
	} else {
		log.Error(msg, args...)
	}
}
