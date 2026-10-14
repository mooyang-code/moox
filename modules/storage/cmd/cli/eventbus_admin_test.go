package main

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	adminpb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	storageconfig "github.com/mooyang-code/moox/modules/storage/internal/config"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

// fakeGateway 记录经网关发给 Admin 的调用，并按方法返回预设的应答。
type fakeGateway struct {
	calls   []fakeCall
	invoke  func(method string, req, rsp interface{}) error
	closed  bool
	service string
}

type fakeCall struct {
	method string
	req    interface{}
}

func (g *fakeGateway) Invoke(_ context.Context, service, method string, req, rsp interface{}) error {
	g.service = service
	g.calls = append(g.calls, fakeCall{method: method, req: req})
	if g.invoke == nil {
		return nil
	}
	return g.invoke(method, req, rsp)
}

func (g *fakeGateway) Close() error { g.closed = true; return nil }

func okRet() *adminpb.RetInfo { return &adminpb.RetInfo{Code: adminpb.ErrorCode_SUCCESS} }

func TestGatewayEventBusAdminInspectMapsConsumerStates(t *testing.T) {
	gateway := &fakeGateway{invoke: func(method string, _, rsp interface{}) error {
		require.Equal(t, "InspectEventBus", method)
		*rsp.(*adminpb.InspectEventBusRsp) = adminpb.InspectEventBusRsp{RetInfo: okRet(), Consumers: []*adminpb.EventBusConsumerState{
			{Durable: events.StorageViewFactorConsumer, Exists: true, FilterSubjects: []string{"a", "b"}},
			{Durable: events.StorageViewMetricsConsumer},
		}}
		return nil
	}}
	admin := gatewayEventBusAdmin{gateway: gateway}

	got, err := admin.Inspect(t.Context(), events.StorageViewConsumerStream, []string{events.StorageViewFactorConsumer, events.StorageViewMetricsConsumer})
	require.NoError(t, err)
	require.Equal(t, sysDeployService, gateway.service)
	require.Equal(t, viewConsumerFilterState{Exists: true, Filters: []string{"a", "b"}}, got[events.StorageViewFactorConsumer])
	require.Equal(t, viewConsumerFilterState{}, got[events.StorageViewMetricsConsumer])
	request := gateway.calls[0].req.(*adminpb.InspectEventBusReq)
	require.Equal(t, events.StorageViewConsumerStream, request.GetStream())
	require.Equal(t, []string{events.StorageViewFactorConsumer, events.StorageViewMetricsConsumer}, request.GetDurables())
}

func TestGatewayEventBusAdminInspectRejectsIncompleteAnswer(t *testing.T) {
	gateway := &fakeGateway{invoke: func(_ string, _, rsp interface{}) error {
		*rsp.(*adminpb.InspectEventBusRsp) = adminpb.InspectEventBusRsp{RetInfo: okRet(), Consumers: []*adminpb.EventBusConsumerState{{Durable: events.StorageViewFactorConsumer, Exists: true}}}
		return nil
	}}
	_, err := gatewayEventBusAdmin{gateway: gateway}.Inspect(t.Context(), events.StorageViewConsumerStream, []string{events.StorageViewFactorConsumer, events.StorageViewMetricsConsumer})
	require.ErrorContains(t, err, events.StorageViewMetricsConsumer, "缺少某个消费者的状态时不能当作它不存在")
}

func TestGatewayEventBusAdminSurfacesAdminFailuresAndTransportErrors(t *testing.T) {
	failed := &fakeGateway{invoke: func(_ string, _, rsp interface{}) error {
		*rsp.(*adminpb.PurgeEventBusSubjectsRsp) = adminpb.PurgeEventBusSubjectsRsp{RetInfo: &adminpb.RetInfo{Code: adminpb.ErrorCode_INNER_ERR, Msg: "连接 EventBus 失败"}}
		return nil
	}}
	err := gatewayEventBusAdmin{gateway: failed}.PurgeSubjects(t.Context(), events.StorageViewConsumerStream, []string{"s"})
	require.ErrorContains(t, err, "PurgeEventBusSubjects")
	require.ErrorContains(t, err, "连接 EventBus 失败", "要带上 Admin 给出的原因")

	broken := &fakeGateway{invoke: func(string, interface{}, interface{}) error { return errors.New("no route") }}
	err = gatewayEventBusAdmin{gateway: broken}.PurgeSubjects(t.Context(), events.StorageViewConsumerStream, []string{"s"})
	require.ErrorContains(t, err, "经网关调用 Admin PurgeEventBusSubjects 失败")
	require.ErrorContains(t, err, "no route")
}

