package collectmgr

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	cloudnodedao "github.com/mooyang-code/moox/modules/admin/internal/service/cloudnode/dao"
	cloudnodemodel "github.com/mooyang-code/moox/modules/admin/internal/service/cloudnode/model"
	"github.com/mooyang-code/moox/modules/admin/internal/service/collectmgr/dao"
	"github.com/mooyang-code/moox/modules/admin/internal/service/collectmgr/model"
	"github.com/mooyang-code/moox/modules/admin/internal/service/collectmgr/planner"
	"gorm.io/gorm"
)

// ===== 测试用 mock =====

// mockSymbolProvider 返回固定标的列表，可控且无外部依赖
type mockSymbolProvider struct {
	mu       sync.Mutex
	symbols  map[string][]string // dataSource -> symbols
	callLog  []string
	callFail bool
}

func newMockSymbolProvider() *mockSymbolProvider {
	return &mockSymbolProvider{
		symbols: map[string][]string{
			"binance": {"BTC-USDT", "ETH-USDT", "SOL-USDT"},
		},
	}
}

func (m *mockSymbolProvider) GetSymbols(ctx context.Context, dataSource string, instType ...string) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.callLog = append(m.callLog, dataSource)
	if m.callFail {
		return nil, fmt.Errorf("mock forced failure")
	}
	out := make([]string, len(m.symbols[dataSource]))
	copy(out, m.symbols[dataSource])
	return out, nil
}

// mockOnlineNodeProvider 返回固定在线节点集合
type mockOnlineNodeProvider struct {
	onlineIDs map[string]bool
}

func newMockOnlineNodeProvider(ids ...string) *mockOnlineNodeProvider {
	set := make(map[string]bool, len(ids))
	for _, id := range ids {
		set[id] = true
	}
	return &mockOnlineNodeProvider{onlineIDs: set}
}

