<template>
  <div class="moox-page logical-page" :class="{ 'is-embedded': embedded }">
    <div class="moox-inner">
      <div class="page-head">
        <a-space>
          <a-button :loading="loading" aria-label="刷新策略账户" @click="load">
            <template #icon><icon-refresh /></template>
            刷新
          </a-button>
          <a-button type="primary" status="success" @click="createVisible = true">
            <template #icon><icon-plus /></template>
            新建策略账户
          </a-button>
        </a-space>
      </div>

      <div class="summary-grid">
        <div class="summary-card">
          <span>策略账户</span>
          <strong>{{ pagination.total }}</strong>
          <small>当前空间总数</small>
        </div>
        <div class="summary-card summary-card--green">
          <span>运行中</span>
          <strong>{{ stateCounts.active }}</strong>
          <small>自动执行已开启</small>
        </div>
        <div class="summary-card summary-card--orange">
          <span>已暂停</span>
          <strong>{{ stateCounts.paused }}</strong>
          <small>等待人工恢复</small>
        </div>
        <div class="summary-card summary-card--red">
          <span>未就绪</span>
          <strong>{{ stateCounts.notReady }}</strong>
          <small>存在阻断原因</small>
        </div>
      </div>

      <div class="table-toolbar">
        <a-space wrap>
          <span class="toolbar-label">筛选</span>
          <a-select v-model="stateFilter" allow-clear placeholder="自动执行状态" style="width: 150px">
            <a-option value="ACTIVE">运行中</a-option>
            <a-option value="PAUSED">已暂停</a-option>
          </a-select>
          <a-select v-model="executionFilter" allow-clear placeholder="执行模式" style="width: 130px">
            <a-option :value="1">模拟</a-option>
            <a-option :value="2">实盘</a-option>
          </a-select>
        </a-space>
        <span class="table-count">显示 {{ filteredRows.length }} 条</span>
      </div>

      <a-empty v-if="!loading && !filteredRows.length" description="暂无策略账户" />
      <a-table
        v-else
        row-key="logical_account_id"
        :data="filteredRows"
        :loading="loading"
        :pagination="pagination"
        :scroll="{ x: 1040 }"
        @page-change="changePage"
      >
        <template #columns>
          <a-table-column title="策略账户" :width="220">
            <template #cell="{ record }">
              <a-link @click="openDetail(record)">{{ record.name }}</a-link>
              <div class="muted">{{ record.logical_account_id }}</div>
            </template>
          </a-table-column>
          <a-table-column title="账户类型" :width="100">
            <template #cell="{ record }">{{ localMarketTypeLabels[record.market_type] }}</template>
          </a-table-column>
          <a-table-column title="执行模式" :width="100">
            <template #cell="{ record }">{{ localExecutionModeLabels[record.execution_mode] }}</template>
          </a-table-column>
          <a-table-column title="结算资产" data-index="settlement_asset" :width="110" />
          <a-table-column title="自动执行" :width="110">
            <template #cell="{ record }">
              <a-tag :color="record.automation_state === 'ACTIVE' ? 'green' : 'orange'">
                {{ automationStateLabel(record.automation_state) }}
              </a-tag>
            </template>
          </a-table-column>
          <a-table-column title="就绪状态" :width="110">
            <template #cell="{ record }">
              <a-tag :color="record.ready ? 'green' : 'red'">{{ record.ready ? "就绪" : "未就绪" }}</a-tag>
            </template>
          </a-table-column>
          <a-table-column title="执行器" data-index="owner_runner_id" :width="140" :ellipsis="true" :tooltip="true" />
          <a-table-column title="成员数" :width="80">
            <template #cell="{ record }">{{ record.members?.length || 0 }}</template>
          </a-table-column>
          <a-table-column title="操作" :width="90" fixed="right">
            <template #cell="{ record }">
              <a-button size="mini" type="text" @click="openDetail(record)">查看</a-button>
            </template>
          </a-table-column>
        </template>
      </a-table>

      <a-modal v-model:visible="createVisible" title="新建策略账户" @ok="create">
        <a-form :model="createForm" auto-label-width>
          <a-form-item label="名称" required><a-input v-model="createForm.name" /></a-form-item>
          <a-form-item label="账户类型" required>
            <a-radio-group v-model="createForm.market_type" type="button">
              <a-radio :value="1">现货</a-radio>
              <a-radio :value="2">合约</a-radio>
            </a-radio-group>
          </a-form-item>
          <a-form-item label="执行模式" required>
            <a-radio-group v-model="createForm.execution_mode" type="button">
              <a-radio :value="1">模拟</a-radio>
              <a-radio :value="2">实盘</a-radio>
            </a-radio-group>
          </a-form-item>
          <a-form-item label="结算资产" required><a-input v-model="createForm.settlement_asset" /></a-form-item>
        </a-form>
      </a-modal>

      <a-drawer v-model:visible="detailVisible" :width="860" title="策略账户详情" @cancel="closeDetail">
        <template v-if="selected">
          <div class="detail-head">
            <div>
              <h3>{{ selected.name }}</h3>
              <span>{{ selected.logical_account_id }}</span>
            </div>
            <a-space>
              <a-button v-if="selected.automation_state === 'ACTIVE'" status="warning" @click="requestPause"
                >暂停自动执行</a-button
              >
              <a-button v-else type="primary" status="success" :disabled="!selected.ready" @click="resume">恢复自动执行</a-button>
              <a-button status="danger" @click="openFlatten">逐账户清仓</a-button>
            </a-space>
          </div>
          <a-alert v-if="selected.pause_reason" type="warning" show-icon class="section">
            {{ selected.pause_reason }}
          </a-alert>
          <a-alert v-if="selected.readiness_reasons?.length" type="error" show-icon class="section">
            {{ selected.readiness_reasons.join("；") }}
          </a-alert>
          <a-alert v-if="selected.automation_state === 'PAUSED' && target" type="info" show-icon class="section">
            当前完整目标序号 {{ target.command_sequence }} 已保存但不会执行；恢复后会继续收敛。
          </a-alert>

          <a-descriptions :column="3" bordered class="section">
            <a-descriptions-item label="自动执行">{{ automationStateLabel(selected.automation_state) }}</a-descriptions-item>
            <a-descriptions-item label="就绪状态">{{ selected.ready ? "就绪" : "未就绪" }}</a-descriptions-item>
            <a-descriptions-item label="执行器">{{ selected.owner_runner_id || "-" }}</a-descriptions-item>
          </a-descriptions>

          <div class="section">
            <div class="section-title">
              <h3>资金曲线</h3>
              <span>结算资产：{{ selected.settlement_asset }}</span>
            </div>
            <equity-curve :logical-account-id="selected.logical_account_id" />
          </div>

          <div class="section-title">
            <h3>实际交易账户</h3>
            <a-button
              size="small"
              type="primary"
              status="success"
              :disabled="selected.automation_state !== 'PAUSED'"
              @click="openMember"
              ><template #icon><icon-plus /></template>添加成员</a-button
            >
          </div>
          <div class="member-hint">多个实际账户按优先级执行，当前账户容量不足时自动切换下一账户。</div>
          <a-table size="small" row-key="trading_account_id" :data="selected.members" :pagination="false">
            <template #columns>
              <a-table-column title="交易账户" data-index="trading_account_id" />
              <a-table-column title="启用" :width="80">
                <template #cell="{ record }">{{ record.enabled ? "是" : "否" }}</template>
              </a-table-column>
              <a-table-column title="优先级" data-index="priority" :width="90" />
              <a-table-column title="操作" :width="190">
                <template #cell="{ record }">
                  <a-space>
                    <a-button size="mini" type="text" @click="openManual(record.trading_account_id)">人工下单</a-button>
                    <a-popconfirm content="确认移除此成员？" @ok="removeMember(record.trading_account_id)">
                      <a-button size="mini" type="text" status="danger" :disabled="selected?.automation_state !== 'PAUSED'"
                        >移除</a-button
                      >
                    </a-popconfirm>
                  </a-space>
                </template>
              </a-table-column>
            </template>
          </a-table>

          <div class="section-title">
            <h3>当前目标</h3>
            <span>{{ targetStatusLabel(target?.status) }}</span>
          </div>
          <a-table size="small" row-key="instrument_id" :data="target?.targets || []" :pagination="false">
            <template #columns>
              <a-table-column title="交易标的" data-index="instrument_id" />
              <a-table-column title="目标持仓量" data-index="quantity" />
            </template>
          </a-table>
          <a-empty v-if="target && !target.targets.length" description="空目标：全部归零" />

          <template v-if="action">
            <div class="section-title">
              <h3>最近人工操作</h3>
              <span>{{ action.action_id }}</span>
            </div>
            <a-descriptions :column="2" bordered>
              <a-descriptions-item label="类型">{{ actionTypeLabel(action.action_type) }}</a-descriptions-item>
              <a-descriptions-item label="状态">{{ actionStateLabel(action.status) }}</a-descriptions-item>
              <a-descriptions-item label="原因">{{ action.reason }}</a-descriptions-item>
              <a-descriptions-item label="错误">{{ action.last_error || "-" }}</a-descriptions-item>
            </a-descriptions>
            <a-table
              v-if="flattenResult"
              size="small"
              row-key="trading_account_id"
              :data="flattenResult.accounts"
              :pagination="false"
              class="section"
            >
              <template #columns>
                <a-table-column title="交易账户" data-index="trading_account_id" />
                <a-table-column title="状态" :width="100">
                  <template #cell="{ record }">{{ flattenAccountStateLabel(record.status) }}</template>
                </a-table-column>
                <a-table-column title="剩余仓位">
                  <template #cell="{ record }">
                    <div v-for="item in record.remaining_positions || []" :key="`${item.instrument_id}-${item.asset}`">
                      {{ item.instrument_id || item.asset }}: {{ item.quantity }} ({{ item.reason }})
                    </div>
                    <span v-if="!record.remaining_positions?.length">无</span>
                  </template>
                </a-table-column>
                <a-table-column title="错误" data-index="error" />
              </template>
            </a-table>
          </template>
        </template>
      </a-drawer>

      <a-modal v-model:visible="reasonVisible" :title="reasonMode === 'pause' ? '暂停策略账户' : '逐账户清仓'" @ok="submitReason">
        <a-alert v-if="reasonMode === 'flatten'" type="warning" show-icon class="section">
          将分别清空每个实际账户的风险仓位，不进行跨账户净额；完成后保持暂停。
        </a-alert>
        <a-form :model="reasonForm" auto-label-width>
          <a-form-item label="操作编号" v-if="reasonMode === 'flatten'" required>
            <a-input v-model="reasonForm.action_id" />
          </a-form-item>
          <a-form-item label="原因" required><a-input v-model="reasonForm.reason" /></a-form-item>
        </a-form>
      </a-modal>

      <a-modal v-model:visible="memberVisible" title="添加实际交易账户" @ok="addMember">
        <a-form :model="memberForm" auto-label-width>
          <a-form-item label="交易账户" required>
            <a-select v-model="memberForm.trading_account_id" allow-search>
              <a-option v-for="item in eligibleAccounts" :key="item.trading_account_id" :value="item.trading_account_id">
                {{ item.name }} ({{ item.trading_account_id }})
              </a-option>
            </a-select>
          </a-form-item>
          <a-form-item label="优先级"><a-input-number v-model="memberForm.priority" :min="0" /></a-form-item>
          <a-form-item label="启用"><a-switch v-model="memberForm.enabled" /></a-form-item>
          <a-form-item label="接管已有敞口"><a-checkbox v-model="memberForm.adopt_existing_exposure" /></a-form-item>
        </a-form>
      </a-modal>

      <a-modal v-model:visible="manualVisible" title="人工下单" @ok="submitManual">
        <a-alert v-if="capabilities && !capabilities.can_place_order" type="error" show-icon class="section">
          {{ capabilities.unavailable_reason || "当前账户不可下单" }}
        </a-alert>
        <a-alert type="warning" show-icon class="section">提交前会暂停整个策略账户并取消活动目标订单。</a-alert>
        <a-form :model="manualForm" auto-label-width>
          <a-form-item label="操作编号" required><a-input v-model="manualForm.action_id" /></a-form-item>
          <a-form-item label="客户端订单编号" required><a-input v-model="manualForm.client_order_id" /></a-form-item>
          <a-form-item label="交易标的" required
            ><a-input v-model="manualForm.instrument_id" placeholder="BTC-USDT-SPOT"
          /></a-form-item>
          <a-form-item label="方向" required>
            <a-radio-group v-model="manualForm.side" type="button">
              <a-radio :value="1">买入</a-radio><a-radio :value="2">卖出</a-radio>
            </a-radio-group>
          </a-form-item>
          <a-form-item label="订单类型">
            <a-radio-group v-model="manualForm.order_type" type="button">
              <a-radio v-if="capabilities?.order_types?.includes(1)" :value="1">市价</a-radio>
              <a-radio v-if="capabilities?.order_types?.includes(2)" :value="2">限价</a-radio>
            </a-radio-group>
          </a-form-item>
          <a-form-item v-if="manualForm.order_type === 2" label="成交策略">
            <a-select v-model="manualForm.fill_policy">
              <a-option v-for="policy in capabilities?.fill_policies || [1, 2, 3]" :key="policy" :value="policy">{{
                ["", "一直有效（GTC）", "立即成交（IOC）", "全部成交（FOK）"][policy]
              }}</a-option>
            </a-select>
          </a-form-item>
          <a-form-item label="数量" required><a-input v-model="manualForm.quantity" /></a-form-item>
          <a-form-item v-if="manualForm.order_type === 2" label="限价" required
            ><a-input v-model="manualForm.limit_price"
          /></a-form-item>
          <a-form-item label="原因" required><a-input v-model="manualForm.reason" /></a-form-item>
        </a-form>
      </a-modal>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onUnmounted, reactive, ref, watch } from "vue";
