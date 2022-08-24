package discovery

import "crypto/tls"

type CredentialsType string

const (
	CredentialsTypeStatic CredentialsType = "static"
	CredentialsTypeLogin  CredentialsType = "login"
)

type Config struct {
	// Addresses is a DNS name or exec command for go-netaddrs.
	Addresses string
	// GRPCPort is the gRPC port to connect to. This must be the
	// same for all Consul servers for now. Defaults to 8502.
	GRPCPort int

	// ServerWatchDisabled disables opening the ServerWatch gRPC
	// stream. This should be used when your Consul servers are
	// behind a load balancer, for example, since the server addresses
	// returned in the ServerWatch stream will differ from the load
	// balancer address.
	ServerWatchDisabled bool

	TLS         *tls.Config
	Credentials Credentials
}

type Credentials struct {
	// Type is either "static" for a statically-configured ACL
	// token, or "login" to obtain an ACL token by logging into a
	// Consul auth method.
	Type CredentialsType

	// Static is used if Type is "static".
	Static StaticTokenCredential

	// Login is used if Type is "login".
	Login LoginCredential
}

type StaticTokenCredential struct {
	// Token is a static ACL token used for gRPC requests to the
	// Consul servers.
	Token string
}

type LoginCredential struct {
	// Method is the name of the Consul auth method.
	Method string
	// Namespace is the namespace containing the auth method.
	Namespace string
	// Partition is the partition containing the auth method.
	Partition string
	// Datacenter is the datacenter containing the auth method.
	Datacenter string
	// Bearer is the bearer token presented to the auth method.
	Bearer string
	// Meta is the arbitrary set of key-value pairs to attach to the
	// token. These are included in the Description field of the token.
	Meta map[string]string
}