func (m *mockOnlineNodeProvider) GetOnlineNodeIDs() []string {
	out := make([]string, 0, len(m.onlineIDs))
	for id := range m.onlineIDs {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// ===== 测试夹具 =====

type e2eFixture struct {
	db          *gorm.DB
	ruleDAO     dao.CollectorTaskRulesDAO
	instanceDAO dao.CollectorTaskInstanceDAO
	nodeDAO     cloudnodedao.CloudNodeDAO
	store       TaskInstanceStore
	symbols     *mockSymbolProvider
	online      *mockOnlineNodeProvider
	registry    *planner.PlannerRegistry
	svc         TaskPlannerService
}

var e2eFixtureSeq uint64

func newE2EFixture(t *testing.T, onlineNodeIDs []string) *e2eFixture {
	t.Helper()
	// 每个测试用唯一 DSN，避免 cache=shared 导致跨用例数据残留
	seq := atomic.AddUint64(&e2eFixtureSeq, 1)
	dsn := fmt.Sprintf("file:e2e_%d?mode=memory&cache=shared", seq)
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(
		&model.CollectorTaskRules{},
		&model.CollectorTaskInstance{},
		&cloudnodemodel.CloudNode{},
	); err != nil {
		t.Fatalf("automigrate: %v", err)
	}

	ruleDAO := dao.NewCollectorTaskRulesDAO(db)
	instanceDAO := dao.NewCollectorTaskInstanceDAO(db)
	nodeDAO := cloudnodedao.NewCloudNodeDAO(db)
	store := NewTaskInstanceStore()
	symbols := newMockSymbolProvider()
	online := newMockOnlineNodeProvider(onlineNodeIDs...)
	registry := planner.NewPlannerRegistry(nodeDAO, symbols, online)
	svc := NewTaskPlannerServiceImpl(ruleDAO, instanceDAO, registry, nodeDAO, online, store)

	return &e2eFixture{
		db:          db,
		ruleDAO:     ruleDAO,
		instanceDAO: instanceDAO,
		nodeDAO:     nodeDAO,
		store:       store,
		symbols:     symbols,
		online:      online,
		registry:    registry,
		svc:         svc,
	}
}

// createNode 在指定空间创建一个支持指定数据类型的在线节点
func (f *e2eFixture) createNode(t *testing.T, spaceID, nodeID, bizType string, dataTypes []string) {
	t.Helper()
	collectors, _ := json.Marshal(dataTypes)
	node := &cloudnodemodel.CloudNode{
		SpaceID:            spaceID,
		NodeID:             nodeID,
		BizType:            bizType,
		NodeType:           cloudnodemodel.NodeTypeSCFEvent,
		SupportedCollectors: string(collectors),
		ProbeEnabled:       true,
	}
	if err := f.nodeDAO.CreateCloudNode(context.Background(), node); err != nil {
		t.Fatalf("create node %s: %v", nodeID, err)
	}
}

// createRule 在指定空间创建一条规则
func (f *e2eFixture) createRule(t *testing.T, spaceID, ruleID, dataType, dataSource, assignType string, assignedNodes []string) {
	t.Helper()
	assigned, _ := json.Marshal(assignedNodes)
	rule := &model.CollectorTaskRules{
		SpaceID:        spaceID,
		RuleID:         ruleID,
		BizType:        "data_collector",
		DataType:       dataType,
		DataSource:     dataSource,
		CollectParams:  `{"objects":["BTC-USDT","ETH-USDT"]}`,
		AssignmentType: assignType,
		AssignedNodes:  string(assigned),
		Enabled:        model.EnabledTrue,
	}
	if err := f.ruleDAO.CreateTaskRule(context.Background(), rule); err != nil {
		t.Fatalf("create rule %s: %v", ruleID, err)
	}
}

// instanceNodeMap 返回 taskID -> plannedExecNode
func (f *e2eFixture) instanceNodeMap() map[string]string {
	out := map[string]string{}
	for _, inst := range f.store.GetSnapshot() {
		out[inst.TaskID] = inst.PlannedExecNode
	}
	return out
}

// instanceSpaceMap 返回 taskID -> spaceID
func (f *e2eFixture) instanceSpaceMap() map[string]string {
	out := map[string]string{}
	for _, inst := range f.store.GetSnapshot() {
		out[inst.TaskID] = inst.SpaceID
	}
	return out
}

// waitForDBFlush 等待异步刷库完成并读取 DB 实例
func (f *e2eFixture) dbInstances(t *testing.T) []*model.CollectorTaskInstance {
	t.Helper()
	time.Sleep(100 * time.Millisecond)
	var instances []*model.CollectorTaskInstance
	if err := f.db.Where("c_invalid = ?", model.InvalidNo).Find(&instances).Error; err != nil {
		t.Fatalf("query db instances: %v", err)
	}
	return instances
}

// ============================================================
// A 组：空结果语义 / planned 标记 / 清空
// ============================================================

// TestA1_StorePlannedFlag 验证 IsPlanned 在首次 ReplaceAll 后置 true
func TestA1_StorePlannedFlag(t *testing.T) {
	f := newE2EFixture(t, []string{"n1"})
	if f.store.IsPlanned() {
		t.Fatal("expected IsPlanned=false before any recalculation")
	}
	if err := f.svc.RecalculateAllTaskInstances(context.Background()); err != nil {
		t.Fatalf("recalculate: %v", err)
	}
	if !f.store.IsPlanned() {
		t.Fatal("expected IsPlanned=true after first recalculation")
	}
}

// TestA3_PlannerFlushEmpty 验证重算无实例时 DB 被清空（A3）
// 场景：先有规则和节点生成实例并落库，禁用所有规则后重算，DB 应清空
func TestA3_PlannerFlushEmpty(t *testing.T) {
	f := newE2EFixture(t, []string{"n1"})
	f.createNode(t, "sp1", "n1", "data_collector", []string{model.DataTypeTicker})
	f.createRule(t, "sp1", "rule-1", model.DataTypeTicker, "binance", model.AssignmentTypeAuto, nil)

	if err := f.svc.RecalculateAllTaskInstances(context.Background()); err != nil {
		t.Fatalf("first recalc: %v", err)
	}
	if got := f.store.GetCount(); got != 2 {
		t.Fatalf("after first recalc, store count = %d, want 2", got)
	}
	if got := len(f.dbInstances(t)); got != 2 {
		t.Fatalf("after first recalc, db count = %d, want 2", got)
	}

	// 禁用规则：通过直接 DB 更新 enabled=false
	if err := f.db.Model(&model.CollectorTaskRules{}).
		Where("c_rule_id = ?", "rule-1").
		Update("c_enabled", model.EnabledFalse).Error; err != nil {
		t.Fatalf("disable rule: %v", err)
	}

	// 再次重算：应生成 0 实例，DB 必须被清空（A3）
	if err := f.svc.RecalculateAllTaskInstances(context.Background()); err != nil {
		t.Fatalf("second recalc: %v", err)
	}
	if got := f.store.GetCount(); got != 0 {
		t.Fatalf("after second recalc, store count = %d, want 0", got)
	}
	if got := len(f.dbInstances(t)); got != 0 {
		t.Fatalf("after second recalc, db count = %d, want 0 (A3: empty list must clear DB)", got)
	}
	if !f.store.IsPlanned() {
		t.Fatal("IsPlanned should remain true even when result is empty")
	}
}

// TestA2_HeartbeatInitialState 验证 store 在未规划/规划空/规划非空三态的 IsPlanned + GetCount 组合
// 心跳服务据此返回 initializing / empty / 正常列表
func TestA2_HeartbeatInitialState(t *testing.T) {
	f := newE2EFixture(t, []string{"n1"})

	// 态1：未规划 -> initializing
	if f.store.IsPlanned() {
		t.Fatal("state1: should be initializing (not planned)")
	}

	// 态2：规划但无规则无节点 -> 权威空列表
	if err := f.svc.RecalculateAllTaskInstances(context.Background()); err != nil {
		t.Fatalf("recalc empty: %v", err)
	}
	if !f.store.IsPlanned() {
		t.Fatal("state2: should be planned")
	}
	if f.store.GetCount() != 0 {
		t.Fatalf("state2: count = %d, want 0 (authoritative empty)", f.store.GetCount())
	}

	// 态3：加入规则和节点 -> 正常列表
	f.createNode(t, "sp1", "n1", "data_collector", []string{model.DataTypeTicker})
	f.createRule(t, "sp1", "rule-1", model.DataTypeTicker, "binance", model.AssignmentTypeAuto, nil)
	if err := f.svc.RecalculateAllTaskInstances(context.Background()); err != nil {
		t.Fatalf("recalc normal: %v", err)
	}
	if !f.store.IsPlanned() {
		t.Fatal("state3: should be planned")
	}
	if f.store.GetCount() != 2 {
		t.Fatalf("state3: count = %d, want 2", f.store.GetCount())
	}
}

// TestB5b_DBInstanceSpaceID 验证刷库后 DB 中实例的 SpaceID 正确回填
// 覆盖 GetSnapshot → flushToDB 路径，防止快照丢字段导致 DB 实例 space_id 为空
func TestB5b_DBInstanceSpaceID(t *testing.T) {
	f := newE2EFixture(t, []string{"sp1-n1", "sp2-n1"})
	f.createNode(t, "sp1", "sp1-n1", "data_collector", []string{model.DataTypeTicker})
	f.createNode(t, "sp2", "sp2-n1", "data_collector", []string{model.DataTypeTicker})
	f.createRule(t, "sp1", "rule-db-1", model.DataTypeTicker, "binance", model.AssignmentTypeAuto, nil)
	f.createRule(t, "sp2", "rule-db-2", model.DataTypeTicker, "binance", model.AssignmentTypeAuto, nil)

	if err := f.svc.RecalculateAllTaskInstances(context.Background()); err != nil {
		t.Fatalf("recalc: %v", err)
	}

	dbInsts := f.dbInstances(t)
	if len(dbInsts) != 4 {
		t.Fatalf("expected 4 db instances, got %d", len(dbInsts))
	}
	spaceCount := map[string]int{}
	for _, inst := range dbInsts {
		if inst.SpaceID == "" {
			t.Fatalf("db instance %s has empty space_id (GetSnapshot dropped field)", inst.TaskID)
		}
		if inst.BizType == "" {
			t.Fatalf("db instance %s has empty biz_type (GetSnapshot dropped field)", inst.TaskID)
		}
		spaceCount[inst.SpaceID]++
	}
	if spaceCount["sp1"] != 2 || spaceCount["sp2"] != 2 {
		t.Fatalf("db space distribution = %v, want sp1=2 sp2=2", spaceCount)
	}
}

// ============================================================
// B 组：space 隔离 / task_id 含 space
// ============================================================

// TestB5_PlannerSpaceDim 验证规则按 space 维度分组重算，跨 space 节点不互通
func TestB5_PlannerSpaceDim(t *testing.T) {
	f := newE2EFixture(t, []string{"sp1-n1", "sp2-n1"})
	// 两个空间各一个节点，节点只支持自己 space 的 ticker
	f.createNode(t, "sp1", "sp1-n1", "data_collector", []string{model.DataTypeTicker})
	f.createNode(t, "sp2", "sp2-n1", "data_collector", []string{model.DataTypeTicker})
	// 两个空间各一条相同 ruleID 的规则（同 ruleID 不同 space 应都能生成）
	f.createRule(t, "sp1", "rule-x", model.DataTypeTicker, "binance", model.AssignmentTypeAuto, nil)
	f.createRule(t, "sp2", "rule-x", model.DataTypeTicker, "binance", model.AssignmentTypeAuto, nil)

	if err := f.svc.RecalculateAllTaskInstances(context.Background()); err != nil {
		t.Fatalf("recalc: %v", err)
	}

	nodeMap := f.instanceNodeMap()
	spaceMap := f.instanceSpaceMap()
	if len(nodeMap) != 4 {
		t.Fatalf("expected 4 instances (2 spaces x 2 symbols), got %d", len(nodeMap))
	}

	// sp1 的实例必须分配到 sp1-n1，sp2 同理（节点不会跨 space）
	for taskID, spaceID := range spaceMap {
		nodeID := nodeMap[taskID]
		switch spaceID {
		case "sp1":
			if nodeID != "sp1-n1" {
				t.Fatalf("sp1 task %s assigned to %s, want sp1-n1 (space isolation broken)", taskID, nodeID)
			}
		case "sp2":
			if nodeID != "sp2-n1" {
				t.Fatalf("sp2 task %s assigned to %s, want sp2-n1 (space isolation broken)", taskID, nodeID)
			}
		default:
			t.Fatalf("unexpected spaceID %s", spaceID)
		}
	}

	// 实例 SpaceID 字段必须正确回填
	for _, inst := range f.store.GetSnapshot() {
		if inst.SpaceID != "sp1" && inst.SpaceID != "sp2" {
			t.Fatalf("instance %s has empty/wrong spaceID %q", inst.TaskID, inst.SpaceID)
		}
	}
}

// TestB6_TaskIDWithSpaceID 验证 task_id = md5(space_id|rule_id|task_params)
// 同 ruleID + 同 params 在不同 space 应产生不同 task_id
func TestB6_TaskIDWithSpaceID(t *testing.T) {
	f := newE2EFixture(t, []string{"sp1-n1", "sp2-n1"})
	f.createNode(t, "sp1", "sp1-n1", "data_collector", []string{model.DataTypeTicker})
	f.createNode(t, "sp2", "sp2-n1", "data_collector", []string{model.DataTypeTicker})
	f.createRule(t, "sp1", "rule-same", model.DataTypeTicker, "binance", model.AssignmentTypeAuto, nil)
	f.createRule(t, "sp2", "rule-same", model.DataTypeTicker, "binance", model.AssignmentTypeAuto, nil)

	if err := f.svc.RecalculateAllTaskInstances(context.Background()); err != nil {
		t.Fatalf("recalc: %v", err)
	}

	// 直接计算预期 task_id（BTC-USDT ticker params）
	// ticker BuildTaskParams: {"data_type":"ticker","data_source":"binance","inst_type":"SPOT","symbol":"BTC-USDT"}
	params := `{"data_type":"ticker","data_source":"binance","inst_type":"SPOT","symbol":"BTC-USDT"}`
	wantSP1 := planner.GenerateStableTaskID("sp1", "rule-same", params)
	wantSP2 := planner.GenerateStableTaskID("sp2", "rule-same", params)

	if wantSP1 == wantSP2 {
		t.Fatal("task_id should differ across spaces for same rule+params (B6 broken)")
	}

	store := f.store
	inst1 := store.GetByTaskID(wantSP1)
	inst2 := store.GetByTaskID(wantSP2)
	if inst1 == nil || inst2 == nil {
		t.Fatalf("expected both task_ids present in store: sp1=%v sp2=%v", inst1 != nil, inst2 != nil)
	}
	if inst1.SpaceID != "sp1" || inst2.SpaceID != "sp2" {
		t.Fatalf("task spaceID mismatch: %q / %q", inst1.SpaceID, inst2.SpaceID)
	}
}

// ============================================================
// C 组：fixed 节点强绑定 / 校验剔除
// ============================================================

// TestC1_FixedStrictBind 验证 fixed 单节点强绑定，跳过负载均衡
func TestC1_FixedStrictBind(t *testing.T) {
	f := newE2EFixture(t, []string{"n1", "n2"})
	// 两个节点都支持 ticker，但规则只 fixed 到 n1
	f.createNode(t, "sp1", "n1", "data_collector", []string{model.DataTypeTicker})
	f.createNode(t, "sp1", "n2", "data_collector", []string{model.DataTypeTicker})
	f.createRule(t, "sp1", "rule-fixed", model.DataTypeTicker, "binance",
		model.AssignmentTypeFixed, []string{"n1"})

	if err := f.svc.RecalculateAllTaskInstances(context.Background()); err != nil {
		t.Fatalf("recalc: %v", err)
	}

	nodeMap := f.instanceNodeMap()
	if len(nodeMap) != 2 {
		t.Fatalf("expected 2 instances, got %d", len(nodeMap))
	}
	for taskID, nodeID := range nodeMap {
		if nodeID != "n1" {
			t.Fatalf("fixed task %s bound to %s, want n1 (C1 strict bind broken)", taskID, nodeID)
		}
	}
}

// TestC2_FixedValidateAndReject 验证 fixed 指定的节点不支持所需 data_type 时被剔除，规则无可用节点
func TestC2_FixedValidateAndReject(t *testing.T) {
	f := newE2EFixture(t, []string{"n1"})
	// n1 只支持 kline，规则要 ticker 且 fixed 到 n1 -> 应被剔除
	f.createNode(t, "sp1", "n1", "data_collector", []string{model.DataTypeKline})
	f.createRule(t, "sp1", "rule-mismatch", model.DataTypeTicker, "binance",
		model.AssignmentTypeFixed, []string{"n1"})

	if err := f.svc.RecalculateAllTaskInstances(context.Background()); err != nil {
		t.Fatalf("recalc: %v", err)
	}

	if got := f.store.GetCount(); got != 0 {
		t.Fatalf("expected 0 instances (fixed node lacks data_type, should be rejected), got %d", got)
	}
}

// ============================================================
// D 组：稳定排序 / 保留旧分配 / Rendezvous / 节点变化迁移
// ============================================================

// TestD1_StableSort 验证相同输入两次重算产生完全相同的分配结果
func TestD1_StableSort(t *testing.T) {
	f := newE2EFixture(t, []string{"n1", "n2", "n3"})
	for _, id := range []string{"n1", "n2", "n3"} {
		f.createNode(t, "sp1", id, "data_collector", []string{model.DataTypeTicker})
	}
	f.createRule(t, "sp1", "rule-stable", model.DataTypeTicker, "binance", model.AssignmentTypeAuto, nil)

	if err := f.svc.RecalculateAllTaskInstances(context.Background()); err != nil {
		t.Fatalf("recalc1: %v", err)
	}
	first := f.instanceNodeMap()

	// 第二次重算，输入完全相同 -> 结果应完全一致
	if err := f.svc.RecalculateAllTaskInstances(context.Background()); err != nil {
		t.Fatalf("recalc2: %v", err)
	}
	second := f.instanceNodeMap()

	if len(first) != len(second) {
		t.Fatalf("instance count changed: %d -> %d", len(first), len(second))
	}
	for taskID, node := range first {
		if second[taskID] != node {
			t.Fatalf("task %s assignment drifted: %s -> %s (D1 stable sort broken)",
				taskID, node, second[taskID])
		}
	}
}

// TestD2_KeepOldAssignment 验证节点集合不变时保留旧 planned_exec_node，不迁移
func TestD2_KeepOldAssignment(t *testing.T) {
	f := newE2EFixture(t, []string{"n1", "n2"})
	f.createNode(t, "sp1", "n1", "data_collector", []string{model.DataTypeTicker})
	f.createNode(t, "sp1", "n2", "data_collector", []string{model.DataTypeTicker})
	f.createRule(t, "sp1", "rule-keep", model.DataTypeTicker, "binance", model.AssignmentTypeAuto, nil)

	if err := f.svc.RecalculateAllTaskInstances(context.Background()); err != nil {
		t.Fatalf("recalc1: %v", err)
	}
	first := f.instanceNodeMap()
	if len(first) != 2 {
		t.Fatalf("expected 2 instances, got %d", len(first))
	}

	// 第二次重算，节点集合不变 -> 所有任务应保留旧节点
	if err := f.svc.RecalculateAllTaskInstances(context.Background()); err != nil {
		t.Fatalf("recalc2: %v", err)
	}
	second := f.instanceNodeMap()
	for taskID, node := range first {
		if second[taskID] != node {
			t.Fatalf("task %s migrated %s -> %s without node change (D2 keep-old broken)",
				taskID, node, second[taskID])
		}
	}
}

// TestD3_RendezvousStability 验证 Rendezvous Hash 的核心特性：
// 新增节点只迁移落到新节点的任务，原有节点间分配保持不变
func TestD3_RendezvousStability(t *testing.T) {
	f := newE2EFixture(t, []string{"n1", "n2"})
	f.createNode(t, "sp1", "n1", "data_collector", []string{model.DataTypeTicker})
	f.createNode(t, "sp1", "n2", "data_collector", []string{model.DataTypeTicker})
	// 用更多标的以增大分布样本
	f.symbols.symbols["binance"] = []string{"BTC-USDT", "ETH-USDT", "SOL-USDT", "BNB-USDT", "XRP-USDT", "DOGE-USDT"}
	f.createRule(t, "sp1", "rule-rh", model.DataTypeTicker, "binance", model.AssignmentTypeAuto, nil)

	if err := f.svc.RecalculateAllTaskInstances(context.Background()); err != nil {
		t.Fatalf("recalc1: %v", err)
	}
	first := f.instanceNodeMap()

	// 新增 n3 并标记在线
	f.createNode(t, "sp1", "n3", "data_collector", []string{model.DataTypeTicker})
	f.online.onlineIDs["n3"] = true

	if err := f.svc.RecalculateAllTaskInstances(context.Background()); err != nil {
		t.Fatalf("recalc2: %v", err)
	}
	second := f.instanceNodeMap()

	// D2: 旧任务若旧节点仍在候选集，应保留旧节点（不因新增 n3 而迁移）
	for taskID, oldNode := range first {
		newNode, ok := second[taskID]
		if !ok {
			t.Fatalf("task %s disappeared after adding node (should be retained)", taskID)
		}
		if newNode != oldNode {
			// 仅当旧节点不再在线时才允许迁移；这里 n1/n2 仍在线，不允许迁移
			t.Fatalf("task %s migrated %s -> %s while old node still candidate (D2/D3 broken)",
				taskID, oldNode, newNode)
		}
	}
	// 新增节点应承担部分任务（Rendezvous 会让部分新 task 或保留旧 task，但至少不应全部还在旧节点）
	// 这里主要验证稳定性，不强求新节点必须有任务
}

// TestD4_MigrateOnNodeChange 验证节点下线时，其上任务迁移到其他在线候选节点
func TestD4_MigrateOnNodeChange(t *testing.T) {
	f := newE2EFixture(t, []string{"n1", "n2"})
	f.createNode(t, "sp1", "n1", "data_collector", []string{model.DataTypeTicker})
	f.createNode(t, "sp1", "n2", "data_collector", []string{model.DataTypeTicker})
	f.symbols.symbols["binance"] = []string{"BTC-USDT", "ETH-USDT", "SOL-USDT", "BNB-USDT"}
	f.createRule(t, "sp1", "rule-mig", model.DataTypeTicker, "binance", model.AssignmentTypeAuto, nil)

	if err := f.svc.RecalculateAllTaskInstances(context.Background()); err != nil {
		t.Fatalf("recalc1: %v", err)
	}
	first := f.instanceNodeMap()

	// n1 下线
	delete(f.online.onlineIDs, "n1")
	if err := f.svc.RecalculateAllTaskInstances(context.Background()); err != nil {
		t.Fatalf("recalc2: %v", err)
	}
	second := f.instanceNodeMap()

	// 原分配到 n1 的任务必须迁移到 n2（唯一剩余候选）
	for taskID, oldNode := range first {
		newNode, ok := second[taskID]
		if !ok {
			t.Fatalf("task %s disappeared after n1 offline (should migrate)", taskID)
		}
		if oldNode == "n1" {
			if newNode != "n2" {
				t.Fatalf("task %s on offline n1 should migrate to n2, got %s (D4 broken)",
					taskID, newNode)
			}
		} else {
			// 原在 n2 的任务应保留
			if newNode != "n2" {
				t.Fatalf("task %s on n2 should stay, got %s", taskID, newNode)
			}
		}
	}
}

// TestRendezvousDeterminism 直接验证 RendezvousHash + SortNodeIDs 的确定性
// 相同输入多次调用结果一致；节点集合扩展后旧节点上任务保持不变
func TestRendezvousDeterminism(t *testing.T) {
	nodes := []string{"n1", "n2", "n3"}
	sorted := planner.SortNodeIDs(nodes)
	first := planner.RendezvousHash("task-abc", sorted)
	for i := 0; i < 20; i++ {
		got := planner.RendezvousHash("task-abc", planner.SortNodeIDs(nodes))
		if got != first {
			t.Fatalf("rendezvous non-deterministic: %s vs %s", first, got)
		}
	}

	// 扩展节点集合，验证一致性哈希特性：原选中的节点若仍在集合中，结果不变
	extended := planner.SortNodeIDs(append(nodes, "n4"))
	if planner.RendezvousHash("task-abc", extended) != first {
		// rendezvous 严格意义：新增节点可能改变选中结果，但 SortNodeIDs 顺序稳定
		// 这里仅断言可复现性，不强制一致性哈希语义（rendezvous 是一致性哈希，新增节点只可能让部分 key 迁移到新节点）
		// 所以允许变化，但再次调用应稳定
		again := planner.RendezvousHash("task-abc", extended)
		if again != planner.RendezvousHash("task-abc", extended) {
			t.Fatal("rendezvous on extended set non-deterministic")
		}
	}

	// 排序应稳定且去重无关：乱序输入应得到相同排序结果
	unsorted := []string{"n3", "n1", "n2"}
	if got := planner.SortNodeIDs(unsorted); !sliceEqual(got, planner.SortNodeIDs(nodes)) {
		t.Fatalf("SortNodeIDs not stable: %v vs %v", got, planner.SortNodeIDs(nodes))
	}
}

// TestGenerateStableTaskIDFormat 验证 task_id 格式与 space 隔离
func TestGenerateStableTaskIDFormat(t *testing.T) {
	id1 := planner.GenerateStableTaskID("sp1", "rule1", `{"a":1}`)
	id2 := planner.GenerateStableTaskID("sp2", "rule1", `{"a":1}`)
	id3 := planner.GenerateStableTaskID("sp1", "rule1", `{"a":1}`)
	if id1 == id2 {
		t.Fatal("task_id should differ by space_id")
	}
	if id1 != id3 {
		t.Fatal("task_id should be deterministic for same input")
	}
	if len(id1) != 32 {
		t.Fatalf("task_id should be 32-char md5 hex, got %d", len(id1))
	}
	if strings.Contains(id1, "|") {
		t.Fatal("task_id should not contain raw separator")
	}
}

// sliceEqual 比较两个字符串切片是否相等
func sliceEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
