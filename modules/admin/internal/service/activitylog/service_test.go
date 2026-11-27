package activitylog

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/mooyang-code/moox/modules/admin/internal/service/auth"
	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"github.com/mooyang-code/moox/modules/admin/schema"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"trpc.group/trpc-go/trpc-go"
)

func newTestService(t *testing.T) (*Service, *time.Time) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "admin.db")), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(schema.AdminSQL()).Error)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	now := time.Date(2026, 10, 12, 8, 0, 0, 0, time.UTC)
	service := NewService(db)
	service.now = func() time.Time { return now }
	return service, &now
}

func entry(space, category, title, dedup string) *pb.ActivityLogEntry {
	return &pb.ActivityLogEntry{SpaceId: space, Source: "collector", Category: category, Title: title, Detail: `{"k":1}`, RefId: "plan-1", DedupKey: dedup}
}

func TestAppendIsIdempotentByDedupKey(t *testing.T) {
	service, _ := newTestService(t)
	ctx := context.Background()
	first, err := service.AppendActivityLogs(ctx, &pb.AppendActivityLogsReq{Entries: []*pb.ActivityLogEntry{entry("crypto", "collector.subject_change", "新增 1 个标的", "k1"), entry("crypto", "collector.fetch_plan_applied", "已生效", "k2")}})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, first.GetRetInfo().GetCode())
	require.EqualValues(t, 2, first.GetInserted())

	replay, err := service.AppendActivityLogs(ctx, &pb.AppendActivityLogsReq{Entries: []*pb.ActivityLogEntry{entry("crypto", "collector.subject_change", "新增 1 个标的", "k1"), entry("crypto", "other", "新条目", "k3")}})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, replay.GetRetInfo().GetCode())
	require.EqualValues(t, 1, replay.GetInserted(), "replayed dedup keys are ignored")

	for i := 0; i < 2; i++ {
		_, err := service.AppendActivityLogs(ctx, &pb.AppendActivityLogsReq{Entries: []*pb.ActivityLogEntry{entry("", "global", "全局事件", "")}})
		require.NoError(t, err)
	}
	listed, err := service.ListActivityLogs(ctx, &pb.ListActivityLogsReq{})
	require.NoError(t, err)
	require.EqualValues(t, 5, listed.GetPage().GetTotal())
}

func TestAppendValidatesEntries(t *testing.T) {
	service, _ := newTestService(t)
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
	service, now := newTestService(t)
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

	since := time.Date(2026, 10, 12, 10, 0, 0, 0, time.UTC).UnixMilli()
	until := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC).UnixMilli()
	window, err := service.ListActivityLogs(ctx, &pb.ListActivityLogsReq{SpaceId: "crypto", Since: since, Until: until})
	require.NoError(t, err)
	require.Len(t, window.GetItems(), 2, "since is inclusive and until is exclusive")
	require.Equal(t, time.Date(2026, 10, 12, 11, 0, 0, 0, time.UTC).UnixMilli(), window.GetItems()[0].GetCreatedAt())

	bad, err := service.ListActivityLogs(ctx, &pb.ListActivityLogsReq{Since: -1})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_INVALID_PARAM, bad.GetRetInfo().GetCode())
}

// 同一秒内的毫秒级范围查询不发生秒级截断漏查。
func TestListPreservesSubSecondMillisecondPrecision(t *testing.T) {
	service, now := newTestService(t)
	ctx := context.Background()
	base := time.Date(2026, 10, 12, 8, 0, 1, 0, time.UTC)

	*now = base.Add(150 * time.Millisecond)
	_, err := service.AppendActivityLogs(ctx, &pb.AppendActivityLogsReq{Entries: []*pb.ActivityLogEntry{entry("crypto", "c", "ms-150", "")}})
	require.NoError(t, err)

	*now = base.Add(500 * time.Millisecond)
	_, err = service.AppendActivityLogs(ctx, &pb.AppendActivityLogsReq{Entries: []*pb.ActivityLogEntry{entry("crypto", "c", "ms-500", "")}})
	require.NoError(t, err)

	*now = base.Add(850 * time.Millisecond)
	_, err = service.AppendActivityLogs(ctx, &pb.AppendActivityLogsReq{Entries: []*pb.ActivityLogEntry{entry("crypto", "c", "ms-850", "")}})
	require.NoError(t, err)

	rsp, err := service.ListActivityLogs(ctx, &pb.ListActivityLogsReq{
		SpaceId: "crypto",
		Since:   base.Add(200 * time.Millisecond).UnixMilli(),
		Until:   base.Add(800 * time.Millisecond).UnixMilli(),
	})
	require.NoError(t, err)
	require.Len(t, rsp.GetItems(), 1)
	require.Equal(t, "ms-500", rsp.GetItems()[0].GetTitle())
	require.Equal(t, base.Add(500*time.Millisecond).UnixMilli(), rsp.GetItems()[0].GetCreatedAt())
}

func TestRecordLoginAppendsGlobalAuthLog(t *testing.T) {
	service, now := newTestService(t)
	*now = time.Date(2026, 10, 12, 9, 30, 0, 250_000_000, time.UTC)
	require.NoError(t, service.RecordLogin(context.Background(), "u-1", "admin", "203.0.113.7", "test-ua"))

	rsp, err := service.ListActivityLogs(context.Background(), &pb.ListActivityLogsReq{Global: true, Category: auth.LoginLogCategory})
	require.NoError(t, err)
	require.Len(t, rsp.GetItems(), 1)
	require.Equal(t, "u-1", rsp.GetItems()[0].GetRefId())
	require.Contains(t, rsp.GetItems()[0].GetDetail(), "203.0.113.7")
	require.Equal(t, now.UnixMilli(), rsp.GetItems()[0].GetCreatedAt())
}

func TestBrowserCallersNeedSpaceOrGlobalAndCannotWrite(t *testing.T) {
	service, _ := newTestService(t)
	_, err := service.AppendActivityLogs(context.Background(), &pb.AppendActivityLogsReq{Entries: []*pb.ActivityLogEntry{
		entry("crypto", "c", "t", ""), entry("stockcn", "c", "t2", ""), entry("", "auth.login", "login", ""),
	}})
	require.NoError(t, err)

	ctx := trpc.BackgroundContext()
	trpc.SetMetaData(ctx, auth.CtxUserID, []byte("admin"))

	denied, err := service.AppendActivityLogs(ctx, &pb.AppendActivityLogsReq{Entries: []*pb.ActivityLogEntry{entry("crypto", "c", "t", "")}})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_NO_PERMISSION, denied.GetRetInfo().GetCode())

	noSpace, err := service.ListActivityLogs(ctx, &pb.ListActivityLogsReq{})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_INVALID_PARAM, noSpace.GetRetInfo().GetCode())
	own, err := service.ListActivityLogs(ctx, &pb.ListActivityLogsReq{SpaceId: "crypto"})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, own.GetRetInfo().GetCode())
	require.Len(t, own.GetItems(), 1)

	global, err := service.ListActivityLogs(ctx, &pb.ListActivityLogsReq{Global: true})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, global.GetRetInfo().GetCode())
	require.Len(t, global.GetItems(), 1)
	require.Equal(t, "auth.login", global.GetItems()[0].GetCategory())

	both, err := service.ListActivityLogs(ctx, &pb.ListActivityLogsReq{Global: true, SpaceId: "crypto"})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_INVALID_PARAM, both.GetRetInfo().GetCode())
}