import { Message, Modal } from "@arco-design/web-vue";
import { useRoute, useRouter } from "vue-router";
import { createClientId } from "@/utils/client-id";
import {
  addLogicalAccountMember,
  createLogicalAccount,
  getExecutionCapabilities,
  flattenLogicalAccount,
  getLogicalAccount,
  getLogicalAccountTarget,
  getOperatorAction,
  listTradingAccounts,
  listLogicalAccounts,
  parseFlattenResult,
  pauseLogicalAccount,
  placeManualOrder,
  removeLogicalAccountMember,
  resumeLogicalAccount,
  logicalAutomationStateLabels,
  targetStateLabels,
  actionStateLabels
} from "@/api/trade";
import type {
  TradingAccount,
  LogicalAccount,
  LogicalAccountTarget,
  OperatorAction,
  ExecutionCapabilities,
  PlaceManualOrderReq
} from "@/api/trade";
import EquityCurve from "./equity-curve.vue";
import { createLatestRequestGuard } from "@/utils/latest-request";

defineOptions({ name: "trading-logical-accounts" });
withDefaults(defineProps<{ embedded?: boolean }>(), { embedded: false });
const rows = ref<LogicalAccount[]>([]);
const accounts = ref<TradingAccount[]>([]);
const selected = ref<LogicalAccount | null>(null);
const target = ref<LogicalAccountTarget | null>(null);
const action = ref<OperatorAction | null>(null);
const capabilities = ref<ExecutionCapabilities | null>(null);
const loading = ref(false);
const createVisible = ref(false);
const detailVisible = ref(false);
const reasonVisible = ref(false);
const reasonMode = ref<"pause" | "flatten">("pause");
const memberVisible = ref(false);
const manualVisible = ref(false);
const actionPoller = ref<ReturnType<typeof setInterval> | null>(null);
const route = useRoute();
const router = useRouter();
const pagination = reactive({ current: 1, pageSize: 20, total: 0 });
const stateFilter = ref("");
const executionFilter = ref<number | undefined>(undefined);
const routeRequests = createLatestRequestGuard();
const detailRequests = createLatestRequestGuard();
const actionPollRequests = createLatestRequestGuard();
const createForm = reactive({ name: "", execution_mode: 1 as 1 | 2, market_type: 1 as 1 | 2, settlement_asset: "USDT" });
const reasonForm = reactive({ action_id: "", reason: "" });
const memberForm = reactive({ trading_account_id: "", enabled: true, priority: 0, adopt_existing_exposure: false });
const manualForm = reactive({
  action_id: "",
  trading_account_id: "",
  client_order_id: "",
  instrument_id: "",
  order_type: 1 as 1 | 2,
  fill_policy: 0 as 0 | 1 | 2 | 3,
  side: 1 as 1 | 2,
  position_side: 1 as const,
  quantity: "",
  limit_price: "",
  reason: ""
});
const localMarketTypeLabels: Record<number, string> = { 0: "-", 1: "现货", 2: "合约" };
const localExecutionModeLabels: Record<number, string> = { 0: "-", 1: "模拟", 2: "实盘" };
const flattenResult = computed(() => parseFlattenResult(action.value || undefined));
const filteredRows = computed(() =>
  rows.value.filter(item => {
    const matchesState = !stateFilter.value || item.automation_state === stateFilter.value;
    const matchesExecution = executionFilter.value === undefined || item.execution_mode === executionFilter.value;
    return matchesState && matchesExecution;
  })
);
const stateCounts = computed(() => ({
  active: rows.value.filter(item => item.automation_state === "ACTIVE").length,
  paused: rows.value.filter(item => item.automation_state === "PAUSED").length,
  notReady: rows.value.filter(item => !item.ready).length
}));
function targetStatusLabel(status?: string) {
  return status ? targetStateLabels[status.toUpperCase()] || "未知" : "暂无";
}
function actionTypeLabel(type?: string) {
  const labels: Record<string, string> = { FLATTEN: "逐账户清仓", MANUAL_ORDER: "人工下单", PAUSE: "暂停", RESUME: "恢复" };
  return type ? labels[type.toUpperCase()] || "未知" : "-";
}
function actionStateLabel(status?: string) {
  return status ? actionStateLabels[status.toUpperCase()] || "未知" : "-";
}
function flattenAccountStateLabel(status?: string) {
  if (!status) return "-";
  const labels: Record<string, string> = { ...actionStateLabels, PARTIAL: "部分完成" };
  return labels[status.toUpperCase()] || "未知";
}
function automationStateLabel(status?: string) {
  return status ? logicalAutomationStateLabels[status.toUpperCase()] || "未知" : "-";
}
const eligibleAccounts = computed(() =>
  accounts.value.filter(
    item =>
      selected.value &&
      item.execution_mode === selected.value.execution_mode &&
      item.market_type === selected.value.market_type &&
      item.settlement_asset === selected.value.settlement_asset &&
      !selected.value.members.some(member => member.trading_account_id === item.trading_account_id)
  )
);

