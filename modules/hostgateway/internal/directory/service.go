// Package directory 在本机回环端口上提供目录：登记的主机与部署，不含公钥与调用名单。
package directory

import (
	"context"
	"errors"

	"github.com/mooyang-code/moox/modules/hostgateway/internal/snapshot"
	pb "github.com/mooyang-code/moox/packages/gatewayclient/directorypb"
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
				// 目录就是快照里的主机与部署，版本用快照哈希；调用方按内嵌的服务目录自己展开。
				snapshot := view.Proto()
				rsp := &pb.GetDirectoryRsp{Version: snapshot.Hash, Changed: req.CurrentVersion != snapshot.Hash}
				if rsp.Changed {
					for _, host := range snapshot.Hosts {
						rsp.Hosts = append(rsp.Hosts, &pb.DirectoryHost{HostId: host.HostId, Address: host.Address, PrivateAddress: host.PrivateAddress, Reachability: host.Reachability, Enabled: host.Enabled})
					}
					for _, deployment := range snapshot.Deployments {
						rsp.Deployments = append(rsp.Deployments, &pb.DirectoryDeployment{HostId: deployment.HostId, ComponentId: deployment.ComponentId, Enabled: deployment.Enabled})
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
