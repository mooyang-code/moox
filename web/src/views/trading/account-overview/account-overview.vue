<template>
  <div class="moox-page">
    <div class="moox-inner">
      <div class="page-head">
        <h2>交易账户</h2>
        <a-space>
          <a-button :loading="loading" aria-label="刷新交易账户" @click="loadAccounts">
            <template #icon><icon-refresh /></template>
            刷新
          </a-button>
          <a-button type="primary" status="success" @click="openCreate">
            <template #icon><icon-plus /></template>
            创建账户
          </a-button>
        </a-space>
      </div>

      <a-alert v-if="createdAccountId" type="success" show-icon class="result-alert">
        {{ createdLogicalAccountId ? "Paper 账户已创建" : "Live 账户已创建" }}：TradingAccount {{ createdAccountId }}
        <template v-if="createdLogicalAccountId">
          ；LogicalAccount {{ createdLogicalAccountId }}
          <a-button type="text" size="small" @click="goLogicalAccount">查看 LogicalAccount</a-button>
        </template>
        <span v-else-if="syncResult && syncResultAccountId === createdAccountId">
          ；首次同步：{{ syncResult.ready ? "Ready" : "Not Ready" }}
          <span v-if="syncResult.warnings.length">（{{ syncResult.warnings.join("；") }}）</span>
        </span>
      </a-alert>

      <a-table
        row-key="trading_account_id"
        :data="accounts"
        :loading="loading"
        :pagination="pagination"
        :scroll="{ x: 1320 }"
        @page-change="changePage"
      >
        <template #columns>
          <a-table-column title="账户" :width="220">
            <template #cell="{ record }">
              <strong>{{ record.name }}</strong>
              <div class="muted account-id">{{ record.trading_account_id }}</div>
            </template>
          </a-table-column>
          <a-table-column title="Exchange / 市场" :width="150">
            <template #cell="{ record }">
              {{ exchangeLabels[record.exchange] || "-" }} · {{ marketTypeLabels[record.market_type] || "-" }}
            </template>
          </a-table-column>
          <a-table-column title="执行配置" :width="170">
            <template #cell="{ record }">
              <div>{{ executionModeLabels[record.execution_mode] || "Unknown" }}</div>
              <div class="muted">{{ accountEnvironmentView(record) }}</div>
            </template>
          </a-table-column>
          <a-table-column title="资金" :width="190">
            <template #cell="{ record }">
              <div>Equity {{ snapshotValue(record.snapshot?.equity) }}</div>
              <div class="muted">Available {{ snapshotValue(record.snapshot?.available_funds) }}</div>
            </template>
          </a-table-column>
          <a-table-column title="状态" :width="150">
            <template #cell="{ record }">
              <a-space>
                <a-tag :color="accountStatusView(record.status, record.ready).color">
                  {{ accountStatusView(record.status, record.ready).label }}
                </a-tag>
                <span class="muted">{{ record.status || "Unknown" }}</span>
              </a-space>
            </template>
          </a-table-column>
          <a-table-column title="最近同步" :width="160">
            <template #cell="{ record }">{{ formatTimestamp(record.last_sync_at) }}</template>
          </a-table-column>
          <a-table-column title="错误" :width="220" ellipsis>
            <template #cell="{ record }">{{ record.last_error || "-" }}</template>
          </a-table-column>
          <a-table-column title="操作" fixed="right" :width="240">
            <template #cell="{ record }">
              <a-space>
                <a-button size="mini" @click="openDetail(record)">详情</a-button>
                <a-button size="mini" :loading="syncingId === record.trading_account_id" @click="sync(record)">
                  <template #icon><icon-sync /></template>
                  同步
                </a-button>
                <a-button
                  v-if="record.execution_mode === 1"
                  size="mini"
                  status="danger"
                  :disabled="
                    closingId === record.trading_account_id ||
                    !capabilitiesByAccount[record.trading_account_id]?.can_close_paper_simulation
                  "
                  @click="requestClosePaper(record)"
                >
                  关闭 Paper
                </a-button>
              </a-space>
            </template>
          </a-table-column>
        </template>
      </a-table>

      <a-modal
        v-model:visible="createVisible"
        :title="form.execution_mode === 1 ? '创建 Paper 模拟' : '创建 Live 账户'"
        :ok-text="form.execution_mode === 2 && form.environment === 2 ? '创建 Production 账户' : '创建账户'"
        @ok="create"
      >
        <a-form :model="form" auto-label-width>
          <a-form-item field="name" label="账户名称" required>
            <a-input v-model="form.name" name="account_name" autocomplete="off" data-test="account-name" />
          </a-form-item>
          <a-form-item field="exchange" label="Exchange" required>
            <a-select v-model="form.exchange" name="exchange">
              <a-option :value="1">Binance</a-option>
              <a-option :value="2">OKX</a-option>
            </a-select>
          </a-form-item>
          <a-form-item field="market_type" label="账户类型" required>
            <a-radio-group v-model="form.market_type" type="button">
              <a-radio :value="1">SPOT</a-radio>
              <a-radio :value="2">SWAP</a-radio>
            </a-radio-group>
          </a-form-item>
          <a-form-item field="execution_mode" label="执行模式" required>
            <a-radio-group v-model="form.execution_mode" type="button" data-test="execution-mode">
              <a-radio :value="1">Paper</a-radio>
              <a-radio :value="2">Live</a-radio>
            </a-radio-group>
          </a-form-item>
          <template v-if="form.execution_mode === 2">
            <a-form-item field="environment" label="环境" required>
              <a-radio-group v-model="form.environment" type="button">
                <a-radio :value="1">Testnet</a-radio>
                <a-radio :value="2">Production</a-radio>
              </a-radio-group>
            </a-form-item>
            <a-form-item field="credential_secret_id" label="Live Secret ID" required>
              <a-input v-model="form.credential_secret_id" name="credential_secret_id" autocomplete="off" />
            </a-form-item>
            <a-form-item v-if="form.market_type === 1" field="sync_symbols" label="交易标的" required>
              <a-input v-model="form.sync_symbols" name="sync_symbols" data-test="sync-symbols" placeholder="BTCUSDT, ETHUSDT" />
            </a-form-item>
          </template>
          <a-form-item field="settlement_asset" label="结算资产" required>
            <a-input v-model="form.settlement_asset" :disabled="form.market_type === 2" name="settlement_asset" />
          </a-form-item>
          <a-form-item v-if="form.market_type === 2" field="margin_mode" label="保证金模式">
            <a-input model-value="CROSS" disabled />
          </a-form-item>
          <template v-if="form.execution_mode === 1">
            <a-form-item field="initial_balance" label="初始资金" required>
              <a-input v-model="form.initial_balance" name="initial_balance" autocomplete="off" />
            </a-form-item>
            <a-form-item field="maker_fee_rate" label="Maker 费率" required
              ><a-input v-model="form.maker_fee_rate"
            /></a-form-item>
            <a-form-item field="taker_fee_rate" label="Taker 费率" required
              ><a-input v-model="form.taker_fee_rate"
            /></a-form-item>
            <a-form-item field="slippage_bps" label="滑点 (bps)" required><a-input v-model="form.slippage_bps" /></a-form-item>
            <a-form-item field="logical_account_name" label="LogicalAccount 名称" required>
              <a-input v-model="form.logical_account_name" name="logical_account_name" />
            </a-form-item>
          </template>
          <a-alert v-if="formErrors" type="error" show-icon>{{ formErrors }}</a-alert>
          <a-alert v-if="form.execution_mode === 2 && form.environment === 2" type="warning" show-icon class="form-warning">
            Production 账户创建后可能具备真实交易能力，请确认 Secret ID 和环境配置正确。
          </a-alert>
        </a-form>
      </a-modal>

      <a-drawer v-model:visible="detailVisible" title="交易账户详情" :width="760" @cancel="closeDetail">
        <template v-if="selectedAccount">
          <div class="detail-head">
            <div>
              <h3>{{ selectedAccount.name }}</h3>
              <div class="muted">{{ selectedAccount.trading_account_id }}</div>
            </div>
            <a-space>
              <a-button :loading="syncingId === selectedAccount.trading_account_id" @click="sync(selectedAccount)"
                >同步账户</a-button
              >
              <a-button @click="goPositions(selectedAccount)">查看持仓</a-button>
              <a-button @click="goOrders(selectedAccount)">查看订单</a-button>
            </a-space>
          </div>
          <a-descriptions :column="2" bordered>
            <a-descriptions-item label="执行模式">{{
              executionModeLabels[selectedAccount.execution_mode] || "Unknown"
            }}</a-descriptions-item>
            <a-descriptions-item label="环境">{{ accountEnvironmentView(selectedAccount) }}</a-descriptions-item>
            <a-descriptions-item label="状态">{{ selectedAccount.status || "Unknown" }}</a-descriptions-item>
            <a-descriptions-item label="Readiness">{{ selectedAccount.ready ? "Ready" : "Not Ready" }}</a-descriptions-item>
            <a-descriptions-item label="最近同步">{{ formatTimestamp(selectedAccount.last_sync_at) }}</a-descriptions-item>
            <a-descriptions-item label="最近就绪">{{ formatTimestamp(selectedAccount.last_ready_at) }}</a-descriptions-item>
            <a-descriptions-item label="错误" :span="2">{{ selectedAccount.last_error || "-" }}</a-descriptions-item>
          </a-descriptions>
          <a-alert
            v-if="syncResult && syncResultAccountId === selectedAccount.trading_account_id"
            :type="syncResult.ready ? 'success' : 'warning'"
            show-icon
            class="section"
          >
            同步完成：{{ syncResult.fills_ingested }} fills，{{ syncResult.orders_updated }} orders，
            {{ syncResult.positions_updated }} positions；{{ syncResult.ready ? "Ready" : "Not Ready" }}
            <span v-if="syncResult.warnings.length">；{{ syncResult.warnings.join("；") }}</span>
          </a-alert>
          <div class="section">
            <h3>资金快照</h3>
            <a-descriptions :column="3" bordered>
              <a-descriptions-item label="Equity">{{ snapshotValue(selectedAccount.snapshot?.equity) }}</a-descriptions-item>
              <a-descriptions-item label="Available">{{
                snapshotValue(selectedAccount.snapshot?.available_funds)
              }}</a-descriptions-item>
              <a-descriptions-item label="未实现 PnL">
                <span :class="snapshotPnlClass(selectedAccount.snapshot?.unrealized_pnl)">
                  {{ snapshotValue(selectedAccount.snapshot?.unrealized_pnl) }}
                </span>
              </a-descriptions-item>
            </a-descriptions>
          </div>
          <div class="section">
            <h3>同步标的</h3>
            <span>{{ selectedAccount.sync_symbols.length ? selectedAccount.sync_symbols.join(", ") : "-" }}</span>
          </div>
          <div class="section">
            <h3>关联 LogicalAccount</h3>
            <a-button v-if="linkedLogicalAccount" type="text" @click="goLinkedLogicalAccount">
              {{ linkedLogicalAccount.name }} · {{ linkedLogicalAccount.logical_account_id }}
            </a-button>
            <span v-else>-</span>
          </div>
          <div class="section">
            <h3>杠杆配置</h3>
            <span>{{
              Object.keys(selectedAccount.leverage_settings).length ? JSON.stringify(selectedAccount.leverage_settings) : "-"
            }}</span>
          </div>
          <div v-if="selectedAccount.execution_mode === 2" class="section">
            <h3>Live 配置</h3>
            <a-form :model="editForm" auto-label-width>
              <a-form-item field="name" label="名称"><a-input v-model="editForm.name" /></a-form-item>
              <a-form-item field="credential_secret_id" label="Secret ID"
                ><a-input v-model="editForm.credential_secret_id"
              /></a-form-item>
              <a-form-item field="settlement_asset" label="结算资产"><a-input v-model="editForm.settlement_asset" /></a-form-item>
              <a-form-item field="sync_symbols" label="交易标的"><a-input v-model="editForm.sync_symbols" /></a-form-item>
              <a-form-item field="status" label="状态">
                <a-select v-model="editForm.status">
                  <a-option value="ENABLED">ENABLED</a-option>
                  <a-option value="DISABLED">DISABLED</a-option>
                </a-select>
              </a-form-item>
              <a-button type="primary" @click="saveAccount">保存 Live 配置</a-button>
            </a-form>
            <a-form v-if="selectedAccount.market_type === 2" :model="leverageForm" layout="inline" class="leverage-form">
              <a-input v-model="leverageForm.instrument_id" placeholder="Instrument ID" />
              <a-input v-model="leverageForm.leverage" placeholder="Leverage" />
              <a-button @click="saveLeverage">设置杠杆</a-button>
            </a-form>
          </div>
          <a-alert v-if="selectedAccount.execution_mode === 1 && !canCloseSelectedPaper" type="info" show-icon class="section">
            当前 Paper 模拟不可关闭：{{
              capabilitiesByAccount[selectedAccount.trading_account_id]?.unavailable_reason || "服务端未授权"
            }}
          </a-alert>
          <a-button
            v-if="selectedAccount.execution_mode === 1"
            status="danger"
            :disabled="!canCloseSelectedPaper"
            @click="requestClosePaper(selectedAccount)"
          >
            关闭 Paper 模拟
          </a-button>
        </template>
      </a-drawer>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref, watch } from "vue";