async function load() {
  loading.value = true;
  try {
    const rsp = await listLogicalAccounts({ page: pagination.current, size: pagination.pageSize });
    rows.value = rsp.logical_accounts || [];
    pagination.total = rsp.page_result?.total || 0;
    await openRouteDetail();
  } finally {
    loading.value = false;
  }
}
function changePage(page: number) {
  pagination.current = page;
  load();
}
async function create() {
  if (!createForm.name.trim() || !createForm.settlement_asset.trim()) {
    Message.warning("请填写所有必填字段");
    return false;
  }
  await createLogicalAccount({
    ...createForm,
    name: createForm.name.trim(),
    settlement_asset: createForm.settlement_asset.trim().toUpperCase()
  });
  createVisible.value = false;
  await load();
  return true;
}
async function openDetail(item: LogicalAccount) {
  detailRequests.invalidate();
  selected.value = item;
  target.value = null;
  action.value = null;
  capabilities.value = null;
  detailVisible.value = true;
  await reloadDetail();
}
function closeDetail() {
  routeRequests.invalidate();
  detailRequests.invalidate();
  detailVisible.value = false;
  stopActionPolling();
  target.value = null;
  action.value = null;
  capabilities.value = null;
  void router.replace({ query: { ...route.query, logical_account_id: undefined } });
}
async function reloadDetail() {
  if (!selected.value) return;
  const request = detailRequests.begin();
  const id = selected.value.logical_account_id;
  const [accountRsp, targetRsp] = await Promise.all([
    getLogicalAccount(id),
    getLogicalAccountTarget(id).catch(() => ({ target: undefined }))
  ]);
  if (!request.isLatest() || selected.value?.logical_account_id !== id) return;
  selected.value = accountRsp.logical_account;
  target.value = targetRsp.target || null;
}

