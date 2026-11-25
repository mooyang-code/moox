package activitylog

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/admin/internal/service/auth/utils"
	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"gorm.io/gorm"
	"trpc.group/trpc-go/trpc-go/log"
)

// Service 实现全局活动日志的写入与查询。写入只允许内部组件（服务目录 ACL 限定），
// 浏览器用户查询时必须指定一个空间，或查询不属于任何空间的全局事件。
type Service struct {
	pb.UnimplementedActivityLog
	dao *DAO
	now func() time.Time
}

func NewService(db *gorm.DB) *Service {
	return &Service{dao: NewDAO(db), now: time.Now}
}

func ret(code pb.ErrorCode, message string) *pb.RetInfo { return &pb.RetInfo{Code: code, Msg: message} }

// isBrowserCaller 判断请求是否来自已登录的浏览器用户；内部组件由网关 ACL 限定，没有用户身份。
func isBrowserCaller(ctx context.Context) bool {
	_, _, err := utils.GetUserInfoFromCtx(ctx)
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

// millisFilter 把请求里的 Unix 毫秒换成时刻，0 表示不限。
func millisFilter(ms int64) time.Time {
	if ms == 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms).UTC()
}

func (s *Service) ListActivityLogs(ctx context.Context, req *pb.ListActivityLogsReq) (*pb.ListActivityLogsRsp, error) {
	if req == nil {
		return &pb.ListActivityLogsRsp{RetInfo: ret(pb.ErrorCode_INVALID_PARAM, "request is required")}, nil
	}
	spaceID := strings.TrimSpace(req.GetSpaceId())
	if req.GetGlobal() && spaceID != "" {
		return &pb.ListActivityLogsRsp{RetInfo: ret(pb.ErrorCode_INVALID_PARAM, "space_id and global are mutually exclusive")}, nil
	}
	if isBrowserCaller(ctx) && !req.GetGlobal() && spaceID == "" {
		return &pb.ListActivityLogsRsp{RetInfo: ret(pb.ErrorCode_INVALID_PARAM, "space_id or global is required")}, nil
	}
	if req.GetSince() < 0 || req.GetUntil() < 0 {
		return &pb.ListActivityLogsRsp{RetInfo: ret(pb.ErrorCode_INVALID_PARAM, "since 与 until 必须是非负的 Unix 毫秒")}, nil
	}
	since, until := millisFilter(req.GetSince()), millisFilter(req.GetUntil())
	filter := Filter{SpaceID: spaceID, Global: req.GetGlobal(), Source: strings.TrimSpace(req.GetSource()), Category: strings.TrimSpace(req.GetCategory()), RefID: strings.TrimSpace(req.GetRefId()), Since: since, Until: until, Page: int(req.GetPage().GetPage()), Size: int(req.GetPage().GetSize())}
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
			Detail: item.Detail, RefId: item.RefID, CreatedAt: item.CreatedAt.UnixMilli(),
		})
	}
	return rsp, nil
}