import { Message, Modal } from "@arco-design/web-vue";
import { useRouter } from "vue-router";
import {
  closePaperSimulation,
  createPaperSimulation,
  createTradingAccount,
  exchangeLabels,
  executionModeLabels,
  formatTimestamp,
  getExecutionCapabilities,
  getTradingAccount,
  listLogicalAccounts,
  listTradingAccounts,
  marketTypeLabels,
  setLeverage,
  syncTradingAccount,
  updateTradingAccount
} from "@/api/trade";
import type { ExecutionCapabilities, LogicalAccount, TradingAccount } from "@/api/trade/types";
import { createLatestRequestGuard } from "@/utils/latest-request";
import {
  buildLiveRequest,
  buildPaperSimulationRequest,
  createDefaultAccountForm,
  validateAccountForm,
  type AccountFormModel
} from "./account-form";
import { accountEnvironmentView, accountStatusView, snapshotPnlClass, snapshotValue } from "./account-display";

defineOptions({ name: "account-overview" });

interface SyncResult {
  fills_ingested: number;
  orders_updated: number;
  positions_updated: number;
  ready: boolean;
  warnings: string[];
}

const router = useRouter();
const accounts = ref<TradingAccount[]>([]);
const loading = ref(false);
const syncingId = ref("");
const closingId = ref("");
const createVisible = ref(false);
const detailVisible = ref(false);
const selectedAccount = ref<TradingAccount | null>(null);
const form = reactive<AccountFormModel>(createDefaultAccountForm());
const editForm = reactive({ name: "", credential_secret_id: "", settlement_asset: "", sync_symbols: "", status: "ENABLED" });
const leverageForm = reactive({ instrument_id: "", leverage: "" });
const formErrors = ref("");
const syncResult = ref<SyncResult | null>(null);
const createdAccountId = ref("");
const createdLogicalAccountId = ref("");
const syncResultAccountId = ref("");
const capabilitiesByAccount = reactive<Record<string, ExecutionCapabilities>>({});
const linkedLogicalAccount = ref<LogicalAccount | null>(null);
const pagination = reactive({ current: 1, pageSize: 20, total: 0 });
const listRequests = createLatestRequestGuard();
const syncRequests = createLatestRequestGuard();
const detailRequests = createLatestRequestGuard();

