package activitylog

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/admin/internal/service/auth/utils"
	"github.com/mooyang-code/moox/modules/admin/internal/service/space"
	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"gorm.io/gorm"
	"trpc.group/trpc-go/trpc-go/log"
)

// Service 实现全局活动日志的写入与查询。写入只允许内部组件（服务目录 ACL 限定），
// 查询时浏览器用户必须指定空间并且是该空间成员。
type Service struct {
	pb.UnimplementedActivityLog
	dao        *DAO
	authorizer space.Service
	now        func() time.Time
}

func NewService(db *gorm.DB, authorizer space.Service) *Service {
	return &Service{dao: NewDAO(db), authorizer: authorizer, now: time.Now}
}

func ret(code pb.ErrorCode, message string) *pb.RetInfo { return &pb.RetInfo{Code: code, Msg: message} }

// isBrowserCaller 判断请求是否来自已登录的浏览器用户；内部组件由网关 ACL 限定，没有用户身份。
func isBrowserCaller(ctx context.Context) bool {
	_, _, _, err := utils.GetUserInfoFromCtx(ctx)
	return err == nil
}

func (s *Service) AppendActivityLogs(ctx context.Context, req *pb.AppendActivityLogsReq) (*pb.AppendActivityLogsRsp, error) {
	if isBrowserCaller(ctx) {
		return &pb.AppendActivityLogsRsp{RetInfo: ret(pb.ErrorCode_NO_PERMISSION, "activity logs can only be written by internal components")}, nil
	}
	if req == nil || len(req.GetEntries()) == 0 || len(req.GetEntries()) > maxEntriesPerAppend {
		return &pb.AppendActivityLogsRsp{RetInfo: ret(pb.ErrorCode_INVALID_PARAM, fmt.Sprintf("entries must contain 1..%d items", maxEntriesPerAppend))}, nil
	}
	entries := make([]Entry, 0, len(req.GetEntries()))
	for _, item := range req.GetEntries() {
		entries = append(entries, Entry{SpaceID: item.GetSpaceId(), Source: item.GetSource(), Category: item.GetCategory(), Level: item.GetLevel(), Title: item.GetTitle(), Detail: item.GetDetail(), RefID: item.GetRefId(), DedupKey: item.GetDedupKey()})
	}
	for _, entry := range entries {
		if _, err := normalizeEntry(entry); err != nil {
			return &pb.AppendActivityLogsRsp{RetInfo: ret(pb.ErrorCode_INVALID_PARAM, err.Error())}, nil
		}
	}
	inserted, err := s.dao.Append(ctx, entries, s.now())
	if err != nil {
		log.ErrorContextf(ctx, "append activity logs: %v", err)
		return &pb.AppendActivityLogsRsp{RetInfo: ret(pb.ErrorCode_INNER_ERR, "append activity logs failed")}, nil
	}
	return &pb.AppendActivityLogsRsp{RetInfo: ret(pb.ErrorCode_SUCCESS, "ok"), Inserted: int32(inserted)}, nil
}

func parseTimeFilter(name, value string) (time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, nil
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("%s must be RFC3339", name)
	}
	return parsed, nil
}

func (s *Service) ListActivityLogs(ctx context.Context, req *pb.ListActivityLogsReq) (*pb.ListActivityLogsRsp, error) {
	if req == nil {
		return &pb.ListActivityLogsRsp{RetInfo: ret(pb.ErrorCode_INVALID_PARAM, "request is required")}, nil
	}
	spaceID := strings.TrimSpace(req.GetSpaceId())
	if userID, _, role, err := utils.GetUserInfoFromCtx(ctx); err == nil {
		if spaceID == "" {
			return &pb.ListActivityLogsRsp{RetInfo: ret(pb.ErrorCode_INVALID_PARAM, "space_id is required")}, nil
		}
		if s.authorizer == nil {
			return &pb.ListActivityLogsRsp{RetInfo: ret(pb.ErrorCode_INNER_ERR, "space authorizer is unavailable")}, nil
		}
		if err := s.authorizer.AuthorizeSpaceRequest(ctx, userID, spaceID, false, role); err != nil {
			return &pb.ListActivityLogsRsp{RetInfo: ret(pb.ErrorCode_NO_PERMISSION, "activity logs require space membership")}, nil
		}
	}
	since, err := parseTimeFilter("since", req.GetSince())
	if err != nil {
		return &pb.ListActivityLogsRsp{RetInfo: ret(pb.ErrorCode_INVALID_PARAM, err.Error())}, nil
	}
	until, err := parseTimeFilter("until", req.GetUntil())
	if err != nil {
		return &pb.ListActivityLogsRsp{RetInfo: ret(pb.ErrorCode_INVALID_PARAM, err.Error())}, nil
	}
	filter := Filter{SpaceID: spaceID, Source: strings.TrimSpace(req.GetSource()), Category: strings.TrimSpace(req.GetCategory()), RefID: strings.TrimSpace(req.GetRefId()), Since: since, Until: until, Page: int(req.GetPage().GetPage()), Size: int(req.GetPage().GetSize())}
	items, total, err := s.dao.List(ctx, filter)
	if err != nil {
		log.ErrorContextf(ctx, "list activity logs: %v", err)
		return &pb.ListActivityLogsRsp{RetInfo: ret(pb.ErrorCode_INNER_ERR, "list activity logs failed")}, nil
	}
	page, size := max(filter.Page, 1), filter.Size
	if size <= 0 {
		size = defaultPageSize
	}
	size = min(size, maxPageSize)
	rsp := &pb.ListActivityLogsRsp{RetInfo: ret(pb.ErrorCode_SUCCESS, "ok"), Page: &pb.PageResult{Page: uint32(page), Size: uint32(size), Total: uint32(total), HasMore: int64(page*size) < total}}
	for _, item := range items {
		rsp.Items = append(rsp.Items, &pb.ActivityLogItem{
			Id: item.ID, SpaceId: item.SpaceID, Source: item.Source, Category: item.Category, Level: item.Level, Title: item.Title,
			Detail: item.Detail, RefId: item.RefID, CreatedAt: item.CreatedAt.UTC().Format(time.RFC3339),
		})
	}
	return rsp, nil
}