async function openRouteDetail() {
  const requestedId = typeof route.query.logical_account_id === "string" ? route.query.logical_account_id : "";
  const request = routeRequests.begin();
  if (!requestedId) {
    if (detailVisible.value) {
      detailRequests.invalidate();
      detailVisible.value = false;
      stopActionPolling();
      selected.value = null;
      target.value = null;
      action.value = null;
      capabilities.value = null;
    }
    return;
  }
  try {
    const requested = await getLogicalAccount(requestedId);
    if (!request.isLatest() || route.query.logical_account_id !== requestedId) return;
    await openDetail(requested.logical_account);
  } catch {
    if (!request.isLatest() || route.query.logical_account_id !== requestedId) return;
    await router.replace({ query: { ...route.query, logical_account_id: undefined } });
    Message.warning("策略账户不存在或无权限");
  }
}

watch(
  () => route.query.logical_account_id,
  () => {
    void openRouteDetail();
  }
);
function requestPause() {
  reasonMode.value = "pause";
  reasonForm.reason = "";
  reasonVisible.value = true;
}
function openFlatten() {
  reasonMode.value = "flatten";
  reasonForm.action_id = createClientId();
  reasonForm.reason = "";
  reasonVisible.value = true;
}
async function submitReason() {
  if (!selected.value || !reasonForm.reason.trim() || (reasonMode.value === "flatten" && !reasonForm.action_id.trim())) {
    Message.warning("请填写原因和操作编号");
    return false;
  }
  if (reasonMode.value === "pause") {
    await pauseLogicalAccount(selected.value.logical_account_id, reasonForm.reason.trim());
  } else {
    const rsp = await flattenLogicalAccount(
      reasonForm.action_id.trim(),
      selected.value.logical_account_id,
      reasonForm.reason.trim()
    );
    action.value = rsp.action;
    startActionPolling(rsp.action.action_id);
  }
  reasonVisible.value = false;
  await reloadDetail();
  await load();
  return true;
}
async function resume() {
  if (!selected.value) return;
  Modal.confirm({
    title: "恢复自动执行",
    content: "恢复后会按当前完整目标继续收敛，人工清仓的仓位可能重新建立。",
    onOk: async () => {
      const rsp = await resumeLogicalAccount(selected.value!.logical_account_id);
      if (rsp.warning) Message.warning(rsp.warning);
      await reloadDetail();
      await load();
    }
  });
}
async function openMember() {
  const rsp = await listTradingAccounts({ page: { page: 1, size: 200 } });
  accounts.value = rsp.accounts || [];
  Object.assign(memberForm, { trading_account_id: "", enabled: true, priority: 0, adopt_existing_exposure: false });
  memberVisible.value = true;
}
async function addMember() {
  if (!selected.value || !memberForm.trading_account_id) {
    Message.warning("请选择交易账户");
    return false;
  }
  await addLogicalAccountMember({ logical_account_id: selected.value.logical_account_id, ...memberForm });
  memberVisible.value = false;
  await reloadDetail();
  await load();
  return true;
}
async function removeMember(exchangeAccountId: string) {
  if (!selected.value) return;
  await removeLogicalAccountMember(selected.value.logical_account_id, exchangeAccountId);
  await reloadDetail();
  await load();
}
function openManual(exchangeAccountId: string) {
  Object.assign(manualForm, {
    action_id: createClientId(),
    trading_account_id: exchangeAccountId,
    client_order_id: createClientId().replace(/-/g, ""),
    instrument_id: "",
    order_type: 1,
    fill_policy: 0,
    side: 1,
    position_side: 1,
    quantity: "",
    limit_price: "",
    reason: ""
  });
  capabilities.value = null;
  getExecutionCapabilities(exchangeAccountId)
    .then(response => {
      capabilities.value = response.capabilities;
    })
    .catch(() => {
      capabilities.value = null;
    });
  manualVisible.value = true;
}
async function submitManual() {
  if (
    !manualForm.action_id.trim() ||
    !manualForm.client_order_id.trim() ||
    !manualForm.instrument_id.trim() ||
    !manualForm.quantity.trim() ||
    !manualForm.reason.trim() ||
    (manualForm.order_type === 2 && !manualForm.limit_price.trim())
  ) {
    Message.warning("请填写所有必填字段");
    return false;
  }
  if (capabilities.value && !capabilities.value.can_place_order) {
    Message.warning(capabilities.value.unavailable_reason || "当前账户不可下单");
    return false;
  }
  const request: PlaceManualOrderReq = { ...manualForm };
  if (selected.value?.market_type === 1) delete request.position_side;
  const rsp = await placeManualOrder({
    ...request,
    action_id: manualForm.action_id.trim(),
    client_order_id: manualForm.client_order_id.trim(),
    instrument_id: manualForm.instrument_id.trim().toUpperCase(),
    quantity: manualForm.quantity.trim(),
    fill_policy: manualForm.order_type === 1 ? 0 : manualForm.fill_policy,
    limit_price: manualForm.order_type === 2 ? manualForm.limit_price.trim() : undefined,
    reason: manualForm.reason.trim()
  });
  action.value = rsp.action;
  manualVisible.value = false;
  startActionPolling(rsp.action.action_id);
  await reloadDetail();
  await load();
  return true;
}
function startActionPolling(actionId: string) {
  stopActionPolling();
  if (action.value?.status !== "RUNNING") return;
  const request = actionPollRequests.begin();
  const logicalAccountId = selected.value?.logical_account_id;
  actionPoller.value = setInterval(async () => {
    const rsp = await getOperatorAction(actionId);
    if (!request.isLatest() || selected.value?.logical_account_id !== logicalAccountId) return;
    action.value = rsp.action;
    if (rsp.action.status !== "RUNNING") stopActionPolling();
  }, 1500);
}
function stopActionPolling() {
  actionPollRequests.invalidate();
  if (actionPoller.value) clearInterval(actionPoller.value);
  actionPoller.value = null;
}
onUnmounted(stopActionPolling);
load();
</script>

