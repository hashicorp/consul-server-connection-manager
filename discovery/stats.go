package discovery

import "github.com/armon/go-metrics/prometheus"

var Summaries = []prometheus.SummaryDefinition{
	{
		Name: []string{"connect_duration"},
		Help: "This will be a sample of the time it takes to get connected to a server. This duration will cover everything from making the server features request all the way through to opening an xDS session with a server",
	},
	{
		Name: []string{"discover_servers_duration"},
		Help: "This will be a sample of the time it takes to discover Consul server IPs.",
	},
	{
		Name: []string{"login_duration"},
		Help: "This will be a sample of the time it takes to login to Consul.",
	},
}

var Gauges = []prometheus.GaugeDefinition{
	{
		Name: []string{"consul_connected"},
		Help: "This will either be 0 or 1 depending on whether the dataplane is currently connected to a Consul server.",
	},
	{
		Name: []string{"connection_errors"},
		Help: "This will track the number of errors encountered during the stream connection",
	},
}
