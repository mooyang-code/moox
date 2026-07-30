<template>
  <div class="moox-page">
    <div class="moox-inner">
      <div class="page-head">
        <div>
          <h2>Logical Account</h2>
          <span>同质 Exchange 账户组、自动执行状态与人工操作。</span>
        </div>
        <a-space>
          <a-button :loading="loading" @click="load"
            ><template #icon><icon-refresh /></template>刷新</a-button
          >
          <a-button type="primary" status="success" @click="createVisible = true"
            ><template #icon><icon-plus /></template>新增</a-button
          >
        </a-space>
      </div>
      <a-table row-key="logical_account_id" :data="rows" :loading="loading" :pagination="pagination" @page-change="changePage">
        <template #columns>
          <a-table-column title="Logical Account">
            <template #cell="{ record }">
              <a-link @click="openDetail(record)">{{ record.name }}</a-link>
              <div class="muted">{{ record.logical_account_id }}</div>
            </template>
          </a-table-column>
          <a-table-column title="账户类型" :width="110">
            <template #cell="{ record }">{{ marketTypeLabels[record.market_type] }}</template>
          </a-table-column>
          <a-table-column title="执行模式" :width="110">
            <template #cell="{ record }">{{ executionModeLabels[record.execution_mode] }}</template>
          </a-table-column>
          <a-table-column title="结算资产" data-index="settlement_asset" :width="110" />
          <a-table-column title="自动执行" :width="120">
            <template #cell="{ record }">
              <a-tag :color="record.automation_state === 'ACTIVE' ? 'green' : 'orange'">{{ record.automation_state }}</a-tag>
            </template>
          </a-table-column>
          <a-table-column title="Readiness" :width="120">
            <template #cell="{ record }">
              <a-tag :color="record.ready ? 'green' : 'red'">{{ record.ready ? "Ready" : "Not Ready" }}</a-tag>
            </template>
          </a-table-column>
          <a-table-column title="Owner Runner" data-index="owner_runner_id" :ellipsis="true" :tooltip="true" />
          <a-table-column title="成员" :width="80">
            <template #cell="{ record }">{{ record.members?.length || 0 }}</template>
          </a-table-column>
          <a-table-column title="操作" :width="100">
            <template #cell="{ record }"><a-button size="mini" type="text" @click="openDetail(record)">管理</a-button></template>
          </a-table-column>
        </template>
      </a-table>

      <a-modal v-model:visible="createVisible" title="新增 Logical Account" @ok="create">
        <a-form :model="createForm" auto-label-width>
          <a-form-item label="名称" required><a-input v-model="createForm.name" /></a-form-item>
          <a-form-item label="账户类型" required>
            <a-radio-group v-model="createForm.market_type" type="button">
              <a-radio :value="1">SPOT</a-radio>
              <a-radio :value="2">SWAP</a-radio>
            </a-radio-group>
          </a-form-item>
          <a-form-item label="执行模式" required>
            <a-radio-group v-model="createForm.execution_mode" type="button">
              <a-radio :value="1">Paper</a-radio>
              <a-radio :value="2">Live</a-radio>
            </a-radio-group>
          </a-form-item>
          <a-form-item label="结算资产" required><a-input v-model="createForm.settlement_asset" /></a-form-item>
        </a-form>
      </a-modal>

      <a-drawer v-model:visible="detailVisible" :width="860" title="Logical Account 管理" @cancel="stopActionPolling">
        <template v-if="selected">
          <div class="detail-head">
            <div>
              <h3>{{ selected.name }}</h3>
              <span>{{ selected.logical_account_id }}</span>
            </div>
            <a-space>
              <a-button v-if="selected.automation_state === 'ACTIVE'" status="warning" @click="requestPause">暂停</a-button>
              <a-button v-else type="primary" status="success" :disabled="!selected.ready" @click="resume">恢复</a-button>
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
            当前 FULL 目标 sequence {{ target.command_sequence }} 已保存但不会执行；恢复后会继续收敛。
          </a-alert>

          <a-descriptions :column="3" bordered class="section">
            <a-descriptions-item label="状态">{{ selected.automation_state }}</a-descriptions-item>
            <a-descriptions-item label="Readiness">{{ selected.ready ? "Ready" : "Not Ready" }}</a-descriptions-item>
            <a-descriptions-item label="Owner">{{ selected.owner_runner_id || "-" }}</a-descriptions-item>
          </a-descriptions>

          <div class="section-title">
            <h3>物理账户</h3>
            <a-button
              size="small"
              type="primary"
              status="success"
              :disabled="selected.automation_state !== 'PAUSED'"
              @click="openMember"
              ><template #icon><icon-plus /></template>添加成员</a-button
            >
          </div>
          <a-table size="small" row-key="exchange_account_id" :data="selected.members" :pagination="false">
            <template #columns>
              <a-table-column title="Exchange Account" data-index="exchange_account_id" />
              <a-table-column title="启用" :width="80">
                <template #cell="{ record }">{{ record.enabled ? "是" : "否" }}</template>
              </a-table-column>
              <a-table-column title="优先级" data-index="priority" :width="90" />
              <a-table-column title="操作" :width="190">
                <template #cell="{ record }">
                  <a-space>
                    <a-button size="mini" type="text" @click="openManual(record.exchange_account_id)">人工下单</a-button>
                    <a-popconfirm content="确认移除此成员？" @ok="removeMember(record.exchange_account_id)">
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
            <h3>当前完整目标</h3>
            <span>{{ target?.status || "暂无" }}</span>
          </div>
          <a-table size="small" row-key="instrument_id" :data="target?.targets || []" :pagination="false">
            <template #columns>
              <a-table-column title="Instrument" data-index="instrument_id" />
              <a-table-column title="绝对目标持仓量" data-index="quantity" />
            </template>
          </a-table>
          <a-empty v-if="target && !target.targets.length" description="空 FULL 目标：全部归零" />

          <template v-if="action">
            <div class="section-title">
              <h3>最近人工操作</h3>
              <span>{{ action.action_id }}</span>
            </div>
            <a-descriptions :column="2" bordered>
              <a-descriptions-item label="类型">{{ action.action_type }}</a-descriptions-item>
              <a-descriptions-item label="状态">{{ action.status }}</a-descriptions-item>
              <a-descriptions-item label="原因">{{ action.reason }}</a-descriptions-item>
              <a-descriptions-item label="错误">{{ action.last_error || "-" }}</a-descriptions-item>
            </a-descriptions>
            <a-table
              v-if="flattenResult"
              size="small"
              row-key="exchange_account_id"
              :data="flattenResult.accounts"
              :pagination="false"
              class="section"
            >
              <template #columns>
                <a-table-column title="Exchange Account" data-index="exchange_account_id" />
                <a-table-column title="状态" data-index="status" :width="100" />
                <a-table-column title="剩余仓位">
                  <template #cell="{ record }">
                    <div v-for="item in record.remaining_positions || []" :key="`${item.symbol}-${item.asset}`">
                      {{ item.symbol || item.asset }}: {{ item.quantity }} ({{ item.reason }})
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

      <a-modal
        v-model:visible="reasonVisible"
        :title="reasonMode === 'pause' ? '暂停 Logical Account' : '逐账户清仓'"
        @ok="submitReason"
      >
        <a-alert v-if="reasonMode === 'flatten'" type="warning" show-icon class="section">
          将分别清空每个物理账户的风险仓位，不进行跨账户净额；完成后保持 PAUSED。
        </a-alert>
        <a-form :model="reasonForm" auto-label-width>
          <a-form-item label="Action ID" v-if="reasonMode === 'flatten'" required>
            <a-input v-model="reasonForm.action_id" />
          </a-form-item>
          <a-form-item label="原因" required><a-input v-model="reasonForm.reason" /></a-form-item>
        </a-form>
      </a-modal>

      <a-modal v-model:visible="memberVisible" title="添加物理账户" @ok="addMember">
        <a-form :model="memberForm" auto-label-width>
          <a-form-item label="Exchange Account" required>
            <a-select v-model="memberForm.exchange_account_id" allow-search>
              <a-option v-for="item in eligibleAccounts" :key="item.exchange_account_id" :value="item.exchange_account_id">
                {{ item.name }} ({{ item.exchange_account_id }})
              </a-option>
            </a-select>
          </a-form-item>
          <a-form-item label="优先级"><a-input-number v-model="memberForm.priority" :min="0" /></a-form-item>
          <a-form-item label="启用"><a-switch v-model="memberForm.enabled" /></a-form-item>
          <a-form-item label="接管已有敞口"><a-checkbox v-model="memberForm.adopt_existing_exposure" /></a-form-item>
        </a-form>
      </a-modal>

      <a-modal v-model:visible="manualVisible" title="人工下单" @ok="submitManual">
        <a-alert type="warning" show-icon class="section">提交前会暂停整个 Logical Account 并取消活动 TARGET 订单。</a-alert>
        <a-form :model="manualForm" auto-label-width>
          <a-form-item label="Action ID" required><a-input v-model="manualForm.action_id" /></a-form-item>
          <a-form-item label="Client Order ID" required><a-input v-model="manualForm.client_order_id" /></a-form-item>
          <a-form-item label="Instrument" required
            ><a-input v-model="manualForm.symbol" placeholder="BTC-USDT-SPOT"
          /></a-form-item>
          <a-form-item label="方向" required>
            <a-radio-group v-model="manualForm.side" type="button">
              <a-radio :value="1">买入</a-radio><a-radio :value="2">卖出</a-radio>
            </a-radio-group>
          </a-form-item>
          <a-form-item label="订单类型">
            <a-radio-group v-model="manualForm.order_type" type="button">
              <a-radio :value="1">MARKET</a-radio><a-radio :value="2">LIMIT</a-radio>
            </a-radio-group>
          </a-form-item>
          <a-form-item v-if="manualForm.order_type === 2" label="成交策略">
            <a-select v-model="manualForm.fill_policy">
              <a-option :value="1">GTC</a-option><a-option :value="2">IOC</a-option><a-option :value="3">FOK</a-option>
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
import { computed, onUnmounted, reactive, ref } from "vue";
import { Message, Modal } from "@arco-design/web-vue";
import { createClientId } from "@/utils/client-id";
import {
  addLogicalAccountMember,
  createLogicalAccount,
  executionModeLabels,
  flattenLogicalAccount,
  getLogicalAccount,
  getLogicalAccountTarget,
  getOperatorAction,
  listAccounts,
  listLogicalAccounts,
  marketTypeLabels,
  parseFlattenResult,
  pauseLogicalAccount,
  placeManualOrder,
  removeLogicalAccountMember,
  resumeLogicalAccount
} from "@/api/trade";
import type { ExchangeAccount, LogicalAccount, LogicalAccountTarget, OperatorAction } from "@/api/trade";