func TestGatewayEventBusAdminDeleteReturnsDeletedEvenOnFailure(t *testing.T) {
	gateway := &fakeGateway{invoke: func(_ string, _, rsp interface{}) error {
		*rsp.(*adminpb.DeleteEventBusConsumersRsp) = adminpb.DeleteEventBusConsumersRsp{
			RetInfo: &adminpb.RetInfo{Code: adminpb.ErrorCode_INNER_ERR, Msg: "删除消费者 storage_view_metrics 失败"},
			Deleted: []string{events.StorageViewFactorConsumer},
		}
		return nil
	}}
	deleted, err := gatewayEventBusAdmin{gateway: gateway}.DeleteConsumers(t.Context(), events.StorageViewConsumerStream, events.StorageViewConsumerDurables)
	require.ErrorContains(t, err, "storage_view_metrics")
	require.Equal(t, []string{events.StorageViewFactorConsumer}, deleted, "失败前已删除的也要告诉调用方")
}

// fakeEventBusAdmin 直接实现 eventBusAdmin，用来驱动各命令的流程。
type fakeEventBusAdmin struct {
	inspect       map[string]viewConsumerFilterState
	inspectErr    error
	deleteErr     error
	purgeErr      error
	deleted       []string
	purged        []string
	purgeCalls    int
	inspectCalled bool
	closed        bool
}

func (f *fakeEventBusAdmin) Inspect(_ context.Context, _ string, _ []string) (map[string]viewConsumerFilterState, error) {
	f.inspectCalled = true
	return f.inspect, f.inspectErr
}

func (f *fakeEventBusAdmin) DeleteConsumers(_ context.Context, _ string, durables []string) ([]string, error) {
	if f.deleteErr != nil {
		return nil, f.deleteErr
	}
	f.deleted = append(f.deleted, durables...)
	return durables, nil
}

func (f *fakeEventBusAdmin) PurgeSubjects(_ context.Context, _ string, subjects []string) error {
	f.purgeCalls++
	if f.purgeErr != nil {
		return f.purgeErr
	}
	f.purged = append(f.purged, subjects...)
	return nil
}

func (f *fakeEventBusAdmin) Close() error { f.closed = true; return nil }

func useFakeEventBusAdmin(t *testing.T, admin *fakeEventBusAdmin) {
	t.Helper()
	original := openEventBusAdmin
	t.Cleanup(func() { openEventBusAdmin = original })
	openEventBusAdmin = func(string, string) (eventBusAdmin, error) { return admin, nil }
}

func resetTestViewConfig() storageconfig.StorageView {
	return storageconfig.StorageView{ConsumerPartitions: []storageconfig.StorageViewConsumerPartition{
		{Durable: events.StorageViewFactorConsumer, Routes: []storageconfig.StorageViewConsumerRoute{{SpaceID: "crypto", DatasetIDs: []string{"ds_a"}}}},
		{Durable: events.StorageViewMiscConsumer, Routes: []storageconfig.StorageViewConsumerRoute{{SpaceID: "stockcn", DatasetIDs: []string{"*"}}}},
	}}
}

