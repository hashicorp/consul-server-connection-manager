// Copyright (c) HashiCorp, Inc.
// SPDX-License-Identifier: MPL-2.0

package discovery

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/armon/go-metrics"
	"github.com/hashicorp/consul/proto-public/pbacl"
	"google.golang.org/grpc"
)

var (
	ErrAlreadyLoggedOut = errors.New("already logged out")
	ErrAlreadyLoggedIn  = errors.New("already logged in")
)

type ACLs struct {
	client pbacl.ACLServiceClient
	cfg    LoginCredential

	// remember this for logout.
	token *pbacl.LoginToken
	clock Clock
	mu    sync.Mutex
}

func newACLs(conn grpc.ClientConnInterface, config Config) *ACLs {
	return &ACLs{
		client: pbacl.NewACLServiceClient(conn),
		cfg:    config.Credentials.Login,
		clock:  &SystemClock{},
	}
}

func (a *ACLs) Login(ctx context.Context) (string, string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.token != nil {
		return "", "", ErrAlreadyLoggedIn
	}

	req := &pbacl.LoginRequest{
		AuthMethod:  a.cfg.AuthMethod,
		BearerToken: a.cfg.BearerToken,
		Meta:        a.cfg.Meta,
		Namespace:   a.cfg.Namespace,
		Partition:   a.cfg.Partition,
		Datacenter:  a.cfg.Datacenter,
	}
	start := a.clock.Now()
	resp, err := a.client.Login(ctx, req)
	if err != nil {
		return "", "", err
	}
	metrics.MeasureSince([]string{"login_duration"}, start)

	a.token = resp.GetToken()
	if a.token == nil || a.token.SecretId == "" {
		return "", "", fmt.Errorf("no secret id in response")
	}
	// TODO: We are prone to a negative caching problem that might cause "ACL not found" on
	// subsequent requests that use the token for the caching period (default 30s). See:
	// https://github.com/hashicorp/consul-k8s/pull/887
	//
	// A short sleep should mitigate some cases of the problem until we address this properly.
	a.clock.Sleep(100 * time.Millisecond)
	return a.token.AccessorId, a.token.SecretId, nil
}

func (a *ACLs) Logout(ctx context.Context) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.token == nil || a.token.SecretId == "" {
		return ErrAlreadyLoggedOut
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