defineOptions({ name: "trading-logical-accounts" });
const rows = ref<LogicalAccount[]>([]);
const accounts = ref<ExchangeAccount[]>([]);
const selected = ref<LogicalAccount | null>(null);
const target = ref<LogicalAccountTarget | null>(null);
const action = ref<OperatorAction | null>(null);
const loading = ref(false);
const createVisible = ref(false);
const detailVisible = ref(false);
const reasonVisible = ref(false);
const reasonMode = ref<"pause" | "flatten">("pause");
const memberVisible = ref(false);
const manualVisible = ref(false);
const actionPoller = ref<ReturnType<typeof setInterval> | null>(null);
const pagination = reactive({ current: 1, pageSize: 20, total: 0 });
const createForm = reactive({ name: "", execution_mode: 1 as 1 | 2, market_type: 1 as 1 | 2, settlement_asset: "USDT" });
const reasonForm = reactive({ action_id: "", reason: "" });
const memberForm = reactive({ exchange_account_id: "", enabled: true, priority: 0, adopt_existing_exposure: false });
const manualForm = reactive({
  action_id: "",
  exchange_account_id: "",
  client_order_id: "",
  symbol: "",
  order_type: 1 as 1 | 2,
  fill_policy: 0 as 0 | 1 | 2 | 3,
  side: 1 as 1 | 2,
  position_side: 1 as const,
  quantity: "",
  limit_price: "",
  reason: ""
});
const flattenResult = computed(() => parseFlattenResult(action.value || undefined));
const eligibleAccounts = computed(() =>
  accounts.value.filter(
    item =>
      selected.value &&
      item.execution_mode === selected.value.execution_mode &&
      item.market_type === selected.value.market_type &&
      item.settlement_asset === selected.value.settlement_asset &&
      !selected.value.members.some(member => member.exchange_account_id === item.exchange_account_id)
  )
);

