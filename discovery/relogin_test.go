// Copyright IBM Corp. 2022, 2025
// SPDX-License-Identifier: MPL-2.0

package discovery

import (
	"context"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hashicorp/consul/proto-public/pbacl"
	"github.com/hashicorp/consul/proto-public/pbdataplane"
	"github.com/hashicorp/go-hclog"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// reloginACLServer hands back a distinguishable accessor per Login call so a
// test can tell which token the Watcher ended up holding.
type reloginACLServer struct {
	pbacl.UnimplementedACLServiceServer
	logins atomic.Int32
}

func (s *reloginACLServer) Login(context.Context, *pbacl.LoginRequest) (*pbacl.LoginResponse, error) {
	n := s.logins.Add(1)
	accessor := "accessor-first"
	if n > 1 {
		accessor = "accessor-second"
	}
	return &pbacl.LoginResponse{
		Token: &pbacl.LoginToken{AccessorId: accessor, SecretId: accessor + "-secret"},
	}, nil
}

// reloginDataplaneServer fails the first call with Unauthenticated, simulating
// a token deleted between login and first use, then succeeds.
type reloginDataplaneServer struct {
	pbdataplane.UnimplementedDataplaneServiceServer
	calls         atomic.Int32
	failEveryCall bool
	unauthUpTo    int32
}

func (s *reloginDataplaneServer) GetSupportedDataplaneFeatures(context.Context, *pbdataplane.GetSupportedDataplaneFeaturesRequest) (*pbdataplane.GetSupportedDataplaneFeaturesResponse, error) {
	n := s.calls.Add(1)
	if s.failEveryCall || n <= s.unauthUpTo {
		return nil, status.Error(codes.Unauthenticated, "ACL not found")
	}
	return &pbdataplane.GetSupportedDataplaneFeaturesResponse{}, nil
}

func startReloginServer(t *testing.T, dp *reloginDataplaneServer) (Addr, *reloginACLServer) {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	acl := &reloginACLServer{}
	s := grpc.NewServer()
	pbacl.RegisterACLServiceServer(s, acl)
	pbdataplane.RegisterDataplaneServiceServer(s, dp)
	go func() { _ = s.Serve(lis) }()
	t.Cleanup(s.Stop)

	tcp := lis.Addr().(*net.TCPAddr)
	addr, err := MakeAddr(tcp.IP.String(), tcp.Port)
	require.NoError(t, err)
	return addr, acl
}

func newReloginWatcher(t *testing.T, cfg Config) *Watcher {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)

	w, err := NewWatcher(ctx, cfg, hclog.NewNullLogger())
	require.NoError(t, err)
	t.Cleanup(w.ctxCancel)

	// connect() expects ctxForSwitch; nextServer() normally sets it.
	w.ctxForSwitch = w.ctx
	return w
}

func loginConfig() Config {
	return Config{
		Credentials: Credentials{
			Type: CredentialsTypeLogin,
			Login: LoginCredential{
				AuthMethod:  "kubernetes",
				BearerToken: "fake-bearer-token",
			},
		},
	}
}

// TestReloginAfterUnauthenticated is the regression test for the case where an
// ACL token is deleted between login and first use. Before the fix, connect()
// returned the Unauthenticated error and every retry reused the dead token,
// because both w.token and ACLs.token remained populated.
func TestReloginAfterUnauthenticated(t *testing.T) {
	dp := &reloginDataplaneServer{unauthUpTo: 1}
	addr, acl := startReloginServer(t, dp)

	w := newReloginWatcher(t, loginConfig())

	state, err := w.connect(addr)
	require.NoError(t, err, "connect should recover by logging in again")
	require.Equal(t, addr, state.addr)

	require.EqualValues(t, 2, acl.logins.Load(), "expected initial login plus one re-login")
	require.EqualValues(t, 2, dp.calls.Load(), "expected the failed call to be retried once")

	// The critical assertion: the Watcher must be holding the NEW token, not
	// the stale one it originally logged in with.
	require.Equal(t, "accessor-second-secret", w.token.Load().(string))
}

