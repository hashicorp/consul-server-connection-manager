// Copyright IBM Corp. 2022, 2025
// SPDX-License-Identifier: MPL-2.0

package discovery

import (
	"fmt"

	"github.com/hashicorp/go-hclog"
	"google.golang.org/grpc/resolver"
)

type watcherResolver struct {
	cc  resolver.ClientConn
	log hclog.Logger
}

var _ resolver.Builder = (*watcherResolver)(nil)
var _ resolver.Resolver = (*watcherResolver)(nil)

func newResolver(log hclog.Logger) *watcherResolver {
	return &watcherResolver{log: log}
}

// Build implements resolver.Builder
//
// This is called by gRPC synchronously when we grpc.Dial.
func (r *watcherResolver) Build(target resolver.Target, cc resolver.ClientConn, opts resolver.BuildOptions) (resolver.Resolver, error) {
	if r.cc != nil {
		return nil, fmt.Errorf("watcher does not support redialing")
	}
	r.cc = cc
	return r, nil
}

// Scheme implements resolver.Builder
//
// This enables us to dial grpc.Dial("consul://") when using this resolver.
func (r *watcherResolver) Scheme() string {
	return "consul"
}

// SetAddress updates the connection to use the given address.
// After this is called, the connection will eventually complete a graceful
// switchover to the new address.
func (r *watcherResolver) SetAddress(addr Addr) error {
	if r.cc == nil {
		// We shouldn't run into this, as long as we Dial prior to calling SetAddress.
		return fmt.Errorf("resolver missing ClientConn")
	}

	var addrs []resolver.Address
	if !addr.Empty() {
		addrs = append(addrs, resolver.Address{Addr: addr.String()})
	}
	// In case we connect to a server, and then all servers go away,
	// support updating this to an empty list of addresses.
	err := r.cc.UpdateState(resolver.State{Addresses: addrs})
	if err != nil {
		r.log.Debug("gRPC resolver failed to update connection address", "error", err)
		return err
	}
	return nil

}

// Close implements resolver.Resolver
func (r *watcherResolver) Close() {}

// ResolveNow implements resolver.Resolver
//
// "ResolveNow will be called by gRPC to try to resolve the target name
// again. It's just a hint, resolver can ignore this if it's not necessary.
// It could be called multiple times concurrently."
func (r *watcherResolver) ResolveNow(_ resolver.ResolveNowOptions) {}
