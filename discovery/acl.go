package discovery

import (
	"context"
	"fmt"
	"time"

	"github.com/hashicorp/consul/proto-public/pbacl"
	"google.golang.org/grpc"
)

type ACLs struct {
	client pbacl.ACLServiceClient
	cfg    LoginCredential

	// remember this for logout.
	token *pbacl.LoginToken
}

func newACLs(conn grpc.ClientConnInterface, config Config) *ACLs {
	return &ACLs{
		client: pbacl.NewACLServiceClient(conn),
		cfg:    config.Credentials.Login,
	}
}

func (a *ACLs) Login(ctx context.Context) (string, error) {
	if a.token != nil {
		// detect mis-use. we shouldn't call this twice.
		return "", fmt.Errorf("already logged in")
	}

	req := &pbacl.LoginRequest{
		AuthMethod:  a.cfg.AuthMethod,
		BearerToken: a.cfg.BearerToken,
		Meta:        a.cfg.Meta,
		Namespace:   a.cfg.Namespace,
		Partition:   a.cfg.Partition,
		Datacenter:  a.cfg.Datacenter,
	}

	resp, err := a.client.Login(ctx, req)
	if err != nil {
		return "", err
	}

	a.token = resp.GetToken()
	if a.token == nil || a.token.SecretId == "" {
		return "", fmt.Errorf("no secret id in response")
	}
	// TODO: We are prone to a negative caching problem that might cause "ACL not found" on
	// subsequent requests that use the token for the caching period (default 30s). See:
	// https://github.com/hashicorp/consul-k8s/pull/887
	//
	// A short sleep should mitigate some cases of the problem until we address this properly.
	time.Sleep(100 * time.Millisecond)
	return a.token.SecretId, nil
}

func (a *ACLs) Logout(ctx context.Context) error {
	if a.token == nil || a.token.SecretId == "" {
		// no token to use for logout.
		return nil
	}
	_, err := a.client.Logout(ctx, &pbacl.LogoutRequest{
		Token:      a.token.SecretId,
		Datacenter: a.cfg.Datacenter,
	})
	if err == nil {
		a.token = nil
	}
	return err
}
