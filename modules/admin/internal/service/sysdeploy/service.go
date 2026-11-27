package sysdeploy

import (
	"strings"

	"github.com/mooyang-code/moox/modules/admin/internal/service/database"
	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
)

type Service interface {
	pb.SysDeployService
	SetSessionCloser(closer SessionCloser)
	TopologyDAO() *TopologyDAO
}

// SessionCloser 关闭某台主机上所有在线的终端与文件会话。
type SessionCloser interface{ CloseHostSessions(hostID string) }

type ServiceImpl struct {
	pb.UnimplementedSysDeploy
	topology    *TopologyDAO
	adminNodeID string
	sessions    SessionCloser
}

func NewService(dbManager *database.Manager, adminNodeID, encryptionKey string) (*ServiceImpl, error) {
	nodeID := strings.TrimSpace(adminNodeID)
	topology, err := NewTopologyDAO(dbManager.GetDB(), nodeID)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(encryptionKey) != "" {
		topology.WithEncryptionKey(encryptionKey)
	}
	return &ServiceImpl{topology: topology, adminNodeID: nodeID}, nil
}

// TopologyDAO 返回复用的长生命周期 TopologyDAO。
func (s *ServiceImpl) TopologyDAO() *TopologyDAO { return s.topology }

// SetSessionCloser 接入 SSH 服务：主机的连接信息变化或主机被删除后，关闭它在线的会话。
func (s *ServiceImpl) SetSessionCloser(closer SessionCloser) { s.sessions = closer }

func (s *ServiceImpl) closeHostSessions(hostID string) {
	if s.sessions != nil {
		s.sessions.CloseHostSessions(hostID)
	}
}

func makePageResult(pageNo, size int, total int64) *pb.PageResult {
	return &pb.PageResult{Page: uint32(pageNo), Size: uint32(size), Total: uint32(total), HasMore: int64(pageNo*size) < total}
}
func retOK() *pb.RetInfo                               { return &pb.RetInfo{Code: pb.ErrorCode_SUCCESS, Msg: "success"} }
func retErr(code pb.ErrorCode, msg string) *pb.RetInfo { return &pb.RetInfo{Code: code, Msg: msg} }