func TestResetStorageViewConsumersDeletesConsumersThenPurgesEverySubject(t *testing.T) {
	admin := &fakeEventBusAdmin{}
	views := []resetViewRecord{{SpaceID: "stockcn", ViewID: "v", PrimaryDatasetID: "ds_b"}}
	opts := resetViewConsumersOptions{stream: events.StorageViewConsumerStream}

	mutated, err := resetStorageViewConsumers(t.Context(), admin, opts, resetTestViewConfig(), views)
	require.NoError(t, err)
	require.True(t, mutated)
	require.Equal(t, events.StorageViewConsumerDurables, admin.deleted)
	registry, err := events.DefaultRegistry()
	require.NoError(t, err)
	expected, err := resetEventSubjects(registry, resetTestViewConfig(), views)
	require.NoError(t, err)
	require.Len(t, expected, 8, "两个数据集 × 四类事件，通配路由按 View 展开")
	require.Equal(t, expected, admin.purged)
	require.Equal(t, 1, admin.purgeCalls, "所有主题一次提交")
}

func TestResetStorageViewConsumersReportsMutationOnceBrokerStateMayHaveChanged(t *testing.T) {
	views := []resetViewRecord{{SpaceID: "stockcn", ViewID: "v", PrimaryDatasetID: "ds_b"}}
	opts := resetViewConsumersOptions{stream: events.StorageViewConsumerStream}

	admin := &fakeEventBusAdmin{deleteErr: errors.New("admin unreachable")}
	mutated, err := resetStorageViewConsumers(t.Context(), admin, opts, resetTestViewConfig(), views)
	require.ErrorContains(t, err, "delete consumers")
	require.True(t, mutated, "删除请求发出后就可能已经改动，调用方要据此保留重置状态而不是回滚本地")
	require.Zero(t, admin.purgeCalls, "删除消费者失败后不再清除消息")

	admin = &fakeEventBusAdmin{purgeErr: errors.New("timeout")}
	mutated, err = resetStorageViewConsumers(t.Context(), admin, opts, resetTestViewConfig(), views)
	require.ErrorContains(t, err, "purge stream")
	require.True(t, mutated)

	// 通配路由没有可展开的数据集：连主题都渲染不出，此时还没有动过任何东西。
	admin = &fakeEventBusAdmin{}
	mutated, err = resetStorageViewConsumers(t.Context(), admin, opts, resetTestViewConfig(), nil)
	require.Error(t, err)
	require.False(t, mutated)
	require.Empty(t, admin.deleted)
}

func TestResetStorageViewConsumersSkipsPurgeWithoutSubjects(t *testing.T) {
	admin := &fakeEventBusAdmin{}
	mutated, err := resetStorageViewConsumers(t.Context(), admin, resetViewConsumersOptions{stream: events.StorageViewConsumerStream}, storageconfig.StorageView{}, nil)
	require.NoError(t, err)
	require.True(t, mutated)
	require.Equal(t, events.StorageViewConsumerDurables, admin.deleted)
	require.Zero(t, admin.purgeCalls, "没有主题时不能向 Admin 发空清除请求")
}

func TestPreflightResetEventBusChecksStreamThroughAdminWithoutMutating(t *testing.T) {
	admin := &fakeEventBusAdmin{}
	views := []resetViewRecord{{SpaceID: "stockcn", ViewID: "v", PrimaryDatasetID: "ds_b"}}
	opts := resetViewConsumersOptions{stream: events.StorageViewConsumerStream}
	require.NoError(t, preflightResetEventBus(t.Context(), admin, opts, resetTestViewConfig(), views))
	require.True(t, admin.inspectCalled)
	require.Empty(t, admin.deleted)
	require.Zero(t, admin.purgeCalls)

	admin = &fakeEventBusAdmin{inspectErr: errors.New("Admin InspectEventBus 失败: EventBus 流 MOOX_STORAGE 不存在")}
	err := preflightResetEventBus(t.Context(), admin, opts, resetTestViewConfig(), views)
	require.ErrorContains(t, err, "MOOX_STORAGE", "预检失败要说明是哪条流")
	require.Empty(t, admin.deleted)
}

