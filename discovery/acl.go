package discovery

import (
	"context"

	"github.com/hashicorp/consul/proto-public/pbacl"
	"github.com/hashicorp/go-hclog"
)

type ACLs interface {
	Login(context.Context) (string, error)
	Logout(context.Context) error
}

type defaultACLs struct {
	watcher *Watcher
	log     hclog.Logger
}

var _ ACLs = (*defaultACLs)(nil)

func newDefaultACLs(watcher *Watcher, log hclog.Logger) *defaultACLs {
	return &defaultACLs{watcher: watcher, log: log}
}

func (a *defaultACLs) Login(ctx context.Context) (string, error) {
	if a.watcher.config.Credentials.Type != CredentialsTypeLogin {
		return "", nil
	}

	cfg := a.watcher.config.Credentials.Login

	client := pbacl.NewACLServiceClient(a.watcher.conn)
	resp, err := client.Login(ctx, &pbacl.LoginRequest{
		AuthMethod:  cfg.AuthMethod,
		BearerToken: cfg.BearerToken,
		Meta:        cfg.Meta,
		Namespace:   cfg.Namespace,
		Partition:   cfg.Partition,
		Datacenter:  cfg.Datacenter,
	})
	if err != nil {
		a.log.Error("auth method login failed", "error", err)
		return "", err
	}

	tok := resp.GetToken()
	a.log.Info("auth method login successful", "accessor-id", tok.AccessorId)
	// TODO: do the acl token replication workaround here?
	//       https://github.com/hashicorp/consul-k8s/pull/887
	return tok.SecretId, nil
}

func (a *defaultACLs) Logout(ctx context.Context) error {
	if a.watcher.config.Credentials.Type != CredentialsTypeLogin {
		return nil
	}
	token := a.watcher.token.Load().(string)
	if token == "" {
		return nil
	}

	client := pbacl.NewACLServiceClient(a.watcher.conn)
	_, err := client.Logout(ctx, &pbacl.LogoutRequest{
		Token:      token,
		Datacenter: "",
	})
	return err
}
