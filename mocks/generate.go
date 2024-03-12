package mocks

import "github.com/hashicorp/consul/proto-public/pbacl"

//go:generate mockery --srcpkg "github.com/hashicorp/consul/proto-public/pbacl" --name ACLServiceClient --output .

var _ pbacl.ACLServiceClient = &ACLServiceClient{}
