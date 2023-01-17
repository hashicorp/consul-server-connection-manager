package discovery

import (
	// "crypto/rand"
	// "encoding/hex"
	"fmt"

	"github.com/hashicorp/go-hclog"
	"google.golang.org/grpc/balancer"
	"google.golang.org/grpc/balancer/base"
)

// init creates a new watcherBalancerBuilder and registers it with gRPC.
// Balancers must be globally registered and cannot be unregistered and registration is
// not threadsafe so must happen in an init() function.
func init() {
	balancer.Register(newWatcherBalancerBuilder())
}

// newWatcherBalancerBuilder can only safely be called in init() because
// base.NewBalancerBuilder is not threadsafe
func newWatcherBalancerBuilder() balancer.Builder {
	log := hclog.New(&hclog.LoggerOptions{
		Name: fmt.Sprintf("watcherBalancerBuilder"),

		// TODO: customize log level at init somehow?
		Level: hclog.Trace,
	})

	pb := &pickerBuilder{
		log: log,
	}

	wbb := watcherBalancerBuilder{log: log}
	wbb.baseBuilder = base.NewBalancerBuilder(wbb.Name(), pb, base.Config{})

	return &wbb
}

// watcherBalancerBuilder is invoked by gRPC to construct our custom balancer. This
// handles the hook up of the balancer to a Watcher.
type watcherBalancerBuilder struct {
	log hclog.Logger

	baseBuilder balancer.Builder
}

func (*watcherBalancerBuilder) Name() string {
	return "consul-server-connection-manager"
}

var _ balancer.Builder = (*watcherBalancerBuilder)(nil)

// Build constructs a balancer. This hooks up the balancer to the Watcher when it is created.
//
// Since we grpc.Dial one time in the Watcher, this is called only the first time a sub-connection
// needs to be created (so after the resolver first sets an address on the connection).
func (b *watcherBalancerBuilder) Build(cc balancer.ClientConn, opt balancer.BuildOptions) balancer.Balancer {
	b.log.Trace("balancerBuilder.Build")

	ccWrapper := clientConnWrapper{
		ClientConn: cc,
		log:        b.log,
	}

	blr := &watcherBalancer{
		cc:       ccWrapper,
		scs:      map[balancer.SubConn]*subConnState{},
		Balancer: b.baseBuilder.Build(&ccWrapper, opt),
	}

	blr.cc.balancer = blr

	return blr
}
