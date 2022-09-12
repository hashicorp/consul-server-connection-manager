package discovery

import (
	"crypto/rand"
	"encoding/hex"

	"github.com/hashicorp/go-hclog"
	"google.golang.org/grpc/balancer"
	"google.golang.org/grpc/balancer/base"
)

// balancerBuilder is invoked by gRPC to construct our custom balancer. This
// handles the hook up of the balancer to a Watcher.
type balancerBuilder struct {
	watcher *Watcher
	log     hclog.Logger

	// This is the unique id for this builder in gRPC global balancer registry,
	// used as the loadBalancingPolicy name when configuring a gRPC connection.
	hackPolicyName string
}

var _ balancer.Builder = (*balancerBuilder)(nil)

// registerBalancer creates new balancer.Builder for the given Watcher and registers it with gRPC.
// Balancers must be globally registered and cannot be unregistered, so we register each builder
// with a unique id to facilitate testing. This returns the loadBalancingPolicy name used to enable
// this balancer on a gRPC connection.
func registerBalancer(w *Watcher, log hclog.Logger) string {
	b := &balancerBuilder{
		watcher:        w,
		log:            log,
		hackPolicyName: randomString(),
	}
	balancer.Register(b)
	return b.hackPolicyName
}

func (b *balancerBuilder) Name() string {
	return b.hackPolicyName
}

// Build constructs a balancer. This hooks up the balancer to the Watcher when it is created.
//
// Since we grpc.Dial one time in the Watcher, this is called only the first time a sub-connection
// needs to be created (so after the resolver first sets an address on the connection).
func (b *balancerBuilder) Build(cc balancer.ClientConn, opt balancer.BuildOptions) balancer.Balancer {
	b.log.Debug("balancerBuilder.Build")

	blr := &watcherBalancer{
		log: b.log,
		scs: map[balancer.SubConn]*subConnState{},
	}
	ccWrapper := &clientConnWrapper{ClientConn: cc, balancer: blr, log: b.log}

	baseBuilder := base.NewBalancerBuilder(b.hackPolicyName, blr, base.Config{})
	blr.Balancer = baseBuilder.Build(ccWrapper, opt)

	b.watcher.balancer = blr
	return b.watcher.balancer

}

// randomString returns a 32-byte hex-encoded string.
func randomString() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic("failed to generate random bytes")
	}
	return hex.EncodeToString(b)
}