const canCloseSelectedPaper = computed(() => {
  const id = selectedAccount.value?.trading_account_id;
  return Boolean(id && capabilitiesByAccount[id]?.can_close_paper_simulation);
});

watch(
  () => form.execution_mode,
  () => {
    form.environment = 1;
    if (form.execution_mode === 1) form.credential_secret_id = "";
  }
);
watch(
  () => form.market_type,
  marketType => {
    if (marketType === 2) {
      form.settlement_asset = "USDT";
      form.margin_mode = "CROSS";
      form.sync_symbols = "";
    }
  }
);

async function loadAccounts() {
  const request = listRequests.begin();
  loading.value = true;
  try {
    const response = await listTradingAccounts({ page: { page: pagination.current, size: pagination.pageSize } });
    if (!request.isLatest()) return;
    accounts.value = response.accounts || [];
    pagination.total = response.page_result?.total || 0;
  } catch (error) {
    if (request.isLatest()) Message.error(error instanceof Error ? error.message : "加载交易账户失败，请稍后重试");
  } finally {
    if (request.isLatest()) loading.value = false;
  }
}

function changePage(page: number) {
  pagination.current = page;
  void loadAccounts();
}

async function sync(account: TradingAccount) {
  const request = syncRequests.begin();
  syncingId.value = account.trading_account_id;
  try {
    const response = await syncTradingAccount(account.trading_account_id);
    if (!request.isLatest()) return;
    const refreshed = await getTradingAccount(account.trading_account_id);
    if (!request.isLatest()) return;
    syncResult.value = {
      fills_ingested: response.fills_ingested,
      orders_updated: response.orders_updated,
      positions_updated: response.positions_updated,
      ready: response.ready,
      warnings: response.warnings || []
    };
    syncResultAccountId.value = account.trading_account_id;
    if (selectedAccount.value?.trading_account_id === account.trading_account_id) selectedAccount.value = refreshed.account;
    if (response.warnings?.length) Message.warning(response.warnings.join("；"));
    else Message.success("账户同步完成");
    await loadAccounts();
  } catch (error) {
    Message.error(error instanceof Error ? error.message : "账户同步失败，请检查连接和 Secret 配置");
  } finally {
    if (request.isLatest()) syncingId.value = "";
  }
}