<style scoped>
.page-head,
.detail-head,
.section-title {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 16px;
}
.page-head {
  margin-bottom: var(--moox-space-4);
}
.page-head h2,
.detail-head h3,
.section-title h3 {
  margin: 0 0 4px;
}
.page-head span,
.detail-head span,
.section-title span,
.muted {
  color: var(--color-text-3);
  font-size: 12px;
}
.section,
.section-title {
  margin-top: 18px;
}
.summary-grid {
  display: grid;
  grid-template-columns: repeat(4, minmax(0, 1fr));
  gap: 12px;
  margin-bottom: var(--moox-space-4);
}
.summary-card {
  display: grid;
  gap: 4px;
  min-height: 92px;
  padding: 14px 16px;
  border: 1px solid var(--color-border-2);
  border-radius: 8px;
  background: var(--color-bg-2);
}
.summary-card span,
.summary-card small {
  color: var(--color-text-3);
  font-size: 12px;
}
.summary-card strong {
  color: var(--color-text-1);
  font-size: 24px;
  line-height: 1;
}
.summary-card--green {
  border-color: rgb(var(--green-3));
  background: rgb(var(--green-1));
}
.summary-card--orange {
  border-color: rgb(var(--orange-3));
  background: rgb(var(--orange-1));
}
.summary-card--red {
  border-color: rgb(var(--red-3));
  background: rgb(var(--red-1));
}
.table-toolbar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 16px;
  margin-bottom: var(--moox-space-3);
}
.toolbar-label,
.table-count {
  color: var(--color-text-3);
  font-size: 12px;
}
.table-count {
  white-space: nowrap;
}
.logical-page :deep(.arco-table-container) {
  border-radius: 8px;
}
.logical-page :deep(.arco-table-th) {
  white-space: nowrap;
}
.member-hint {
  margin-top: 8px;
  color: var(--color-text-3);
  font-size: 12px;
}
@media (max-width: 760px) {
  .summary-grid {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }
  .table-toolbar {
    align-items: flex-start;
    flex-direction: column;
  }
  .page-head {
    align-items: flex-start;
    flex-direction: column;
  }
}
</style>
