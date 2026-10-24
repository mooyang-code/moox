package garbage

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/mooyang-code/moox/modules/admin/schema"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func newTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "admin.db")), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(schema.AdminSQL()).Error)
	return db
}

type gatewayFunc func(context.Context, string, string, any, any) error

func (f gatewayFunc) Invoke(ctx context.Context, service, method string, req, rsp any) error {
	return f(ctx, service, method, req, rsp)
}

func TestCollectorCallsCloudFunction(t *testing.T) {
	calls := 0
	collector, err := NewCollector(gatewayFunc(func(_ context.Context, service, method string, req, rsp any) error {
		calls++
		require.Equal(t, "trpc.moox.cloudfunction.CloudFunctionMgr", service)
		require.Equal(t, "CollectGarbage", method)
		raw, err := json.Marshal(req)
		require.NoError(t, err)
		require.JSONEq(t, `{"dry_run":false}`, string(raw))
		rsp.(*cloudFunctionGarbageSummary).Packages = 2
		return nil
	}))
	require.NoError(t, err)

	require.NoError(t, collector.Run(context.Background()))
	require.Equal(t, 1, calls)
}

func TestCollectorReportsCloudFunctionFailure(t *testing.T) {
	collector, err := NewCollector(gatewayFunc(func(_ context.Context, _, _ string, _, rsp any) error {
		rsp.(*cloudFunctionGarbageSummary).RetInfo.Code = 999
		rsp.(*cloudFunctionGarbageSummary).RetInfo.Msg = "list cloud accounts: boom"
		return nil
	}))
	require.NoError(t, err)

	require.ErrorContains(t, collector.Run(context.Background()), "collect FunctionInstance garbage: list cloud accounts: boom")
}

func TestCollectorRequiresAdminGatewayAndReportsRoutingFailure(t *testing.T) {
	_, err := NewCollector(nil)
	require.ErrorContains(t, err, "admin gateway")
	collector, err := NewCollector(gatewayFunc(func(context.Context, string, string, any, any) error {
		return errors.New("no active FunctionInstance deployment")
	}))
	require.NoError(t, err)
	require.ErrorContains(t, collector.Run(context.Background()), "no active FunctionInstance deployment")
}
