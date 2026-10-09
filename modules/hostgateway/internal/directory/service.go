// Package directory serves public service-discovery metadata on loopback only.
package directory

import (
	"context"
	"errors"

	"github.com/mooyang-code/moox/modules/hostgateway/internal/snapshot"
	pb "github.com/mooyang-code/moox/packages/gatewayroute/proto/gatewayroutegen"
	"trpc.group/trpc-go/trpc-go/codec"
	"trpc.group/trpc-go/trpc-go/server"
)

const Path = "trpc.moox.hostgateway.Directory"

func Register(local server.Service, state *snapshot.State) error {
	if local == nil || state == nil {
		return errors.New("directory requires local listener and snapshot state")
	}
	desc := &server.ServiceDesc{ServiceName: Path, HandlerType: ((*interface{})(nil)), Methods: []server.Method{{
		Name: "/" + Path + "/GetDirectory",
		Func: func(_ interface{}, ctx context.Context, decode server.FilterFunc) (interface{}, error) {
			raw := &codec.Body{}
			filters, err := decode(raw)
			if err != nil {
				return nil, err
			}
			return filters.Filter(ctx, raw, func(ctx context.Context, input interface{}) (interface{}, error) {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
				body := input.(*codec.Body)
				serialization := codec.Message(ctx).SerializationType()
				if len(body.Data) > 1024 || serialization != codec.SerializationTypePB && serialization != codec.SerializationTypeJSON {
					return nil, errors.New("invalid local directory request")
				}
				req := &pb.GetDirectoryReq{}
				if err := codec.Unmarshal(serialization, body.Data, req); err != nil || len(req.CurrentVersion) > 64 {
					return nil, errors.New("invalid local directory version")
				}
				view := state.Load()
				if view == nil {
					return nil, errors.New("local directory is not initialized")
				}
				d := view.Directory()
				rsp := &pb.GetDirectoryRsp{Version: d.Version, Changed: req.CurrentVersion != d.Version}
				if rsp.Changed {
					rsp.Hosts, rsp.Services = map[string]*pb.DirectoryHost{}, map[string]*pb.ServiceHosts{}
					for id, host := range d.Hosts {
						rsp.Hosts[id] = &pb.DirectoryHost{Address: host.Address, PrivateAddress: host.PrivateAddress, Region: host.Region}
					}
					for path, hosts := range d.Services {
						rsp.Services[path] = &pb.ServiceHosts{HostIds: hosts}
					}
				}
				encoded, err := codec.Marshal(serialization, rsp)
				if err != nil {
					return nil, errors.New("encode directory response")
				}
				return &codec.Body{Data: encoded}, nil
			})
		},
	}}}
	return local.Register(desc, struct{}{})
}
