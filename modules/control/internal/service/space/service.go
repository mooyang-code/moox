package space

import (
	"context"
	"fmt"

	"github.com/mooyang-code/moox/modules/control/internal/service/database"
)

// PageReq 是管理台列表页通用分页请求。
type PageReq struct {
	Page int `json:"page"`
	Size int `json:"size"`
}

// PageResult 是管理台列表页通用分页结果。
type PageResult struct {
	Page    int   `json:"page"`
	Size    int   `json:"size"`
	Total   int64 `json:"total"`
	HasMore bool  `json:"has_more"`
}

// Service 提供 Control/Admin Space 的业务能力。
type Service interface {
	CreateSpace(ctx context.Context, item *Space) (*Space, error)
	UpdateSpace(ctx context.Context, item *Space) (*Space, error)
	ListSpaces(ctx context.Context, owner string, status string, page PageReq) ([]Space, PageResult, error)
	ListSpaceMembers(ctx context.Context, spaceID string, page PageReq) ([]SpaceMember, PageResult, error)
}

type service struct {
	dao *DAO
}

// NewService 创建 Space 服务。
func NewService(dbManager *database.Manager) Service {
	return &service{dao: NewDAO(dbManager.GetDB())}
}

func normalizePage(page PageReq) (PageReq, int, int) {
	if page.Page <= 0 {
		page.Page = 1
	}
	if page.Size <= 0 || page.Size > 200 {
		page.Size = 20
	}
	offset := (page.Page - 1) * page.Size
	return page, offset, page.Size
}

func makePageResult(page PageReq, total int64) PageResult {
	return PageResult{Page: page.Page, Size: page.Size, Total: total, HasMore: int64(page.Page*page.Size) < total}
}

func (s *service) CreateSpace(ctx context.Context, item *Space) (*Space, error) {
	if item == nil || item.SpaceID == "" || item.Name == "" {
		return nil, fmt.Errorf("space_id and name are required")
	}
	if err := s.dao.CreateSpace(ctx, item); err != nil {
		return nil, err
	}
	return item, nil
}

func (s *service) UpdateSpace(ctx context.Context, item *Space) (*Space, error) {
	if item == nil || item.SpaceID == "" {
		return nil, fmt.Errorf("space_id is required")
	}
	if err := s.dao.UpdateSpace(ctx, item); err != nil {
		return nil, err
	}
	return item, nil
}

func (s *service) ListSpaces(ctx context.Context, owner string, status string, page PageReq) ([]Space, PageResult, error) {
	page, offset, limit := normalizePage(page)
	rows, total, err := s.dao.ListSpaces(ctx, owner, status, offset, limit)
	return rows, makePageResult(page, total), err
}

func (s *service) ListSpaceMembers(ctx context.Context, spaceID string, page PageReq) ([]SpaceMember, PageResult, error) {
	if spaceID == "" {
		return nil, PageResult{}, fmt.Errorf("space_id is required")
	}
	page, offset, limit := normalizePage(page)
	rows, total, err := s.dao.ListSpaceMembers(ctx, spaceID, offset, limit)
	return rows, makePageResult(page, total), err
}
