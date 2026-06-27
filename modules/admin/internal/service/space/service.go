package space

import (
	"context"
	"fmt"
	"time"

	"github.com/mooyang-code/moox/modules/admin/internal/service/database"
	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
)

// Service 提供 Control/Admin Space 的业务能力，实现 pb.SpaceMgrService。
// DAO 层保留 GORM model（Space/SpaceMember），service 内完成 model→PB 映射。
type Service interface {
	pb.SpaceMgrService
}

type service struct {
	dao *DAO
}

// NewService 创建 Space 服务。
func NewService(dbManager *database.Manager) Service {
	return &service{dao: NewDAO(dbManager.GetDB())}
}

func normalizePage(page *pb.Page) (int, int, int) {
	pageNo, size := int(page.GetPage()), int(page.GetSize())
	if pageNo <= 0 {
		pageNo = 1
	}
	if size <= 0 || size > 200 {
		size = 20
	}
	return pageNo, (pageNo - 1) * size, size
}

func makePageResult(pageNo, size int, total int64) *pb.PageResult {
	return &pb.PageResult{
		Page:    uint32(pageNo),
		Size:    uint32(size),
		Total:   uint32(total),
		HasMore: int64(pageNo*size) < total,
	}
}

func (s *service) CreateSpace(ctx context.Context, req *pb.CreateSpaceReq) (*pb.CreateSpaceRsp, error) {
	if req == nil || req.GetSpace() == nil {
		return nil, fmt.Errorf("space is required")
	}
	item := pbToModelSpace(req.GetSpace())
	if item.SpaceID == "" || item.Name == "" {
		return nil, fmt.Errorf("space_id and name are required")
	}
	if err := s.dao.CreateSpace(ctx, item); err != nil {
		return nil, err
	}
	return &pb.CreateSpaceRsp{
		RetInfo: &pb.RetInfo{Code: pb.ErrorCode_SUCCESS, Msg: "success"},
		Space:   modelToPBSpace(item),
	}, nil
}

func (s *service) UpdateSpace(ctx context.Context, req *pb.UpdateSpaceReq) (*pb.UpdateSpaceRsp, error) {
	if req == nil || req.GetSpace() == nil {
		return nil, fmt.Errorf("space is required")
	}
	item := pbToModelSpace(req.GetSpace())
	if item.SpaceID == "" {
		return nil, fmt.Errorf("space_id is required")
	}
	if err := s.dao.UpdateSpace(ctx, item); err != nil {
		return nil, err
	}
	return &pb.UpdateSpaceRsp{
		RetInfo: &pb.RetInfo{Code: pb.ErrorCode_SUCCESS, Msg: "success"},
		Space:   modelToPBSpace(item),
	}, nil
}

func (s *service) ListSpaces(ctx context.Context, req *pb.ListSpacesReq) (*pb.ListSpacesRsp, error) {
	pageNo, offset, limit := normalizePage(req.GetPage())
	rows, total, err := s.dao.ListSpaces(ctx, req.GetOwner(), req.GetStatus(), offset, limit)
	if err != nil {
		return nil, err
	}
	spaces := make([]*pb.Space, 0, len(rows))
	for i := range rows {
		spaces = append(spaces, modelToPBSpace(&rows[i]))
	}
	return &pb.ListSpacesRsp{
		RetInfo:     &pb.RetInfo{Code: pb.ErrorCode_SUCCESS, Msg: "success"},
		Spaces:      spaces,
		PageResult:  makePageResult(pageNo, limit, total),
	}, nil
}

func (s *service) ListSpaceMembers(ctx context.Context, req *pb.ListSpaceMembersReq) (*pb.ListSpaceMembersRsp, error) {
	if req.GetSpaceId() == "" {
		return nil, fmt.Errorf("space_id is required")
	}
	pageNo, offset, limit := normalizePage(req.GetPage())
	rows, total, err := s.dao.ListSpaceMembers(ctx, req.GetSpaceId(), offset, limit)
	if err != nil {
		return nil, err
	}
	members := make([]*pb.SpaceMember, 0, len(rows))
	for i := range rows {
		members = append(members, modelToPBSpaceMember(&rows[i]))
	}
	return &pb.ListSpaceMembersRsp{
		RetInfo:    &pb.RetInfo{Code: pb.ErrorCode_SUCCESS, Msg: "success"},
		Members:    members,
		PageResult: makePageResult(pageNo, limit, total),
	}, nil
}

// modelToPBSpace 把 GORM model 映射为 PB Space（时间用 RFC3339 字符串）。
func modelToPBSpace(m *Space) *pb.Space {
	if m == nil {
		return nil
	}
	return &pb.Space{
		Id:          m.ID,
		SpaceId:     m.SpaceID,
		Name:        m.Name,
		Description: m.Description,
		Owner:       m.Owner,
		Market:      m.Market,
		Timezone:    m.Timezone,
		Status:      m.Status,
		Attributes:  m.Attributes,
		CreatedAt:   formatTime(m.CreatedAt),
		UpdatedAt:   formatTime(m.UpdatedAt),
	}
}

// modelToPBSpaceMember 把 GORM model 映射为 PB SpaceMember。
func modelToPBSpaceMember(m *SpaceMember) *pb.SpaceMember {
	if m == nil {
		return nil
	}
	return &pb.SpaceMember{
		Id:         m.ID,
		SpaceId:    m.SpaceID,
		UserId:     m.UserID,
		Role:       m.Role,
		Status:     m.Status,
		Attributes: m.Attributes,
		CreatedAt:  formatTime(m.CreatedAt),
		UpdatedAt:  formatTime(m.UpdatedAt),
	}
}

// pbToModelSpace 把 PB Space 映射为 GORM model（仅取业务字段，时间由 DAO 维护）。
func pbToModelSpace(p *pb.Space) *Space {
	if p == nil {
		return nil
	}
	return &Space{
		ID:          p.GetId(),
		SpaceID:     p.GetSpaceId(),
		Name:        p.GetName(),
		Description: p.GetDescription(),
		Owner:       p.GetOwner(),
		Market:      p.GetMarket(),
		Timezone:    p.GetTimezone(),
		Status:      p.GetStatus(),
		Attributes:  p.GetAttributes(),
	}
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format(time.RFC3339)
}
