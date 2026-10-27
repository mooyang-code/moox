package main

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	adminpb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
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
			{Durable: testViewDurableA, Exists: true, FilterSubjects: []string{"a", "b"}},
			{Durable: testViewDurableB},
		}}
		return nil
	}}
	admin := gatewayEventBusAdmin{gateway: gateway}

	got, err := admin.Inspect(t.Context(), events.StorageViewConsumerStream, []string{testViewDurableA, testViewDurableB})
	require.NoError(t, err)
	require.Equal(t, sysDeployService, gateway.service)
	require.Equal(t, viewConsumerFilterState{Exists: true, Filters: []string{"a", "b"}}, got[testViewDurableA])
	require.Equal(t, viewConsumerFilterState{}, got[testViewDurableB])
	request := gateway.calls[0].req.(*adminpb.InspectEventBusReq)
	require.Equal(t, events.StorageViewConsumerStream, request.GetStream())
	require.Equal(t, []string{testViewDurableA, testViewDurableB}, request.GetDurables())
}

func TestGatewayEventBusAdminInspectRejectsIncompleteAnswer(t *testing.T) {
	gateway := &fakeGateway{invoke: func(_ string, _, rsp interface{}) error {
		*rsp.(*adminpb.InspectEventBusRsp) = adminpb.InspectEventBusRsp{RetInfo: okRet(), Consumers: []*adminpb.EventBusConsumerState{{Durable: testViewDurableA, Exists: true}}}
		return nil
	}}
	_, err := gatewayEventBusAdmin{gateway: gateway}.Inspect(t.Context(), events.StorageViewConsumerStream, []string{testViewDurableA, testViewDurableB})
	require.ErrorContains(t, err, testViewDurableB, "缺少某个消费者的状态时不能当作它不存在")
}

func TestGatewayEventBusAdminSurfacesAdminFailuresAndTransportErrors(t *testing.T) {
	failed := &fakeGateway{invoke: func(_ string, _, rsp interface{}) error {
		*rsp.(*adminpb.PurgeEventBusSubjectsRsp) = adminpb.PurgeEventBusSubjectsRsp{RetInfo: &adminpb.RetInfo{Code: adminpb.ErrorCode_INNER_ERR, Msg: "连接 EventBus 失败"}}
		return nil
	}}
	_, err := gatewayEventBusAdmin{gateway: failed}.PurgeSubjects(t.Context(), events.StorageViewConsumerStream, []string{"s"})
	require.ErrorContains(t, err, "PurgeEventBusSubjects")
	require.ErrorContains(t, err, "连接 EventBus 失败", "要带上 Admin 给出的原因")

	broken := &fakeGateway{invoke: func(string, interface{}, interface{}) error { return errors.New("no route") }}
	_, err = gatewayEventBusAdmin{gateway: broken}.PurgeSubjects(t.Context(), events.StorageViewConsumerStream, []string{"s"})
	require.ErrorContains(t, err, "经网关调用 Admin PurgeEventBusSubjects 失败")
	require.ErrorContains(t, err, "no route")
}

func TestGatewayEventBusAdminDeleteReturnsDeletedEvenOnFailure(t *testing.T) {
	gateway := &fakeGateway{invoke: func(_ string, _, rsp interface{}) error {
		*rsp.(*adminpb.DeleteEventBusConsumersRsp) = adminpb.DeleteEventBusConsumersRsp{
			RetInfo: &adminpb.RetInfo{Code: adminpb.ErrorCode_INNER_ERR, Msg: "删除消费者 view_ba9876543210 失败"},
			Deleted: []string{testViewDurableA},
		}
		return nil
	}}
	deleted, _, err := gatewayEventBusAdmin{gateway: gateway}.DeleteConsumers(t.Context(), events.StorageViewConsumerStream, testViewDurables)
	require.ErrorContains(t, err, "view_ba9876543210")
	require.Equal(t, []string{testViewDurableA}, deleted, "失败前已删除的也要告诉调用方")
}

