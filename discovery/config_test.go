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
		"infer tls server name when address is ip": {
			cfg: Config{
				Addresses: "1.2.3.4",
				TLS:       &tls.Config{},
			},
			expCfg: Config{
				Addresses:                   "1.2.3.4",
				ServerWatchDisabledInterval: DefaultServerWatchDisabledInterval,
				TLS: &tls.Config{
					ServerName: "1.2.3.4",
				},
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

func TestSupportsDataplaneFeatures(t *testing.T) {
	matchNone := SupportsDataplaneFeatures()
	match1And2 := SupportsDataplaneFeatures("feat1", "feat2")

	// No supported features.
	{
		state := State{}
		require.True(t, matchNone(state))
		require.False(t, match1And2(state))
	}

	// feat2 not supported.
	{
		state := State{
			DataplaneFeatures: map[string]bool{
				"feat1": true,
				"feat2": false,
			},
		}
		require.True(t, matchNone(state))
		require.False(t, match1And2(state))
	}

	// Both feat1 and feat2 supported.
	{
		state := State{
			DataplaneFeatures: map[string]bool{
				"feat1": true,
				"feat2": true,
			},
		}
		require.True(t, matchNone(state))
		require.True(t, match1And2(state))
	}
}
