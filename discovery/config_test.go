package discovery

import (
	"crypto/tls"
	"testing"

	"github.com/cenkalti/backoff/v4"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/stretchr/testify/require"
)

func TestConfigDefaults(t *testing.T) {
	makeDefaultConfig := func() Config {
		return Config{
			ServerWatchDisabledInterval: DefaultServerWatchDisabledInterval,
			BackOff: BackOffConfig{
				InitialInterval:     DefaultBackOffInitialInterval,
				Multiplier:          DefaultBackOffMultiplier,
				MaxInterval:         DefaultBackOffMaxInterval,
				RandomizationFactor: DefaultBackOffRandomizationFactor,
			},
		}
	}

	cases := map[string]struct {
		cfg Config
		// this modifies the "base" default config from makeDefaults()
		expCfgFn func(Config) Config
	}{
		"default server watch disabled interval": {
			cfg:      Config{},
			expCfgFn: func(c Config) Config { return c },
		},
		"custom server watch disabled interval": {
			cfg: Config{
				ServerWatchDisabledInterval: 1234,
			},
			expCfgFn: func(c Config) Config {
				c.ServerWatchDisabledInterval = 1234
				return c
			},
		},
		"custom backoff config": {
			cfg: Config{
				BackOff: BackOffConfig{
					InitialInterval:     1,
					Multiplier:          2,
					MaxInterval:         3,
					RandomizationFactor: 4,
				},
			},
			expCfgFn: func(c Config) Config {
				c.BackOff.InitialInterval = 1
				c.BackOff.Multiplier = 2
				c.BackOff.MaxInterval = 3
				c.BackOff.RandomizationFactor = 4
				return c
			},
		},
		"infer tls server name": {
			cfg: Config{
				Addresses: "my.host.name",
				TLS:       &tls.Config{},
			},
			expCfgFn: func(c Config) Config {
				c.Addresses = "my.host.name"
				c.TLS = &tls.Config{ServerName: "my.host.name"}
				return c
			},
		},
		"infer tls server name when address is ip": {
			cfg: Config{
				Addresses: "1.2.3.4",
				TLS:       &tls.Config{},
			},
			expCfgFn: func(c Config) Config {
				c.Addresses = "1.2.3.4"
				c.TLS = &tls.Config{ServerName: "1.2.3.4"}
				return c
			},
		},
		"do not infer tls server name when address is exec command": {
			cfg: Config{
				Addresses: "exec=./script.sh",
				TLS:       &tls.Config{},
			},
			expCfgFn: func(c Config) Config {
				c.Addresses = "exec=./script.sh"
				c.TLS = &tls.Config{}
				return c
			},
		},
		"do not infer tls server name when TLS is disabled": {
			cfg: Config{
				Addresses: "my.host.name",
				TLS:       nil,
			},
			expCfgFn: func(c Config) Config {
				c.Addresses = "my.host.name"
				return c
			},
		},
		"do not infer tls server name when TLS verification is disabled": {
			cfg: Config{
				Addresses: "my.host.name",
				TLS:       &tls.Config{InsecureSkipVerify: true},
			},
			expCfgFn: func(c Config) Config {
				c.Addresses = "my.host.name"
				c.TLS = &tls.Config{InsecureSkipVerify: true}
				return c
			},
		},
		"do not infer tls server name when already set": {
			cfg: Config{
				Addresses: "my.host.name",
				TLS:       &tls.Config{ServerName: "other.host"},
			},
			expCfgFn: func(c Config) Config {
				c.Addresses = "my.host.name"
				c.TLS = &tls.Config{ServerName: "other.host"}
				return c
			},
		},
	}
	for name, c := range cases {
		c := c
		t.Run(name, func(t *testing.T) {
			expCfg := c.expCfgFn(makeDefaultConfig())
			require.Equal(t, expCfg, c.cfg.withDefaults())
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

func TestGetBackoffPolicy(t *testing.T) {
	cfg := BackOffConfig{}.withDefaults()
	bo := cfg.getPolicy()

	exp := &backoff.ExponentialBackOff{
		InitialInterval:     DefaultBackOffInitialInterval,
		RandomizationFactor: DefaultBackOffRandomizationFactor,
		Multiplier:          DefaultBackOffMultiplier,
		MaxInterval:         DefaultBackOffMaxInterval,
		MaxElapsedTime:      0,
		Stop:                backoff.Stop,
		Clock:               backoff.SystemClock,
	}
	require.Empty(t, cmp.Diff(bo, exp, cmpopts.IgnoreUnexported(*exp)))
}