// attempted 决定重置失败后本地状态能否恢复：Admin 明确说没发出过变更请求才算“没动过”，
// 网关层没有拿到应答时请求可能已经执行，必须按已改动处理。
func TestGatewayEventBusAdminReportsWhetherEventBusMayHaveChanged(t *testing.T) {
	notAttempted := &fakeGateway{invoke: func(_ string, _, rsp interface{}) error {
		failure := &adminpb.RetInfo{Code: adminpb.ErrorCode_INNER_ERR, Msg: "连接 EventBus 失败"}
		switch response := rsp.(type) {
		case *adminpb.DeleteEventBusConsumersRsp:
			*response = adminpb.DeleteEventBusConsumersRsp{RetInfo: failure}
		case *adminpb.PurgeEventBusSubjectsRsp:
			*response = adminpb.PurgeEventBusSubjectsRsp{RetInfo: failure}
		}
		return nil
	}}
	admin := gatewayEventBusAdmin{gateway: notAttempted}
	_, attempted, err := admin.DeleteConsumers(t.Context(), events.StorageViewConsumerStream, testViewDurables)
	require.Error(t, err)
	require.False(t, attempted, "Admin 没发出过删除请求，EventBus 没被改动")
	attempted, err = admin.PurgeSubjects(t.Context(), events.StorageViewConsumerStream, []string{"s"})
	require.Error(t, err)
	require.False(t, attempted)

	executedThenFailed := &fakeGateway{invoke: func(_ string, _, rsp interface{}) error {
		switch response := rsp.(type) {
		case *adminpb.DeleteEventBusConsumersRsp:
			*response = adminpb.DeleteEventBusConsumersRsp{RetInfo: &adminpb.RetInfo{Code: adminpb.ErrorCode_INNER_ERR, Msg: "x"}, Attempted: true}
		case *adminpb.PurgeEventBusSubjectsRsp:
			*response = adminpb.PurgeEventBusSubjectsRsp{RetInfo: &adminpb.RetInfo{Code: adminpb.ErrorCode_INNER_ERR, Msg: "x"}, Attempted: true}
		}
		return nil
	}}
	admin = gatewayEventBusAdmin{gateway: executedThenFailed}
	_, attempted, err = admin.DeleteConsumers(t.Context(), events.StorageViewConsumerStream, testViewDurables)
	require.Error(t, err)
	require.True(t, attempted)
	attempted, err = admin.PurgeSubjects(t.Context(), events.StorageViewConsumerStream, []string{"s"})
	require.Error(t, err)
	require.True(t, attempted)

	noAnswer := &fakeGateway{invoke: func(string, interface{}, interface{}) error { return errors.New("deadline exceeded") }}
	admin = gatewayEventBusAdmin{gateway: noAnswer}
	_, attempted, err = admin.DeleteConsumers(t.Context(), events.StorageViewConsumerStream, testViewDurables)
	require.Error(t, err)
	require.True(t, attempted, "没有拿到应答时请求可能已经执行")
	attempted, err = admin.PurgeSubjects(t.Context(), events.StorageViewConsumerStream, []string{"s"})
	require.Error(t, err)
	require.True(t, attempted)
}

// fakeEventBusAdmin 直接实现 eventBusAdmin，用来驱动各命令的流程。
type fakeEventBusAdmin struct {
	inspect    map[string]viewConsumerFilterState
	inspectErr error
	// deleteErr / purgeErr 非空时对应调用失败，*Attempted 决定失败时是否算“可能已改动”。
	deleteErr, purgeErr             error
	deleteAttempted, purgeAttempted bool
	deleted                         []string
	purged                          []string
	purgeBatches                    [][]string
	purgeCalls                      int
	inspectCalled                   bool
	closed                          bool
}

func (f *fakeEventBusAdmin) Inspect(_ context.Context, _ string, _ []string) (map[string]viewConsumerFilterState, error) {
	f.inspectCalled = true
	return f.inspect, f.inspectErr
}

func (f *fakeEventBusAdmin) DeleteConsumers(_ context.Context, _ string, durables []string) ([]string, bool, error) {
	if f.deleteErr != nil {
		return nil, f.deleteAttempted, f.deleteErr
	}
	f.deleted = append(f.deleted, durables...)
	return durables, true, nil
}

func (f *fakeEventBusAdmin) PurgeSubjects(_ context.Context, _ string, subjects []string) (bool, error) {
	f.purgeCalls++
	f.purgeBatches = append(f.purgeBatches, append([]string(nil), subjects...))
	if f.purgeErr != nil {
		return f.purgeAttempted, f.purgeErr
	}
	f.purged = append(f.purged, subjects...)
	return true, nil
}

