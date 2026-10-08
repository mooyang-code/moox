// Package directory 实现主机网关本机入口上的 Directory 服务：把快照中的全局服务目录交给本机调用方。
// 只在 127.0.0.1:11002 上提供，不需要签名。
package directory

import (
	"context"

	"github.com/mooyang-code/moox/modules/hostgateway/internal/snapshot"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/mooyang-code/moox/packages/gatewayroute/proto/directorypb"
	"google.golang.org/protobuf/proto"
)

// Source 提供当前快照。
type Source interface {
	Load() *snapshot.Applied
}

// Service 实现 directorypb.DirectoryService。
type Service struct {
	hostID string
	source Source
}

// New 创建 Directory 服务。
func New(hostID string, source Source) *Service { return &Service{hostID: hostID, source: source} }

// GetDirectory 与调用方持有的版本相同时只返回 changed=false。
func (s *Service) GetDirectory(_ context.Context, req *directorypb.GetDirectoryReq) (*directorypb.GetDirectoryRsp, error) {
	applied := s.source.Load()
	if applied == nil || applied.Directory == nil {
		return &directorypb.GetDirectoryRsp{
			RetInfo: &commonpb.RetInfo{Code: commonpb.ErrorCode_INNER_ERR, Msg: "主机网关还没有拿到快照"}, HostId: s.hostID,
		}, nil
	}
	rsp := &directorypb.GetDirectoryRsp{RetInfo: &commonpb.RetInfo{Code: commonpb.ErrorCode_SUCCESS}, HostId: s.hostID}
	if req.GetCurrentVersion() == applied.Directory.GetVersion() {
		return rsp, nil
	}
	rsp.Changed = true
	rsp.Directory = proto.Clone(applied.Directory).(*directorypb.DirectorySnapshot)
	return rsp, nil
}
