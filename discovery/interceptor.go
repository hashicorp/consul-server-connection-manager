package discovery

import (
	"context"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

const tokenField = "x-consul-token"

func makeUnaryInterceptor(watcher *Watcher) grpc.UnaryClientInterceptor {
	return func(
		ctx context.Context,
		method string,
		req, reply interface{},
		cc *grpc.ClientConn,
		invoker grpc.UnaryInvoker,
		opts ...grpc.CallOption,
	) error {
		ctx = interceptContext(watcher, ctx)
		err := invoker(ctx, method, req, reply, cc, opts...)
		interceptError(watcher, err)
		return err
	}
}

func makeStreamInterceptor(watcher *Watcher) grpc.StreamClientInterceptor {
	return func(
		ctx context.Context,
		desc *grpc.StreamDesc,
		cc *grpc.ClientConn,
		method string,
		streamer grpc.Streamer,
		opts ...grpc.CallOption,
	) (grpc.ClientStream, error) {
		ctx = interceptContext(watcher, ctx)
		stream, err := streamer(ctx, desc, cc, method, opts...)
		interceptError(watcher, err)
		if err != nil {
			return nil, err
		}
		return &streamWrapper{ClientStream: stream, watcher: watcher}, nil
	}
}

type streamWrapper struct {
	grpc.ClientStream
	watcher *Watcher
}

func (s *streamWrapper) SendMsg(m interface{}) error {
	err := s.ClientStream.SendMsg(m)
	interceptError(s.watcher, err)
	return err
}

func (s *streamWrapper) RecvMsg(m interface{}) error {
	err := s.ClientStream.RecvMsg(m)
	interceptError(s.watcher, err)
	return err
}

// interceptContext will automatically include the ACL token on the context.
func interceptContext(watcher *Watcher, ctx context.Context) context.Context {
	token, ok := watcher.token.Load().(string)
	if !ok || token == "" {
		// no token to add to context
		return ctx
	}

	// skip if there is already a token in the context
	if md, ok := metadata.FromOutgoingContext(ctx); ok {
		if tok := md.Get(tokenField); len(tok) > 0 {
			return ctx
		}
	}

	return metadata.AppendToOutgoingContext(ctx, tokenField, token)
}

// interceptError can automatically react to errors.
func interceptError(watcher *Watcher, err error) {
	if err == nil {
		return
	}

	s, ok := status.FromError(err)
	if !ok {
		return
	}

	if s.Code() == codes.ResourceExhausted {
		watcher.log.Debug("saw gRPC ResourceExhausted status code")
		watcher.requestServerSwitch()
	}
}
