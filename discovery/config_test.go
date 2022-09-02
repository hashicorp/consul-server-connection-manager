package discovery

import (
	"crypto/tls"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestConfigDefaults(t *testing.T) {
	cases := map[string]struct {
		cfg, expCfg Config
	}{
		"default server watch disabled interval": {
			cfg: Config{},
			expCfg: Config{
				ServerWatchDisabledInterval: DefaultServerWatchDisabledInterval,
			},
		},
		"custom server watch disabled interval": {
			cfg: Config{
				ServerWatchDisabledInterval: 1234,
			},
			expCfg: Config{
				ServerWatchDisabledInterval: 1234,
			},
		},
		"infer tls server name": {
			cfg: Config{
				Addresses: "my.host.name",
				TLS:       &tls.Config{},
			},
			expCfg: Config{
				Addresses:                   "my.host.name",
				ServerWatchDisabledInterval: DefaultServerWatchDisabledInterval,
				TLS: &tls.Config{
					ServerName: "my.host.name",
				},
			},
		},
		"do not infer tls server name when address is ip": {
			cfg: Config{
				Addresses: "1.2.3.4",
				TLS:       &tls.Config{},
			},
			expCfg: Config{
				Addresses:                   "1.2.3.4",
				ServerWatchDisabledInterval: DefaultServerWatchDisabledInterval,
				TLS:                         &tls.Config{},
			},
		},
		"do not infer tls server name when address is exec command": {
			cfg: Config{
				Addresses: "exec=./script.sh",
				TLS:       &tls.Config{},
			},
			expCfg: Config{
				Addresses:                   "exec=./script.sh",
				ServerWatchDisabledInterval: DefaultServerWatchDisabledInterval,
				TLS:                         &tls.Config{},
			},
		},
		"do not infer tls server name when TLS is disabled": {
			cfg: Config{
				Addresses: "my.host.name",
				TLS:       nil,
			},
			expCfg: Config{
				Addresses:                   "my.host.name",
				ServerWatchDisabledInterval: DefaultServerWatchDisabledInterval,
			},
		},
		"do not infer tls server name when TLS verification is disabled": {
			cfg: Config{
				Addresses: "my.host.name",
				TLS:       &tls.Config{InsecureSkipVerify: true},
			},
			expCfg: Config{
				Addresses:                   "my.host.name",
				ServerWatchDisabledInterval: DefaultServerWatchDisabledInterval,
				TLS:                         &tls.Config{InsecureSkipVerify: true},
			},
		},
		"do not infer tls server name when already set": {
			cfg: Config{
				Addresses: "my.host.name",
				TLS:       &tls.Config{ServerName: "other.host"},
			},
			expCfg: Config{
				Addresses:                   "my.host.name",
				ServerWatchDisabledInterval: DefaultServerWatchDisabledInterval,
				TLS:                         &tls.Config{ServerName: "other.host"},
			},
		},
	}
	for name, c := range cases {
		c := c
		t.Run(name, func(t *testing.T) {
			require.Equal(t, c.expCfg, c.cfg.withDefaults())
		})
	}
}

func TestIsPotentialHostname(t *testing.T) {
	// non-hostnames
	for _, addr := range []string{
		"",
		"exec=",
		"exec=1.2.3.4",
		"1.2.3.4",
		"::1",
		"0:0:0:0:0:0:0:1",
	} {
		require.False(t, isPotentialHostname(addr), "addr=%q", addr)
	}

	// valid hostnames
	for _, addr := range []string{
		"exec", // no trailing '='. this could be a hostname.
		"a",
		"a.b",
		"abc.tld",
		"1.2.3.tld",
		"1.2.3.4.tld",
	} {
		require.True(t, isPotentialHostname(addr), "addr=%q", addr)
	}
}
