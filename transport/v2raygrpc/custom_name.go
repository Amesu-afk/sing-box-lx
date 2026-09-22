package v2raygrpc

import (
	"context"

	"github.com/sagernet/sing-box/common/grpcname"

	"google.golang.org/grpc"
)

type GunService interface {
	Context() context.Context
	Send(*Hunk) error
	Recv() (*Hunk, error)
}

func ServerDesc(name string) grpc.ServiceDesc {
	// lx: SPEC 093 — a leading "/" in service_name is Xray's custom-path form:
	// the service and stream names come from the path, in escaped form (Xray
	// feeds getServiceName's result to the descriptor the same way). Without a
	// leading "/" the descriptor is byte for byte what it was.
	serviceName, streamName := name, "Tun"
	if grpcname.IsCustom(name) {
		serviceName, streamName = grpcname.Split(name)
	}
	return grpc.ServiceDesc{
		ServiceName: serviceName,
		HandlerType: (*GunServiceServer)(nil),
		Methods:     []grpc.MethodDesc{},
		Streams: []grpc.StreamDesc{
			{
				StreamName:    streamName,
				Handler:       _GunService_Tun_Handler,
				ServerStreams: true,
				ClientStreams: true,
			},
		},
		Metadata: "gun.proto",
	}
}

func (c *gunServiceClient) TunCustomName(ctx context.Context, name string, opts ...grpc.CallOption) (GunService_TunClient, error) {
	// lx: SPEC 093 — custom-path form goes on the wire as Xray builds it; the
	// old form keeps the upstream method string (unescaped, as before).
	method := "/" + name + "/Tun"
	if grpcname.IsCustom(name) {
		method = grpcname.RawPath(name)
	}
	stream, err := c.cc.NewStream(ctx, &ServerDesc(name).Streams[0], method, opts...)
	if err != nil {
		return nil, err
	}
	x := &grpc.GenericClientStream[Hunk, Hunk]{ClientStream: stream}
	return x, nil
}

var _ GunServiceCustomNameClient = (*gunServiceClient)(nil)

type GunServiceCustomNameClient interface {
	TunCustomName(ctx context.Context, name string, opts ...grpc.CallOption) (GunService_TunClient, error)
	Tun(ctx context.Context, opts ...grpc.CallOption) (GunService_TunClient, error)
}

func RegisterGunServiceCustomNameServer(s *grpc.Server, srv GunServiceServer, name string) {
	desc := ServerDesc(name)
	s.RegisterService(&desc, srv)
}