func (f *fakeEventBusAdmin) Close() error { f.closed = true; return nil }

func useFakeEventBusAdmin(t *testing.T, admin *fakeEventBusAdmin) {
	t.Helper()
	original := openEventBusAdmin
	t.Cleanup(func() { openEventBusAdmin = original })
	openEventBusAdmin = func(string, string) (eventBusAdmin, error) { return admin, nil }
}

// resetTestViews 是两个空间里各一个 View：重置时每个数据集一个消费者、每个数据集四类事件。
func resetTestViews() []resetViewRecord {
	return []resetViewRecord{
		{SpaceID: "crypto", ViewID: "va", PrimaryDatasetID: "ds_a"},
		{SpaceID: "stockcn", ViewID: "vb", PrimaryDatasetID: "ds_b"},
	}
}

func TestResetStorageViewConsumersDeletesConsumersThenPurgesEverySubject(t *testing.T) {
	admin := &fakeEventBusAdmin{}
	views := resetTestViews()
	opts := resetViewConsumersOptions{stream: events.StorageViewConsumerStream}

	mutated, err := resetStorageViewConsumers(t.Context(), admin, opts, views)
	require.NoError(t, err)
	require.True(t, mutated)
	require.Equal(t, resetConsumerDurables(views), admin.deleted)
	require.Len(t, admin.deleted, 2, "每个数据集一个 View 消费者")
	registry, err := events.DefaultRegistry()
	require.NoError(t, err)
	expected, err := resetEventSubjects(registry, views)
	require.NoError(t, err)
	require.Len(t, expected, 8, "两个数据集 × 四类事件")
	require.Equal(t, expected, admin.purged)
	require.Equal(t, 1, admin.purgeCalls, "所有主题一次提交")
}

func TestResetStorageViewConsumersReportsMutationOnlyWhenBrokerStateMayHaveChanged(t *testing.T) {
	views := resetTestViews()
	opts := resetViewConsumersOptions{stream: events.StorageViewConsumerStream}

	// Admin 明确说没发出过删除请求（例如预检之后 EventBus 暂时不可达）：什么都没动，调用方要恢复本地状态。
	admin := &fakeEventBusAdmin{deleteErr: errors.New("Admin DeleteEventBusConsumers 失败: 连接 EventBus 失败")}
	mutated, err := resetStorageViewConsumers(t.Context(), admin, opts, views)
	require.ErrorContains(t, err, "delete consumers")
	require.False(t, mutated, "确定没有改动 EventBus 时不能判为已改动，否则本地状态不会被恢复")
	require.Zero(t, admin.purgeCalls, "删除消费者失败后不再清除消息")

	// 请求发出后失败：可能已经删了一部分，旧事件无法再保证，必须保留重置状态。
	admin = &fakeEventBusAdmin{deleteErr: errors.New("timeout"), deleteAttempted: true}
	mutated, err = resetStorageViewConsumers(t.Context(), admin, opts, views)
	require.ErrorContains(t, err, "delete consumers")
	require.True(t, mutated)
	require.Zero(t, admin.purgeCalls)

	admin = &fakeEventBusAdmin{purgeErr: errors.New("timeout"), purgeAttempted: true}
	mutated, err = resetStorageViewConsumers(t.Context(), admin, opts, views)
	require.ErrorContains(t, err, "purge stream")
	require.True(t, mutated)

	// 消费者已经删掉后，即使清除请求没发出去，EventBus 也已被改动。
	admin = &fakeEventBusAdmin{purgeErr: errors.New("Admin PurgeEventBusSubjects 失败: 连接 EventBus 失败")}
	mutated, err = resetStorageViewConsumers(t.Context(), admin, opts, views)
	require.Error(t, err)
	require.True(t, mutated)

	// 没有任何 View：没有消费者要删、没有主题要清，什么都不会动。
	admin = &fakeEventBusAdmin{}
	mutated, err = resetStorageViewConsumers(t.Context(), admin, opts, nil)
	require.NoError(t, err)
	require.False(t, mutated)
	require.Empty(t, admin.deleted)
	require.Zero(t, admin.purgeCalls)
}