func TestPurgeDatasetEventSubjectsGoesThroughAdmin(t *testing.T) {
	admin := &fakeEventBusAdmin{}
	useFakeEventBusAdmin(t, admin)
	subjects, err := datasetEventSubjects("mooxsys", "ds_a")
	require.NoError(t, err)

	err = purgeDatasetEventSubjectsFromJetStream(t.Context(), purgeDatasetEventsOptions{stream: events.StorageViewConsumerStream, storageConf: "x"}, subjects)
	require.NoError(t, err)
	require.Equal(t, subjects, admin.purged)
	require.True(t, admin.closed, "用完要关闭网关客户端")

	admin = &fakeEventBusAdmin{purgeErr: errors.New("boom")}
	useFakeEventBusAdmin(t, admin)
	err = purgeDatasetEventSubjectsFromJetStream(t.Context(), purgeDatasetEventsOptions{stream: events.StorageViewConsumerStream}, subjects)
	require.ErrorContains(t, err, "purge subjects")
	require.True(t, admin.closed)
}

func TestInspectViewConsumerFiltersAsksForTheTwoStaticConsumers(t *testing.T) {
	admin := &fakeEventBusAdmin{inspect: map[string]viewConsumerFilterState{events.StorageViewFactorConsumer: {Exists: true}}}
	got, err := inspectViewConsumerFilters(t.Context(), admin, events.StorageViewConsumerStream)
	require.NoError(t, err)
	require.True(t, got[events.StorageViewFactorConsumer].Exists)

	admin = &fakeEventBusAdmin{inspectErr: errors.New("denied")}
	_, err = inspectViewConsumerFilters(t.Context(), admin, events.StorageViewConsumerStream)
	require.ErrorContains(t, err, "inspect View consumers")
}

func TestDeleteJetStreamConsumerOnlyAcceptsViewConsumers(t *testing.T) {
	admin := &fakeEventBusAdmin{}
	useFakeEventBusAdmin(t, admin)

	deleted, err := deleteJetStreamConsumer(t.Context(), "conf", "root", events.StorageViewConsumerStream, events.StorageViewFactorConsumer)
	require.NoError(t, err)
	require.True(t, deleted)
	require.True(t, admin.closed)

	_, err = deleteJetStreamConsumer(t.Context(), "conf", "root", events.StorageViewConsumerStream, "factor_collector_period_v1")
	require.ErrorContains(t, err, "不支持的消费者")
	require.Equal(t, []string{events.StorageViewFactorConsumer}, admin.deleted, "越界的消费者名不能发到 Admin")
}

func TestViewMaintenanceCommandsNoLongerAcceptAdminCredentialFlags(t *testing.T) {
	const undefined = "flag provided but not defined"
	for _, flagName := range []string{"--credential-file", "--eventbus-url"} {
		require.ErrorContains(t, runResetViewConsumers([]string{"--dry-run", flagName, "x"}, &bytes.Buffer{}, &bytes.Buffer{}), undefined, flagName)
		require.ErrorContains(t, runReconcileViewConsumers([]string{"--dry-run", flagName, "x"}, &bytes.Buffer{}, &bytes.Buffer{}), undefined, flagName)
		require.ErrorContains(t, runPurgeDatasetEvents(t.Context(), []string{"--space", "s", "--dataset", "d", "--dry-run", flagName, "x"}, &bytes.Buffer{}), undefined, flagName)
		require.ErrorContains(t, runForceRebuildView([]string{"--space-id", "s", "--view-id", "v", "--consumer", events.StorageViewMiscConsumer, "--dry-run", flagName, "x"}, &bytes.Buffer{}, &bytes.Buffer{}), undefined, flagName)
		require.ErrorContains(t, runRepairView([]string{"--space-id", "s", "--view-id", "v", "--consumer", events.StorageViewMiscConsumer, "--dry-run", flagName, "x"}, &bytes.Buffer{}, &bytes.Buffer{}), undefined, flagName)
	}
}

func TestOpenGatewayEventBusAdminRequiresStorageViewIdentity(t *testing.T) {
	dir := t.TempDir()
	config := filepath.Join(dir, "storage.yaml")
	require.NoError(t, os.WriteFile(config, []byte("gateway_client:\n  caller: collector\n  key_id: k\n  key_file: ../k.key\n"), 0o600))
	_, err := openGatewayEventBusAdmin(config, dir)
	require.ErrorContains(t, err, "storage-view")
	require.ErrorContains(t, err, "collector")

	_, err = openGatewayEventBusAdmin("", dir)
	require.ErrorContains(t, err, "--storage-conf")
}