function openCreate() {
  Object.assign(form, createDefaultAccountForm());
  formErrors.value = "";
  syncResult.value = null;
  syncResultAccountId.value = "";
  createVisible.value = true;
}

async function submitCreate() {
  const validation = validateAccountForm(form);
  if (validation) {
    formErrors.value = validation;
    return false;
  }
  try {
    if (form.execution_mode === 1) {
      const response = await createPaperSimulation(buildPaperSimulationRequest(form));
      createdAccountId.value = response.account?.trading_account_id || "";
      createdLogicalAccountId.value = response.logical_account?.logical_account_id || "";
    } else {
      const response = await createTradingAccount(buildLiveRequest(form));
      createdAccountId.value = response.account?.trading_account_id || "";
      createdLogicalAccountId.value = "";
      if (response.account?.trading_account_id) {
        try {
          const sync = await syncTradingAccount(response.account.trading_account_id);
          syncResult.value = {
            fills_ingested: sync.fills_ingested,
            orders_updated: sync.orders_updated,
            positions_updated: sync.positions_updated,
            ready: sync.ready,
            warnings: sync.warnings || []
          };
          syncResultAccountId.value = response.account.trading_account_id;
        } catch {
          Message.warning("Live 账户已创建，但首次同步失败，请在详情中重试");
        }
      }
    }
    createVisible.value = false;
    formErrors.value = "";
    await loadAccounts();
    return true;
  } catch (error) {
    formErrors.value = error instanceof Error ? error.message : "创建失败，请检查输入和服务端错误";
    return false;
  }
}

