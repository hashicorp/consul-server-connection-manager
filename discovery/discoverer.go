package discovery

import (
	"context"
	"net"

	"github.com/armon/go-metrics"
	"github.com/hashicorp/go-hclog"
	netaddrs "github.com/hashicorp/go-netaddrs"
)

type Discoverer interface {
	Discover(ctx context.Context) ([]Addr, error)
}

type NetaddrsDiscoverer struct {
	config Config
	log    hclog.Logger
	clock  Clock
}

func NewNetaddrsDiscoverer(config Config, log hclog.Logger) *NetaddrsDiscoverer {
	return &NetaddrsDiscoverer{
		config: config,
		log:    log,
		clock:  &SystemClock{},
	}
}

func (n *NetaddrsDiscoverer) Discover(ctx context.Context) ([]Addr, error) {
	start := n.clock.Now()
	addrs, err := netaddrs.IPAddrs(ctx, n.config.Addresses, n.log)
	if err != nil {
		n.log.Error("discovering server addresses", "err", err)
		return nil, err
	}

	metrics.MeasureSince([]string{"discover_servers_duration"}, start)

	var result []Addr
	for _, addr := range addrs {
		result = append(result, Addr{
			TCPAddr: net.TCPAddr{
				IP:   addr.IP,
				Port: n.config.GRPCPort,
				Zone: addr.Zone,
			},
		})
	}
	return result, nil
}