// TestReloginRateLimited verifies that re-logins are rate limited, so a burst
// of Unauthenticated responses cannot become a login storm.
func TestReloginRateLimited(t *testing.T) {
	dp := &reloginDataplaneServer{failEveryCall: true}
	addr, acl := startReloginServer(t, dp)

	cfg := loginConfig()
	cfg.MinReloginInterval = time.Hour
	w := newReloginWatcher(t, cfg)

	_, err := w.connect(addr)
	require.Error(t, err, "server rejects every call, so connect must fail")

	// Initial login plus exactly one re-login; the rate limit blocks the rest.
	require.EqualValues(t, 2, acl.logins.Load())

	// Further attempts are suppressed while inside the interval.
	require.False(t, w.relogin(), "re-login within MinReloginInterval must be skipped")
	require.EqualValues(t, 2, acl.logins.Load())
}

// TestReloginSkippedForStaticCredentials ensures static tokens are untouched:
// Unauthenticated there means misconfiguration and must stay visible.
func TestReloginSkippedForStaticCredentials(t *testing.T) {
	dp := &reloginDataplaneServer{failEveryCall: true}
	addr, acl := startReloginServer(t, dp)

	w := newReloginWatcher(t, Config{
		Credentials: Credentials{
			Type:   CredentialsTypeStatic,
			Static: StaticTokenCredential{Token: "static-token"},
		},
	})

	_, err := w.connect(addr)
	require.Error(t, err)
	require.EqualValues(t, codes.Unauthenticated, status.Code(err), "error must surface unchanged")
	require.EqualValues(t, 0, acl.logins.Load(), "static credentials must never log in")
}

// TestReloginIgnoresOtherErrorCodes ensures only Unauthenticated triggers a
// re-login.
func TestReloginIgnoresOtherErrorCodes(t *testing.T) {
	w := newReloginWatcher(t, loginConfig())
	require.False(t, w.recoverFromUnauthenticated(status.Error(codes.Unavailable, "nope"), "tok"))
	require.False(t, w.recoverFromUnauthenticated(status.Error(codes.ResourceExhausted, "nope"), "tok"))
}

// failingACLServer always rejects the login, standing in for a bearer token
// that Consul will not accept.
type failingACLServer struct {
	pbacl.UnimplementedACLServiceServer
	logins atomic.Int32
}

func (s *failingACLServer) Login(context.Context, *pbacl.LoginRequest) (*pbacl.LoginResponse, error) {
	s.logins.Add(1)
	return nil, status.Error(codes.PermissionDenied, "bearer token rejected")
}

// TestReloginFailuresAreRateLimited guards a bug found in review: recording
// lastRelogin only after a *successful* login means a bearer token that always
// fails never engages the rate limit, so every rejected request triggers
// another login attempt.
func TestReloginFailuresAreRateLimited(t *testing.T) {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	acl := &failingACLServer{}
	dp := &reloginDataplaneServer{failEveryCall: true}
	s := grpc.NewServer()
	pbacl.RegisterACLServiceServer(s, acl)
	pbdataplane.RegisterDataplaneServiceServer(s, dp)
	go func() { _ = s.Serve(lis) }()
	t.Cleanup(s.Stop)

	tcp := lis.Addr().(*net.TCPAddr)
	addr, err := MakeAddr(tcp.IP.String(), tcp.Port)
	require.NoError(t, err)

	cfg := loginConfig()
	cfg.MinReloginInterval = time.Hour
	w := newReloginWatcher(t, cfg)
	require.NoError(t, w.switchServer(addr))
	w.acls = newACLs(w.conn, w.config)

	// First attempt is allowed and fails.
	require.False(t, w.relogin())
	require.EqualValues(t, 1, acl.logins.Load())

	// Every subsequent attempt inside the interval must be suppressed, even
	// though no login has ever succeeded.
	for i := 0; i < 5; i++ {
		require.False(t, w.relogin())
	}
	require.EqualValues(t, 1, acl.logins.Load(), "failed re-logins must be rate limited too")
}

// TestReloginKeepsTokenWhenLoginFails guards the second review finding: the
// old token must not be blanked while a replacement is being fetched, because
// interceptContext reads w.token on every outbound request.
func TestReloginKeepsTokenWhenLoginFails(t *testing.T) {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	s := grpc.NewServer()
	pbacl.RegisterACLServiceServer(s, &failingACLServer{})
	pbdataplane.RegisterDataplaneServiceServer(s, &reloginDataplaneServer{})
	go func() { _ = s.Serve(lis) }()
	t.Cleanup(s.Stop)

	tcp := lis.Addr().(*net.TCPAddr)
	addr, err := MakeAddr(tcp.IP.String(), tcp.Port)
	require.NoError(t, err)

	w := newReloginWatcher(t, loginConfig())
	require.NoError(t, w.switchServer(addr))
	w.acls = newACLs(w.conn, w.config)
	w.token.Store("existing-token")

	require.False(t, w.relogin())
	require.Equal(t, "existing-token", w.token.Load().(string),
		"a failed re-login must not leave the Watcher with an empty token")
}

