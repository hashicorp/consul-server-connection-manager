package discovery

import (
	"fmt"
	// "sync"

	"github.com/hashicorp/go-hclog"
	"google.golang.org/grpc/balancer"
	"google.golang.org/grpc/balancer/base"
	// "google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/resolver"
)

// TODO: Is a PickerBuilder implementation only needed for the argument to
// base.NewBalancerBuilder?
type pickerBuilder struct {
	log hclog.Logger
}

// This implements a custom gRPC PickerBuilder and Picker.
type picker struct {
	log hclog.Logger

	// the singular SubConn used to construct this picker, which will always be picked
	result balancer.PickResult

	// the address used to create the result SubConn
	address resolver.Address

	err error
}

// Ensure our pickerBuilderPicker implements the gRPC PickerBuilder and Picker interfaces
var _ base.PickerBuilder = (*pickerBuilder)(nil)
var _ balancer.Picker = (*picker)(nil)

// Build implements base.PickerBuilder
//
// This is called by the base Balancer when the set of ready sub-connections
// has changed.
func (pb *pickerBuilder) Build(info base.PickerBuildInfo) balancer.Picker {
	pb.log.Trace("pickerBuilder.Build", "sub-conns ready", len(info.ReadySCs))

	// p.lock.Lock()
	// defer p.lock.Unlock()

	for sc, info := range info.ReadySCs {
		// Return a picker which always selects first ready SubConn
		return &picker{
			log:     pb.log,
			result:  balancer.PickResult{SubConn: sc},
			address: info.Address,
		}

		// TODO: only SubConns in "READY" state should ever be passed into the
		// PickerBuilder Build call, and this impl is trying to ensure only a single
		// SubConn is ever passed, did updating this state serve some other purpose?
		//
		// This should be duplicative now since we already implement this between
		// clientConnWrapper.NewSubConn, clientConnWrapper.UpdateAddresses and
		// watcherBalancer.UpdateSubConnState?
		//
		// 	_, ok := p.scs[sc]
		// 	if !ok {
		// 		p.scs[sc] = &subConnState{}
		// 	}
		//
		// 	p.scs[sc].addr = info.Address
		// 	p.scs[sc].state.ConnectivityState = connectivity.Ready
	}

	// If no ready SubConns are passed to Build, return a picker which will always have
	// an error state
	return &picker{
		log:    pb.log,
		result: balancer.PickResult{},
		err:    fmt.Errorf("no ready sub-connections"),
	}
}

// Pick implements balancer.Picker
//
// This is called prior to each gRPC message to choose the sub-conn to use.
// This always picks the ready sub-connection from which the picker was created.
func (p *picker) Pick(balancer.PickInfo) (balancer.PickResult, error) {
	p.log.Trace("picker.Pick", p.address, p.err)

	return p.result, p.err
}

// Pick implements balancer.Picker
//
// This is called prior to each gRPC message to choose the sub-conn to use.
// This picks any ready sub-connection.
// func (p *pickerBuilderPicker) Pick(info balancer.PickInfo) (balancer.PickResult, error) {
// 	p.lock.Lock()
// 	defer p.lock.Unlock()

// 	// Pick any READY address.
// 	for sc, state := range p.scs {
// 		if state.state.ConnectivityState == connectivity.Ready {
// 			return balancer.PickResult{SubConn: sc}, nil
// 		}
// 	}
// 	return balancer.PickResult{}, fmt.Errorf("no ready sub-connections")
// }
