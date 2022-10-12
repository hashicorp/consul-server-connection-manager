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