// Admin 一次最多接受 256 个主题。数据集多到主题超过上限时必须分批提交，
// 否则会在本地状态已经重置、消费者已经删除之后被拒绝，把 Storage 留在停止状态。
func TestResetStorageViewConsumersPurgesLargeTopologiesInBatches(t *testing.T) {
	const datasets = 70
	views := make([]resetViewRecord, 0, datasets)
	for i := 0; i < datasets; i++ {
		views = append(views, resetViewRecord{SpaceID: "crypto", ViewID: fmt.Sprintf("v_%03d", i), PrimaryDatasetID: fmt.Sprintf("ds_%03d", i)})
	}
	registry, err := events.DefaultRegistry()
	require.NoError(t, err)
	expected, err := resetEventSubjects(registry, views)
	require.NoError(t, err)
	require.Len(t, expected, datasets*len(events.StorageDatasetEvents))
	require.Greater(t, len(expected), 256, "夹具要超过 Admin 的单次上限才有意义")

	admin := &fakeEventBusAdmin{}
	opts := resetViewConsumersOptions{stream: events.StorageViewConsumerStream}
	require.NoError(t, preflightResetEventBus(t.Context(), admin, opts, views), "预检不能因为主题多而失败")
	mutated, err := resetStorageViewConsumers(t.Context(), admin, opts, views)
	require.NoError(t, err)
	require.True(t, mutated)
	require.Equal(t, expected, admin.purged, "所有主题都要清除且顺序不变")
	require.Equal(t, 5, admin.purgeCalls)
	for i, batch := range admin.purgeBatches {
		require.LessOrEqual(t, len(batch), purgeSubjectBatch, "第 %d 批过大", i)
	}

	// 中间一批失败：停止后续批次，并仍然算已改动。
	admin = &fakeEventBusAdmin{}
	failing := &failOnPurgeCall{fakeEventBusAdmin: admin, failAt: 2, failAttempted: true}
	mutated, err = resetStorageViewConsumers(t.Context(), failing, opts, views)
	require.ErrorContains(t, err, "purge stream")
	require.True(t, mutated)
	require.Equal(t, 2, admin.purgeCalls, "第二批失败后不再提交第三批")
}

// 前面的批次已经清除成功后，后一批即使 Admin 说“没发出请求”，整体也已经改动过 EventBus。
func TestPurgeSubjectsKeepsAttemptedFromEarlierBatches(t *testing.T) {
	subjects := make([]string, 3*purgeSubjectBatch)
	admin := &failOnPurgeCall{fakeEventBusAdmin: &fakeEventBusAdmin{}, failAt: 2, failAttempted: false}
	attempted, err := purgeSubjects(t.Context(), admin, events.StorageViewConsumerStream, subjects)
	require.Error(t, err)
	require.True(t, attempted)
	require.Equal(t, 2, admin.purgeCalls, "失败后不再提交后续批次")

	admin = &failOnPurgeCall{fakeEventBusAdmin: &fakeEventBusAdmin{}, failAt: 1, failAttempted: false}
	attempted, err = purgeSubjects(t.Context(), admin, events.StorageViewConsumerStream, subjects)
	require.Error(t, err)
	require.False(t, attempted, "第一批就确定没发出请求时没有改动")

	attempted, err = purgeSubjects(t.Context(), &fakeEventBusAdmin{}, events.StorageViewConsumerStream, nil)
	require.NoError(t, err)
	require.False(t, attempted, "没有主题时不发请求")
}

// failOnPurgeCall 让第 failAt 次清除调用失败，failAttempted 决定失败时是否算已尝试。
type failOnPurgeCall struct {
	*fakeEventBusAdmin
	failAt        int
	failAttempted bool
}

func (f *failOnPurgeCall) PurgeSubjects(ctx context.Context, stream string, subjects []string) (bool, error) {
	attempted, err := f.fakeEventBusAdmin.PurgeSubjects(ctx, stream, subjects)
	if err == nil && f.purgeCalls == f.failAt {
		return f.failAttempted, errors.New("boom")
	}
	return attempted, err
}

