package discovery

import (
	"context"
	"fmt"
	"testing"

	"github.com/hashicorp/consul-server-connection-manager/mocks"
	"github.com/hashicorp/consul/proto-public/pbacl"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestACLLoginLogout(t *testing.T) {
	cases := map[string]func(ACLMockFixture) ACLMockFixture{
		"login and logout success": func(a ACLMockFixture) ACLMockFixture {
			return a
		},
		"login error": func(a ACLMockFixture) ACLMockFixture {
			a.LoginErr = fmt.Errorf("mock login error")
			// no logout request is be made if login failed (no token)
			a.ExpLogoutRequest = nil
			return a
		},
		"logout error": func(a ACLMockFixture) ACLMockFixture {
			a.LogoutErr = fmt.Errorf("mock logout error")
			return a
		},
	}
	for name, fn := range cases {
		fn := fn
		t.Run(name, func(t *testing.T) {
			fixture := fn(makeACLMockFixture("test-secret"))
			acls := fixture.SetupClientMock(t)
			ctx := context.Background()

			// Test Login.
			tok, err := acls.Login(ctx)
			if fixture.LoginErr != nil {
				require.Equal(t, fixture.LoginErr, err)
				require.Equal(t, tok, "")
			} else {
				require.NoError(t, err)
				require.Equal(t, "test-secret", tok)
				require.NotNil(t, acls.token)
			}

			// Test Logout.
			err = acls.Logout(ctx)
			if fixture.LogoutErr != nil {
				require.Equal(t, fixture.LogoutErr, err)
			} else {
				require.NoError(t, err)
				require.Nil(t, acls.token)
			}
		})
	}
}

// This holds the many values for mocking the ACL Login / Logout gRPC
// requests.
type ACLMockFixture struct {
	Config LoginCredential

	// Expected args and mocked return values for Login(ctx, req) gRPC request.
	// If ExpLoginRequest is nil, a Login call is not expected.
	ExpLoginRequest *pbacl.LoginRequest
	LoginResponse   *pbacl.LoginResponse
	LoginErr        error

	// Expected args and mocked return values for Logout(ctx, req) gRPC request.
	// If ExpLogoutRequest is nil, a Logout call is not expected.
	ExpLogoutRequest *pbacl.LogoutRequest
	LogoutResponse   *pbacl.LogoutResponse
	LogoutErr        error
}

// makeACLMockFixture mocks a successful login/logout request. You can modify
// the returned value to create an unsuccessful request.
func makeACLMockFixture(token string) ACLMockFixture {
	return ACLMockFixture{
		Config: LoginCredential{
			AuthMethod:  "test-auth-method",
			BearerToken: "test-token",
			Namespace:   "test-ns",
			Partition:   "test-ptn",
			Datacenter:  "test-dc",
			Meta: map[string]string{
				"test": "meta",
			},
		},
		ExpLoginRequest: &pbacl.LoginRequest{
			AuthMethod:  "test-auth-method",
			BearerToken: "test-token",
			Meta: map[string]string{
				"test": "meta",
			},
			Namespace:  "test-ns",
			Partition:  "test-ptn",
			Datacenter: "test-dc",
		},
		LoginResponse: &pbacl.LoginResponse{
			Token: &pbacl.LoginToken{
				AccessorId: "test-accessor",
				SecretId:   token,
			},
		},

		ExpLogoutRequest: &pbacl.LogoutRequest{
			Token:      token,
			Datacenter: "test-dc",
		},
		LogoutResponse: &pbacl.LogoutResponse{},
	}
}

func (a ACLMockFixture) SetupClientMock(t *testing.T) *ACLs {
	ctx := mock.Anything
	client := mocks.NewACLServiceClient(t)
	if a.ExpLoginRequest != nil {
		client.Mock.On("Login", ctx, a.ExpLoginRequest).Return(a.LoginResponse, a.LoginErr)
	}
	if a.ExpLogoutRequest != nil {
		client.Mock.On("Logout", ctx, a.ExpLogoutRequest).Return(a.LogoutResponse, a.LogoutErr)
	}
	return &ACLs{client: client, cfg: a.Config}
}