function create() {
  if (form.execution_mode === 2 && form.environment === 2) {
    Modal.confirm({
      title: "确认创建 Production 账户",
      content: "Production 账户可能产生真实交易，请确认环境和 Secret ID。",
      onOk: () => submitCreate()
    });
    return false;
  }
  return submitCreate();
}

async function openDetail(account: TradingAccount) {
  detailRequests.invalidate();
  selectedAccount.value = account;
  detailVisible.value = true;
  syncResult.value = null;
  syncResultAccountId.value = "";
  Object.assign(editForm, {
    name: account.name,
    credential_secret_id: account.live?.credential_secret_id || "",
    settlement_asset: account.settlement_asset,
    sync_symbols: account.sync_symbols.join(", "),
    status: account.status
  });
  Object.assign(leverageForm, { instrument_id: "", leverage: "" });
  linkedLogicalAccount.value = null;
  const request = detailRequests.begin();
  try {
    const response = await listLogicalAccounts({ page: 1, size: 200 });
    if (!request.isLatest() || selectedAccount.value?.trading_account_id !== account.trading_account_id) return;
    linkedLogicalAccount.value =
      response.logical_accounts.find(item =>
        item.members.some(member => member.trading_account_id === account.trading_account_id)
      ) || null;
  } catch {
    if (request.isLatest() && selectedAccount.value?.trading_account_id === account.trading_account_id) {
      linkedLogicalAccount.value = null;
    }
  }
  if (account.execution_mode === 1) {
    try {
      const response = await getExecutionCapabilities(account.trading_account_id);
      if (!request.isLatest() || selectedAccount.value?.trading_account_id !== account.trading_account_id) return;
      capabilitiesByAccount[account.trading_account_id] = response.capabilities;
    } catch {
      if (request.isLatest() && selectedAccount.value?.trading_account_id === account.trading_account_id) {
        capabilitiesByAccount[account.trading_account_id] = {
          can_place_order: false,
          unavailable_reason: "无法读取 Paper 能力",
          order_types: [],
          fill_policies: [],
          can_close_paper_simulation: false
        };
      }
    }
  }
}

