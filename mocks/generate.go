// Copyright IBM Corp. 2022, 2025
// SPDX-License-Identifier: MPL-2.0

package mocks

import "github.com/hashicorp/consul/proto-public/v2/pbacl"

//go:generate mockery --srcpkg "github.com/hashicorp/consul/proto-public/v2/pbacl" --name ACLServiceClient --output .

var _ pbacl.ACLServiceClient = &ACLServiceClient{}