// TestReloginNotifiesSubscribers checks the easiest part of this fix to omit:
// consumers cache the token from Subscribe/State, so they must be told when it
// is replaced or they keep presenting the deleted one.
func TestReloginNotifiesSubscribers(t *testing.T) {
	dp := &reloginDataplaneServer{unauthUpTo: 1}
	addr, _ := startReloginServer(t, dp)

	w := newReloginWatcher(t, loginConfig())
	sub := w.Subscribe()

	_, err := w.connect(addr)
	require.NoError(t, err)

	select {
	case state := <-sub:
		require.Equal(t, "accessor-second-secret", state.Token,
			"subscribers must receive the token obtained by the re-login")
	case <-time.After(5 * time.Second):
		t.Fatal("subscriber was never notified of the new token")
	}
}

// TestInterceptorOnlyReactsToUnauthenticated documents the blast radius of the
// interceptor hook. interceptError runs for every gRPC error on the
// connection, so the token must be left alone for every code except
// Unauthenticated.
func TestInterceptorOnlyReactsToUnauthenticated(t *testing.T) {
	dp := &reloginDataplaneServer{}
	addr, acl := startReloginServer(t, dp)

	w := newReloginWatcher(t, loginConfig())
	require.NoError(t, w.switchServer(addr))
	w.acls = newACLs(w.conn, w.config)
	w.token.Store("current-token")

	for _, code := range []codes.Code{
		codes.Unavailable,
		codes.DeadlineExceeded,
		codes.PermissionDenied,
		codes.ResourceExhausted,
		codes.Canceled,
		codes.Internal,
		codes.NotFound,
	} {
		interceptError(w, status.Error(code, "boom"))
	}

	require.EqualValues(t, 0, acl.logins.Load(),
		"no error code other than Unauthenticated may trigger a re-login")
	require.Equal(t, "current-token", w.token.Load().(string), "token must be untouched")

	// Unauthenticated, by contrast, does trigger exactly one re-login.
	interceptError(w, status.Error(codes.Unauthenticated, "ACL not found"))
	require.EqualValues(t, 1, acl.logins.Load())
	require.Equal(t, "accessor-first-secret", w.token.Load().(string))
}

// TestInterceptorSkipsReloginWithoutToken covers the case where the request
// carried no token at all. Unauthenticated then means the login itself was
// refused, not that a token was deleted, so re-logging in would just double
// the login attempts against an auth method that is already rejecting us.
func TestInterceptorSkipsReloginWithoutToken(t *testing.T) {
	dp := &reloginDataplaneServer{}
	addr, acl := startReloginServer(t, dp)

	w := newReloginWatcher(t, loginConfig())
	require.NoError(t, w.switchServer(addr))
	w.acls = newACLs(w.conn, w.config)
	w.token.Store("") // nothing was presented

	interceptError(w, status.Error(codes.Unauthenticated, "ACL not found"))

	require.EqualValues(t, 0, acl.logins.Load(),
		"a request that carried no token must not trigger a re-login")
}

// TestACLsResetAllowsLoginAgain covers the new Reset method directly: without
// it, Login refuses forever with ErrAlreadyLoggedIn.
func TestACLsResetAllowsLoginAgain(t *testing.T) {
	dp := &reloginDataplaneServer{}
	addr, _ := startReloginServer(t, dp)

	w := newReloginWatcher(t, loginConfig())
	require.NoError(t, w.switchServer(addr))
	acls := newACLs(w.conn, w.config)

	_, secret1, err := acls.Login(w.ctx)
	require.NoError(t, err)
	require.NotEmpty(t, secret1)

	_, _, err = acls.Login(w.ctx)
	require.ErrorIs(t, err, ErrAlreadyLoggedIn, "second login must be refused while cached")

	acls.Reset()

	_, secret2, err := acls.Login(w.ctx)
	require.NoError(t, err)
	require.NotEqual(t, secret1, secret2, "Reset must allow a genuinely new token")
}
