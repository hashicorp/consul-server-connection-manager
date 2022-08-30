package discovery

import (
	"testing"

	"github.com/hashicorp/go-hclog"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/resolver"
	"google.golang.org/grpc/serviceconfig"
)

func TestResolver(t *testing.T) {
	cc := &fakeClientConn{}

	rs := newResolver(hclog.NewNullLogger())

	rs2, err := rs.Build(resolver.Target{}, cc, resolver.BuildOptions{})
	require.NoError(t, err)

	// builder and resolver are the same instance
	require.Equal(t, rs, rs2)
	require.Equal(t, cc, rs.cc)

	addr, err := MakeAddr("127.0.0.1", 1234)
	require.NoError(t, err)

	// check that SetAddress passes addresses into the resolver's ClientConn
	err = rs.SetAddress(addr)
	require.NoError(t, err)
	require.Equal(t, []resolver.Address{{Addr: "127.0.0.1:1234"}}, cc.state.Addresses)

	addr.Port = 2345
	err = rs.SetAddress(addr)
	require.NoError(t, err)
	require.Equal(t, []resolver.Address{{Addr: "127.0.0.1:2345"}}, cc.state.Addresses)

	err = rs.SetAddress(Addr{})
	require.NoError(t, err)
	require.Len(t, cc.state.Addresses, 0)
}

// fakeClientConn implements resolver.ClientConn for tests
type fakeClientConn struct {
	state resolver.State
}

var _ resolver.ClientConn = (*fakeClientConn)(nil)

func (f *fakeClientConn) UpdateState(state resolver.State) error {
	f.state = state
	return nil
}

func (*fakeClientConn) ReportError(error)                       {}
func (*fakeClientConn) NewAddress(addresses []resolver.Address) {}
func (*fakeClientConn) NewServiceConfig(serviceConfig string)   {}
func (*fakeClientConn) ParseServiceConfig(serviceConfigJSON string) *serviceconfig.ParseResult {
	return nil
}