async function load() {
  loading.value = true;
  try {
    const rsp = await listLogicalAccounts({ page: pagination.current, size: pagination.pageSize });
    rows.value = rsp.logical_accounts || [];
    pagination.total = rsp.page_result?.total || 0;
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
  selected.value = item;
  detailVisible.value = true;
  await reloadDetail();
}
async function reloadDetail() {
  if (!selected.value) return;
  const id = selected.value.logical_account_id;
  const [accountRsp, targetRsp] = await Promise.all([
    getLogicalAccount(id),
    getLogicalAccountTarget(id).catch(() => ({ target: undefined }))
  ]);
  selected.value = accountRsp.logical_account;
  target.value = targetRsp.target || null;
}
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
    Message.warning("请填写原因和 Action ID");
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
  const rsp = await listAccounts({ page: { page: 1, size: 200 } });
  accounts.value = rsp.accounts || [];
  Object.assign(memberForm, { exchange_account_id: "", enabled: true, priority: 0, adopt_existing_exposure: false });
  memberVisible.value = true;
}
async function addMember() {
  if (!selected.value || !memberForm.exchange_account_id) {
    Message.warning("请选择 Exchange Account");
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
    exchange_account_id: exchangeAccountId,
    client_order_id: createClientId().replace(/-/g, ""),
    symbol: "",
    order_type: 1,
    fill_policy: 0,
    side: 1,
    position_side: 1,
    quantity: "",
    limit_price: "",
    reason: ""
  });
  manualVisible.value = true;
}
async function submitManual() {
  if (
    !manualForm.action_id.trim() ||
    !manualForm.client_order_id.trim() ||
    !manualForm.symbol.trim() ||
    !manualForm.quantity.trim() ||
    !manualForm.reason.trim() ||
    (manualForm.order_type === 2 && !manualForm.limit_price.trim())
  ) {
    Message.warning("请填写所有必填字段");
    return false;
  }
  const rsp = await placeManualOrder({
    ...manualForm,
    action_id: manualForm.action_id.trim(),
    client_order_id: manualForm.client_order_id.trim(),
    symbol: manualForm.symbol.trim().toUpperCase(),
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
  actionPoller.value = setInterval(async () => {
    const rsp = await getOperatorAction(actionId);
    action.value = rsp.action;
    if (rsp.action.status !== "RUNNING") stopActionPolling();
  }, 1500);
}
function stopActionPolling() {
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
  margin-bottom: var(--moox-space-2);
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
</style>
