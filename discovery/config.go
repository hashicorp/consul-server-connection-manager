package discovery

import (
	"crypto/tls"
	"strings"
	"time"

	"github.com/cenkalti/backoff/v4"
)

type ServerEvalFn func(State) bool

type CredentialsType string

const (
	CredentialsTypeStatic CredentialsType = "static"
	CredentialsTypeLogin  CredentialsType = "login"
)

const (
	DefaultServerWatchDisabledInterval = 1 * time.Minute

	DefaultBackOffInitialInterval     = 500 * time.Millisecond
	DefaultBackOffMaxInterval         = 60 * time.Second
	DefaultBackOffMultiplier          = 1.5
	DefaultBackOffRandomizationFactor = 0.5
)

type Config struct {
	// Addresses is a DNS name or exec command for go-netaddrs.
	Addresses string

	// GRPCPort is the gRPC port to connect to. This must be the
	// same for all Consul servers for now. Defaults to 8502.
	GRPCPort int

	// ServerWatchDisabled disables opening the ServerWatch gRPC stream. This
	// should be used when your Consul servers are behind a load balancer, for
	// example, since the server addresses returned in the ServerWatch stream
	// will differ from the load balancer address.
	ServerWatchDisabled bool

	// ServerWatchDisabledInterval is the amount of time to sleep if
	// ServerWatchDisabled=true or when connecting to a server that does not
	// support the server watch stream. When the Watcher wakes up, it will
	// check that the current server is still OK and then continue sleeping. If
	// the current server is not OK, then it will switch to another server.
	//
	// This defaults to 1 minute.
	ServerWatchDisabledInterval time.Duration

	// ServerEvalFn is optional. It can be used to exclude servers based on
	// custom criteria. If not nil, it is called after connecting to a server
	// but prior to marking the server "current". When this returns false,
	// the Watcher will skip the server.
	//
	// The State passed to this function will be valid. The GRPCConn will be
	// valid to use and DataplaneFeatures will be populated and the Address
	// and Token (if applicable) will be set.
	//
	// This is called synchronously in the same goroutine as Watcher.Run(),
	// so it should not block, or at least not for too long.
	//
	// To filter dataplane features, you can use the SupportsDataplaneFeatures
	// helper, `cfg.ServerEvalFn = SupportsDataplaneFeatures("<feature-name>")`.
	ServerEvalFn ServerEvalFn

	// TLS contains the TLS settings to use for the gRPC connections to the
	// Consul servers. By default this is nil, indicating that TLS is disabled.
	//
	// If unset, the ServerName field is automatically set if Addresses
	// contains a DNS hostname. The ServerName field is only set if TLS and TLS
	// verification are enabled.
	TLS         *tls.Config
	Credentials Credentials

	BackOff BackOffConfig
}

func (c Config) withDefaults() Config {
	if c.ServerWatchDisabledInterval == 0 {
		c.ServerWatchDisabledInterval = DefaultServerWatchDisabledInterval
	}

	// Infer the ServerName field if a hostname is used in Addresses.
	if c.TLS != nil && !c.TLS.InsecureSkipVerify && c.TLS.ServerName == "" && !strings.HasPrefix(c.Addresses, "exec=") {
		c.TLS = c.TLS.Clone()
		c.TLS.ServerName = c.Addresses
	}

	c.BackOff = c.BackOff.withDefaults()

	return c
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
	// AuthMethod is the name of the Consul auth method.
	AuthMethod string
	// Namespace is the namespace containing the auth method.
	Namespace string
	// Partition is the partition containing the auth method.
	Partition string
	// Datacenter is the datacenter containing the auth method.
	Datacenter string
	// BearerToken is the bearer token presented to the auth method.
	BearerToken string
	// Meta is the arbitrary set of key-value pairs to attach to the
	// token. These are included in the Description field of the token.
	Meta map[string]string
}

type BackOffConfig struct {
	// InitialInterval is initial backoff retry interval for exponential backoff. Default: 500ms.
	InitialInterval time.Duration
	// Multiplier is the factor by which the backoff retry interval increases on each subquent
	// retry. Default: 1.5.
	Multiplier float64
	// MaxInterval is the maximum backoff interval for exponential backoff. Default: 60s.
	MaxInterval time.Duration
	// RandomizationFactor randomizes the backoff retry interval using the formula:
	//    RetryInterval * (random value in range [1-RandomizationFactor, 1+RandomizationFactor])
	// Default: 0.5.
	RandomizationFactor float64
}

func (b BackOffConfig) withDefaults() BackOffConfig {
	if b.InitialInterval == 0 {
		b.InitialInterval = DefaultBackOffInitialInterval
	}
	if b.Multiplier == 0 {
		b.Multiplier = DefaultBackOffMultiplier
	}
	if b.MaxInterval == 0 {
		b.MaxInterval = DefaultBackOffMaxInterval
	}
	if b.RandomizationFactor == 0 {
		b.RandomizationFactor = DefaultBackOffRandomizationFactor
	}
	return b
}

func (b BackOffConfig) getPolicy() backoff.BackOff {
	result := backoff.NewExponentialBackOff()
	result.InitialInterval = b.InitialInterval
	result.MaxInterval = b.MaxInterval
	result.Multiplier = b.Multiplier
	result.RandomizationFactor = b.RandomizationFactor
	// Backoff forever.
	result.MaxElapsedTime = 0
	return result
}

// SupportsDataplaneFeatures returns a ServerEvalFn that selects Consul servers
// that support a list of given dataplane features.
//
// The following are dataplane feature name strings:
//
//	"DATAPLANE_FEATURES_WATCH_SERVERS"
//	"DATAPLANE_FEATURES_EDGE_CERTIFICATE_MANAGEMENT"
//	"DATAPLANE_FEATURES_ENVOY_BOOTSTRAP_CONFIGURATION"
//
// See the hashicorp/consul/proto-public package for a up-to-date list.
func SupportsDataplaneFeatures(names ...string) ServerEvalFn {
	return func(s State) bool {
		for _, name := range names {
			if !s.DataplaneFeatures[name] {
				return false
			}
		}
		return true
	}
}