// repairFixture 准备一个带一个 View 的元数据库和最小配置，返回 runRepairViewOperation 的选项。
func repairFixture(t *testing.T) repairViewOptions {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "meta.db")
	db, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)
	_, err = db.Exec(`CREATE TABLE t_views (c_space_id TEXT, c_view_id TEXT, c_engine TEXT, c_active_index_id TEXT, c_desired_view_revision INTEGER, c_attrs_json TEXT, c_mtime TEXT, PRIMARY KEY(c_space_id,c_view_id))`)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO t_views VALUES ('space','view','duckdb','',3,'{"space_id":"space","view_id":"view","desired_view_revision":3}','')`)
	require.NoError(t, err)
	require.NoError(t, db.Close())
	conf := filepath.Join(dir, "config", "storage.yaml")
	require.NoError(t, os.MkdirAll(filepath.Dir(conf), 0o755))
	require.NoError(t, os.WriteFile(conf, []byte("storage:\n  root: "+dir+"\n  metadata:\n    path: "+dbPath+"\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "start.sh"), []byte("#!/bin/sh\n"), 0o755))
	return repairViewOptions{
		action: "repair-view", spaceID: "space", viewID: "view", storageConf: conf, packageRoot: dir, stream: events.StorageViewConsumerStream,
		consumer: events.StorageViewFactorConsumer, deliverPolicy: "new", timeout: time.Minute, yes: true, resetConsumer: true, forceRebuild: true, restart: true,
	}
}

func TestRepairViewChecksEventBusBeforeStoppingStorageView(t *testing.T) {
	var lifecycle []string
	originalLifecycle := runRepairLifecycle
	t.Cleanup(func() { runRepairLifecycle = originalLifecycle })
	runRepairLifecycle = func(_ context.Context, _, action, _ string, _ io.Writer) error {
		lifecycle = append(lifecycle, action)
		return nil
	}

	broken := &fakeEventBusAdmin{inspectErr: errors.New("Admin InspectEventBus 失败: 连接 EventBus 失败")}
	useFakeEventBusAdmin(t, broken)
	opts := repairFixture(t)
	err := runRepairViewOperation(t.Context(), opts, &bytes.Buffer{}, &bytes.Buffer{})
	require.ErrorContains(t, err, "preflight EventBus")
	require.ErrorContains(t, err, "连接 EventBus 失败")
	require.Empty(t, lifecycle, "预检失败时 storage-view 不能被停止")
	require.Empty(t, broken.deleted)

	// dry-run 同样预检，并且不停服务、不删消费者。
	opts = repairFixture(t)
	opts.dryRun, opts.yes = true, false
	err = runRepairViewOperation(t.Context(), opts, &bytes.Buffer{}, &bytes.Buffer{})
	require.ErrorContains(t, err, "preflight EventBus")
	require.Empty(t, lifecycle)

	healthy := &fakeEventBusAdmin{inspect: map[string]viewConsumerFilterState{events.StorageViewFactorConsumer: {Exists: true}}}
	useFakeEventBusAdmin(t, healthy)
	var stdout bytes.Buffer
	opts = repairFixture(t)
	require.NoError(t, runRepairViewOperation(t.Context(), opts, &stdout, &bytes.Buffer{}))
	require.Equal(t, []string{"stop", "start"}, lifecycle)
	require.Equal(t, []string{events.StorageViewFactorConsumer}, healthy.deleted)
	require.Contains(t, stdout.String(), `"consumer_reset":true`)

	// 不删消费者的修复不需要 EventBus，也就不会去预检。
	lifecycle = nil
	useFakeEventBusAdmin(t, broken)
	opts = repairFixture(t)
	opts.resetConsumer = false
	require.NoError(t, runRepairViewOperation(t.Context(), opts, &bytes.Buffer{}, &bytes.Buffer{}))
	require.Equal(t, []string{"stop", "start"}, lifecycle)
}