function closeDetail() {
  detailRequests.invalidate();
  detailVisible.value = false;
  selectedAccount.value = null;
  linkedLogicalAccount.value = null;
}

async function saveAccount() {
  if (!selectedAccount.value) return;
  const accountId = selectedAccount.value.trading_account_id;
  try {
    const response = await updateTradingAccount({
      trading_account_id: selectedAccount.value.trading_account_id,
      name: editForm.name.trim(),
      credential_secret_id: editForm.credential_secret_id.trim(),
      settlement_asset: editForm.settlement_asset.trim().toUpperCase(),
      status: editForm.status,
      sync_symbols: editForm.sync_symbols
        .split(/[\s,]+/)
        .filter(Boolean)
        .map(symbol => symbol.toUpperCase())
    });
    if (selectedAccount.value?.trading_account_id !== accountId) return;
    selectedAccount.value = response.account;
    Message.success("Live 配置已保存");
    await loadAccounts();
  } catch (error) {
    if (selectedAccount.value?.trading_account_id === accountId) {
      Message.error(error instanceof Error ? error.message : "保存失败，请检查配置");
    }
  }
}

async function saveLeverage() {
  if (!selectedAccount.value || !leverageForm.instrument_id.trim() || !leverageForm.leverage.trim()) {
    Message.warning("请输入 Instrument ID 和杠杆");
    return;
  }
  const accountId = selectedAccount.value.trading_account_id;
  try {
    const response = await setLeverage({
      trading_account_id: selectedAccount.value.trading_account_id,
      instrument_id: leverageForm.instrument_id.trim(),
      leverage: leverageForm.leverage.trim()
    });
    if (selectedAccount.value?.trading_account_id !== accountId) return;
    selectedAccount.value = response.account;
    Message.success("杠杆已更新");
  } catch (error) {
    if (selectedAccount.value?.trading_account_id === accountId) {
      Message.error(error instanceof Error ? error.message : "杠杆更新失败");
    }
  }
}

async function requestClosePaper(account: TradingAccount) {
  if (!capabilitiesByAccount[account.trading_account_id]) await openDetail(account);
  if (!capabilitiesByAccount[account.trading_account_id]?.can_close_paper_simulation) {
    Message.warning("服务端当前不允许关闭该 Paper 模拟");
    return;
  }
  Modal.confirm({
    title: "关闭 Paper 模拟",
    content: "关闭后不可恢复，仅保留历史查询。",
    onOk: () => closePaper(account)
  });
}

async function closePaper(account: TradingAccount) {
  closingId.value = account.trading_account_id;
  try {
    await closePaperSimulation(account.trading_account_id);
    Message.success("Paper 模拟已关闭");
    if (selectedAccount.value?.trading_account_id === account.trading_account_id) closeDetail();
    await loadAccounts();
  } catch (error) {
    Message.error(error instanceof Error ? error.message : "关闭 Paper 模拟失败");
  } finally {
    closingId.value = "";
  }
}

function goLogicalAccount() {
  if (createdLogicalAccountId.value) {
    void router.push({ path: "/trading/logical-accounts", query: { logical_account_id: createdLogicalAccountId.value } });
  }
}

function goLinkedLogicalAccount() {
  if (linkedLogicalAccount.value) {
    void router.push({
      path: "/trading/logical-accounts",
      query: { logical_account_id: linkedLogicalAccount.value.logical_account_id }
    });
  }
}

function goPositions(account: TradingAccount) {
  void router.push({ path: "/trading/positions", query: { trading_account_id: account.trading_account_id } });
}

function goOrders(account: TradingAccount) {
  void router.push({ path: "/trading/orders", query: { trading_account_id: account.trading_account_id } });
}

onMounted(() => loadAccounts());
</script>

<style scoped>
.page-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: var(--moox-space-2);
}

.result-alert,
.form-warning,
.section {
  margin-top: var(--moox-space-3);
}

.detail-head {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  margin-bottom: var(--moox-space-4);
}

.detail-head h3 {
  margin: 0;
}

.muted {
  color: var(--color-text-3);
  font-size: 12px;
}

.account-id {
  max-width: 180px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.positive {
  color: rgb(var(--red-6));
}

.negative {
  color: rgb(var(--green-6));
}

.leverage-form {
  margin-top: var(--moox-space-3);
}
</style>
