package activitylog

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/mooyang-code/moox/modules/admin/internal/service/auth/model"
	spaceSvc "github.com/mooyang-code/moox/modules/admin/internal/service/space"
	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"github.com/mooyang-code/moox/modules/admin/schema"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"trpc.group/trpc-go/trpc-go"
)

func newTestService(t *testing.T, authorizer spaceSvc.Service) (*Service, *time.Time) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "admin.db")), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(schema.AdminSQL()).Error)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	now := time.Date(2026, 10, 12, 8, 0, 0, 0, time.UTC)
	service := NewService(db, authorizer)
	service.now = func() time.Time { return now }
	return service, &now
}

func entry(space, category, title, dedup string) *pb.ActivityLogEntry {
	return &pb.ActivityLogEntry{SpaceId: space, Source: "collector", Category: category, Title: title, Detail: `{"k":1}`, RefId: "plan-1", DedupKey: dedup}
}

func TestAppendIsIdempotentByDedupKey(t *testing.T) {
	service, _ := newTestService(t, nil)
	ctx := context.Background()
	first, err := service.AppendActivityLogs(ctx, &pb.AppendActivityLogsReq{Entries: []*pb.ActivityLogEntry{entry("crypto", "collector.subject_change", "新增 1 个标的", "k1"), entry("crypto", "collector.fetch_plan_applied", "已生效", "k2")}})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, first.GetRetInfo().GetCode())
	require.EqualValues(t, 2, first.GetInserted())

	replay, err := service.AppendActivityLogs(ctx, &pb.AppendActivityLogsReq{Entries: []*pb.ActivityLogEntry{entry("crypto", "collector.subject_change", "新增 1 个标的", "k1"), entry("crypto", "other", "新条目", "k3")}})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, replay.GetRetInfo().GetCode())
	require.EqualValues(t, 1, replay.GetInserted(), "replayed dedup keys are ignored")

	// 没有幂等键的记录每次都写入。
	for i := 0; i < 2; i++ {
		_, err := service.AppendActivityLogs(ctx, &pb.AppendActivityLogsReq{Entries: []*pb.ActivityLogEntry{entry("", "global", "全局事件", "")}})
		require.NoError(t, err)
	}
	listed, err := service.ListActivityLogs(ctx, &pb.ListActivityLogsReq{})
	require.NoError(t, err)
	require.EqualValues(t, 5, listed.GetPage().GetTotal())
}

func TestAppendValidatesEntries(t *testing.T) {
	service, _ := newTestService(t, nil)
	for name, bad := range map[string]*pb.ActivityLogEntry{
		"missing title":  {Source: "collector", Category: "c"},
		"bad level":      {Source: "collector", Category: "c", Title: "t", Level: "fatal"},
		"invalid detail": {Source: "collector", Category: "c", Title: "t", Detail: "{not json"},
	} {
		rsp, err := service.AppendActivityLogs(context.Background(), &pb.AppendActivityLogsReq{Entries: []*pb.ActivityLogEntry{bad}})
		require.NoError(t, err, name)
		require.Equal(t, pb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode(), name)
	}
	rsp, err := service.AppendActivityLogs(context.Background(), &pb.AppendActivityLogsReq{})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode())
}

func TestListFiltersAndPaginatesNewestFirst(t *testing.T) {
	service, now := newTestService(t, nil)
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		*now = now.Add(time.Hour)
		category := "collector.subject_change"
		if i%2 == 1 {
			category = "collector.fetch_plan_applied"
		}
		_, err := service.AppendActivityLogs(ctx, &pb.AppendActivityLogsReq{Entries: []*pb.ActivityLogEntry{entry("crypto", category, fmt.Sprintf("事件 %d", i), "")}})
		require.NoError(t, err)
	}
	_, err := service.AppendActivityLogs(ctx, &pb.AppendActivityLogsReq{Entries: []*pb.ActivityLogEntry{entry("stockcn", "collector.subject_change", "别的空间", "")}})
	require.NoError(t, err)

	page1, err := service.ListActivityLogs(ctx, &pb.ListActivityLogsReq{SpaceId: "crypto", Page: &pb.Page{Page: 1, Size: 2}})
	require.NoError(t, err)
	require.EqualValues(t, 5, page1.GetPage().GetTotal())
	require.True(t, page1.GetPage().GetHasMore())
	require.Equal(t, []string{"事件 4", "事件 3"}, []string{page1.GetItems()[0].GetTitle(), page1.GetItems()[1].GetTitle()})
	page3, err := service.ListActivityLogs(ctx, &pb.ListActivityLogsReq{SpaceId: "crypto", Page: &pb.Page{Page: 3, Size: 2}})
	require.NoError(t, err)
	require.Len(t, page3.GetItems(), 1)
	require.False(t, page3.GetPage().GetHasMore())

	byCategory, err := service.ListActivityLogs(ctx, &pb.ListActivityLogsReq{SpaceId: "crypto", Category: "collector.fetch_plan_applied"})
	require.NoError(t, err)
	require.Len(t, byCategory.GetItems(), 2)

	since := time.Date(2026, 10, 12, 10, 0, 0, 0, time.UTC).Format(time.RFC3339)
	until := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC).Format(time.RFC3339)
	window, err := service.ListActivityLogs(ctx, &pb.ListActivityLogsReq{SpaceId: "crypto", Since: since, Until: until})
	require.NoError(t, err)
	require.Len(t, window.GetItems(), 2, "since is inclusive and until is exclusive")
	require.Equal(t, "2026-10-12T11:00:00Z", window.GetItems()[0].GetCreatedAt())

	bad, err := service.ListActivityLogs(ctx, &pb.ListActivityLogsReq{Since: "yesterday"})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_INVALID_PARAM, bad.GetRetInfo().GetCode())
}

type memberAuthorizer struct {
	spaceSvc.Service
	allowed map[string]bool
}

func (a memberAuthorizer) AuthorizeSpaceRequest(_ context.Context, _, spaceID string, mutates bool, _ int32) error {
	if mutates || !a.allowed[spaceID] {
		return errors.New("not a member")
	}
	return nil
}

func TestBrowserCallersNeedMembershipAndCannotWrite(t *testing.T) {
	service, _ := newTestService(t, memberAuthorizer{allowed: map[string]bool{"crypto": true}})
	_, err := service.AppendActivityLogs(context.Background(), &pb.AppendActivityLogsReq{Entries: []*pb.ActivityLogEntry{entry("crypto", "c", "t", ""), entry("stockcn", "c", "t2", "")}})
	require.NoError(t, err)

	ctx := trpc.BackgroundContext()
	trpc.SetMetaData(ctx, model.CtxUserID, []byte("member"))
	trpc.SetMetaData(ctx, model.CtxUserRole, []byte("1"))

	denied, err := service.AppendActivityLogs(ctx, &pb.AppendActivityLogsReq{Entries: []*pb.ActivityLogEntry{entry("crypto", "c", "t", "")}})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_NO_PERMISSION, denied.GetRetInfo().GetCode())

	noSpace, err := service.ListActivityLogs(ctx, &pb.ListActivityLogsReq{})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_INVALID_PARAM, noSpace.GetRetInfo().GetCode())
	other, err := service.ListActivityLogs(ctx, &pb.ListActivityLogsReq{SpaceId: "stockcn"})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_NO_PERMISSION, other.GetRetInfo().GetCode())
	own, err := service.ListActivityLogs(ctx, &pb.ListActivityLogsReq{SpaceId: "crypto"})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, own.GetRetInfo().GetCode())
	require.Len(t, own.GetItems(), 1)
}