func TestPreflightResetEventBusChecksStreamThroughAdminWithoutMutating(t *testing.T) {
	admin := &fakeEventBusAdmin{}
	views := []resetViewRecord{{SpaceID: "stockcn", ViewID: "v", PrimaryDatasetID: "ds_b"}}
	opts := resetViewConsumersOptions{stream: events.StorageViewConsumerStream}
	require.NoError(t, preflightResetEventBus(t.Context(), admin, opts, views))
	require.True(t, admin.inspectCalled)
	require.Empty(t, admin.deleted)
	require.Zero(t, admin.purgeCalls)

	admin = &fakeEventBusAdmin{inspectErr: errors.New("Admin InspectEventBus 失败: EventBus 流 MOOX_STORAGE 不存在")}
	err := preflightResetEventBus(t.Context(), admin, opts, views)
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

func TestDeleteJetStreamConsumerOnlyAcceptsViewConsumers(t *testing.T) {
	admin := &fakeEventBusAdmin{}
	useFakeEventBusAdmin(t, admin)

	deleted, err := deleteJetStreamConsumer(t.Context(), "conf", "root", events.StorageViewConsumerStream, testViewDurableA)
	require.NoError(t, err)
	require.True(t, deleted)
	require.True(t, admin.closed)

	_, err = deleteJetStreamConsumer(t.Context(), "conf", "root", events.StorageViewConsumerStream, "factor_collector_period_v1")
	require.ErrorContains(t, err, "不支持的消费者")
	require.Equal(t, []string{testViewDurableA}, admin.deleted, "越界的消费者名不能发到 Admin")
}

func TestViewMaintenanceCommandsNoLongerAcceptAdminCredentialFlags(t *testing.T) {
	const undefined = "flag provided but not defined"
	for _, flagName := range []string{"--credential-file", "--eventbus-url"} {
		require.ErrorContains(t, runResetViewConsumers([]string{"--dry-run", flagName, "x"}, &bytes.Buffer{}, &bytes.Buffer{}), undefined, flagName)
		require.ErrorContains(t, runPurgeDatasetEvents(t.Context(), []string{"--space", "s", "--dataset", "d", "--dry-run", flagName, "x"}, &bytes.Buffer{}), undefined, flagName)
		require.ErrorContains(t, runForceRebuildView([]string{"--space-id", "s", "--view-id", "v", "--consumer", testViewDurableB, "--dry-run", flagName, "x"}, &bytes.Buffer{}, &bytes.Buffer{}), undefined, flagName)
		require.ErrorContains(t, runRepairView([]string{"--space-id", "s", "--view-id", "v", "--consumer", testViewDurableB, "--dry-run", flagName, "x"}, &bytes.Buffer{}, &bytes.Buffer{}), undefined, flagName)
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

	// 配置里没有网关身份（例如指到了 storage-primary 的配置）要明确说出缺什么、该读哪份。
	require.NoError(t, os.WriteFile(config, []byte("storage:\n  root: ./var\n"), 0o600))
	_, err = openGatewayEventBusAdmin(config, dir)
	require.ErrorContains(t, err, "gateway_client.key_id")
	require.ErrorContains(t, err, "storage-view/config/storage.yaml")
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
		consumer: testViewDurableA, deliverPolicy: "new", timeout: time.Minute, yes: true, resetConsumer: true, forceRebuild: true, restart: true,
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

	healthy := &fakeEventBusAdmin{inspect: map[string]viewConsumerFilterState{testViewDurableA: {Exists: true}}}
	useFakeEventBusAdmin(t, healthy)
	var stdout bytes.Buffer
	opts = repairFixture(t)
	require.NoError(t, runRepairViewOperation(t.Context(), opts, &stdout, &bytes.Buffer{}))
	require.Equal(t, []string{"stop", "start"}, lifecycle)
	require.Equal(t, []string{testViewDurableA}, healthy.deleted)
	require.Contains(t, stdout.String(), `"consumer_reset":true`)

	// 不删消费者的修复不需要 EventBus，也就不会去预检。
	lifecycle = nil
	useFakeEventBusAdmin(t, broken)
	opts = repairFixture(t)
	opts.resetConsumer = false
	require.NoError(t, runRepairViewOperation(t.Context(), opts, &bytes.Buffer{}, &bytes.Buffer{}))
	require.Equal(t, []string{"stop", "start"}, lifecycle)
}

// 测试用的两个 View 消费者名（view_<12 位十六进制>）。
const (
	testViewDurableA = "view_0123456789ab"
	testViewDurableB = "view_ba9876543210"
)

var testViewDurables = []string{testViewDurableA, testViewDurableB}
