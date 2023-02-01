// Copyright (c) HashiCorp, Inc.
// SPDX-License-Identifier: MPL-2.0

package discovery

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/metadata"
)

func TestInterceptContext(t *testing.T) {
	makeMD := func() metadata.MD {
		return metadata.MD{tokenField: []string{"test-token"}}
	}

	cases := map[string]struct {
		// configure the watcher with this token
		watcherToken string
		// call interceptContext with this context
		context context.Context
		// expect a context back with this metadata
		expMD metadata.MD
	}{
		"no watcher token": {
			context: metadata.NewOutgoingContext(context.Background(), metadata.MD{}),
			expMD:   metadata.MD{},
		},
		"watcher has token": {
			watcherToken: "test-token",
			context:      metadata.NewOutgoingContext(context.Background(), metadata.MD{}),
			expMD:        makeMD(),
		},
		"context already has token": {
			watcherToken: "other-token",
			context:      metadata.NewOutgoingContext(context.Background(), makeMD()),
			expMD:        makeMD(),
		},
	}
	for name, c := range cases {
		c := c
		t.Run(name, func(t *testing.T) {
			watcher := &Watcher{}
			watcher.token.Store(c.watcherToken)

			ctx := interceptContext(watcher, c.context)
			md, ok := metadata.FromOutgoingContext(ctx)
			require.True(t, ok)
			require.Equal(t, c.expMD, md)
		})
	}
}
