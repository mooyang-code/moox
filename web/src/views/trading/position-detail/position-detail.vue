<template>
  <div class="moox-page position-page">
    <div class="moox-inner">
      <div class="page-head">
        <div>
          <h2>持仓</h2>
          <span>查看账户当前资产、合约仓位和风险指标。</span>
        </div>
        <a-space>
          <a-button :loading="loading" aria-label="刷新持仓" @click="refresh">
            <template #icon><icon-refresh /></template>
            刷新
          </a-button>
          <a-button :loading="syncing" :disabled="!tradingAccountId" @click="sync">
            <template #icon><icon-sync /></template>
            同步账户
          </a-button>
        </a-space>
      </div>

      <div class="filter-bar">
        <span class="toolbar-label">账户</span>
        <a-select v-model="tradingAccountId" placeholder="选择交易账户" class="account-select" @change="onAccountChange">
          <a-option v-for="account in accounts" :key="account.trading_account_id" :value="account.trading_account_id">
            {{ account.name }} · {{ localMarketTypeLabels[account.market_type] }}
          </a-option>
        </a-select>
        <a-input v-model="instrumentId" placeholder="交易标的" allow-clear class="symbol-input" @press-enter="loadPositions" />
        <a-button :disabled="!tradingAccountId" @click="loadPositions">
          <template #icon><icon-search /></template>
          查询
        </a-button>
      </div>

      <template v-if="selectedAccount">
        <a-alert class="account-status" :type="selectedAccount.ready ? 'success' : 'warning'">
          {{ exchangeLabels[selectedAccount.exchange] }} · {{ localMarketTypeLabels[selectedAccount.market_type] }} ·
          {{ selectedAccount.ready ? "就绪" : "未就绪" }} · 最近同步 {{ formatTimestamp(selectedAccount.last_sync_at) }}
          <span v-if="selectedAccount.last_error"> · {{ selectedAccount.last_error }}</span>
        </a-alert>

        <div class="summary-grid">
          <div class="summary-card">
            <span>权益</span><strong>{{ snapshotValue(selectedAccount.snapshot?.equity) }}</strong
            ><small>{{ selectedAccount.settlement_asset }}</small>
          </div>
          <div class="summary-card">
            <span>可用资金</span><strong>{{ snapshotValue(selectedAccount.snapshot?.available_funds) }}</strong
            ><small>可用于新订单</small>
          </div>
          <div class="summary-card" :class="pnlTone(selectedAccount.snapshot?.unrealized_pnl)">
            <span>未实现盈亏</span><strong>{{ snapshotValue(selectedAccount.snapshot?.unrealized_pnl) }}</strong
            ><small>按最新同步数据</small>
          </div>
          <div class="summary-card">
            <span>{{ selectedAccount.market_type === 1 ? "资产数" : "持仓数" }}</span
            ><strong>{{ selectedAccount.market_type === 1 ? holdings.length : positions.length }}</strong
            ><small>当前账户</small>
          </div>
        </div>

        <a-table
          v-if="selectedAccount.market_type === 1"
          row-key="asset"
          :data="holdings"
          :loading="loading"
          :pagination="false"
          :scroll="{ x: 960 }"
        >
          <template #columns>
            <a-table-column title="资产" data-index="asset" :width="100" />
            <a-table-column title="数量" data-index="quantity" :width="110" />
            <a-table-column title="平均成本" data-index="average_cost" :width="130" />
            <a-table-column title="标记价" data-index="mark_price" :width="130" />
            <a-table-column title="市值" data-index="market_value" :width="130" />
            <a-table-column title="未实现盈亏" :width="140">
              <template #cell="{ record }"
                ><span :class="pnlClass(record.unrealized_pnl)">{{ record.unrealized_pnl || "-" }}</span></template
              >
            </a-table-column>
            <a-table-column title="交易标的" data-index="instrument_id" :width="180" />
          </template>
        </a-table>

        <a-table
          v-else
          row-key="instrument_id"
          :data="positions"
          :loading="loading"
          :pagination="false"
          :scroll="{ x: 'max-content' }"
        >
          <template #columns>
            <a-table-column title="市场" :width="90">
              <template #cell>{{ localMarketTypeLabels[selectedAccount.market_type] }}</template>
            </a-table-column>
            <a-table-column title="交易标的" data-index="instrument_id" />
            <a-table-column title="方向" :width="90">
              <template #cell="{ record }">{{ quantitySide(record.signed_quantity) }}</template>
            </a-table-column>
            <a-table-column title="持仓数量" data-index="signed_quantity" />
            <a-table-column title="开仓价" data-index="entry_price" />
            <a-table-column title="标记价" data-index="mark_price" />
            <a-table-column title="强平价" data-index="liquidation_price" />
            <a-table-column title="杠杆" data-index="leverage" />
            <a-table-column title="保证金" data-index="used_margin" />
            <a-table-column title="未实现盈亏">
              <template #cell="{ record }">
                <span :class="pnlClass(record.unrealized_pnl)">{{ record.unrealized_pnl }}</span>
              </template>
            </a-table-column>
            <a-table-column title="更新时间">
              <template #cell="{ record }">{{ formatTimestamp(record.exchange_updated_at) }}</template>
            </a-table-column>
          </template>
        </a-table>
        <a-empty
          v-if="
            selectedAccount &&
            !loading &&
            ((selectedAccount.market_type === 1 && !holdings.length) || (selectedAccount.market_type === 2 && !positions.length))
          "
          description="暂无持仓数据"
        />
      </template>
      <a-empty v-else description="请选择一个交易账户" />
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref, watch } from "vue";
import { Message } from "@arco-design/web-vue";
import { useRoute, useRouter } from "vue-router";
import { createLatestRequestGuard } from "@/utils/latest-request";
import {
  exchangeLabels,
  formatTimestamp,
  listTradingAccounts,
  listHoldings,
  listPositions,
  syncTradingAccount
} from "@/api/trade";
import type { TradingAccount, Holding, Position } from "@/api/trade/types";

defineOptions({ name: "position-detail" });

const accounts = ref<TradingAccount[]>([]);
const positions = ref<Position[]>([]);
const holdings = ref<Holding[]>([]);
const route = useRoute();
const router = useRouter();
const tradingAccountId = ref(typeof route.query.trading_account_id === "string" ? route.query.trading_account_id : "");
const instrumentId = ref("");
const loading = ref(false);
const syncing = ref(false);
const accountsLoaded = ref(false);
const accountRequests = createLatestRequestGuard();
const positionRequests = createLatestRequestGuard();
const selectedAccount = computed(
  () => accounts.value.find(account => account.trading_account_id === tradingAccountId.value) || null
);
const localMarketTypeLabels: Record<number, string> = { 0: "-", 1: "现货", 2: "合约" };

function snapshotValue(value?: string) {
  return value && value !== "0" ? value : value === "0" ? "0" : "-";
}
function pnlTone(value?: string) {
  const parsed = Number(value);
  return parsed > 0 ? "positive" : parsed < 0 ? "negative" : "";
}

async function loadAccounts() {
  const request = accountRequests.begin();
  const response = await listTradingAccounts({ page: { page: 1, size: 200 } });
  if (!request.isLatest()) return;
  accounts.value = response.accounts || [];
  accountsLoaded.value = true;
  const requested = typeof route.query.trading_account_id === "string" ? route.query.trading_account_id : "";
  const hasRequested = accounts.value.some(account => account.trading_account_id === requested);
  tradingAccountId.value = hasRequested ? requested : requested ? "" : accounts.value[0]?.trading_account_id || "";
  if (requested && !hasRequested) {
    await router.replace({ query: { ...route.query, trading_account_id: undefined } });
    Message.warning("账户不存在或无权限");
  }
}

async function applyRouteAccount() {
  if (!accountsLoaded.value) return;
  const requested = typeof route.query.trading_account_id === "string" ? route.query.trading_account_id : "";
  if (!requested) {
    if (tradingAccountId.value) {
      tradingAccountId.value = "";
      positionRequests.invalidate();
      loading.value = false;
      positions.value = [];
      holdings.value = [];
    }
    return;
  }
  if (!accounts.value.some(account => account.trading_account_id === requested)) {
    tradingAccountId.value = "";
    positionRequests.invalidate();
    loading.value = false;
    positions.value = [];
    holdings.value = [];
    await router.replace({ query: { ...route.query, trading_account_id: undefined } });
    Message.warning("账户不存在或无权限");
    return;
  }
  if (tradingAccountId.value === requested) return;
  tradingAccountId.value = requested;
  await loadPositions();
}

async function loadPositions() {
  if (!tradingAccountId.value) {
    positionRequests.invalidate();
    loading.value = false;
    positions.value = [];
    holdings.value = [];
    return;
  }
  const request = positionRequests.begin();
  const accountId = tradingAccountId.value;
  const requestedSymbol = instrumentId.value.trim().toUpperCase();
  loading.value = true;
  try {
    if (selectedAccount.value?.market_type === 1) {
      const response = await listHoldings(accountId);
      if (!request.isLatest() || tradingAccountId.value !== accountId) return;
      holdings.value = response.holdings || [];
      positions.value = [];
      return;
    }
    const response = await listPositions({ trading_account_id: accountId, instrument_id: requestedSymbol });
    if (!request.isLatest() || tradingAccountId.value !== accountId) return;
    positions.value = response.positions || [];
    holdings.value = [];
  } finally {
    if (request.isLatest()) loading.value = false;
  }
}

async function sync() {
  const accountId = tradingAccountId.value;
  syncing.value = true;
  try {
    const response = await syncTradingAccount(accountId);
    if (tradingAccountId.value === accountId) Message.success(`同步完成：${response.positions_updated} 个仓位`);
    await Promise.all([loadAccounts(), loadPositions()]);
  } finally {
    syncing.value = false;
  }
}

function onAccountChange(id: string) {
  tradingAccountId.value = id;
  void router.replace({ query: { ...route.query, trading_account_id: id } });
  void loadPositions();
}

async function refresh() {
  await loadAccounts();
  await loadPositions();
}

watch(
  () => route.query.trading_account_id,
  () => {
    void applyRouteAccount();
  }
);

function quantitySide(quantity: string) {
  const value = Number(quantity);
  if (value > 0) return "多头";
  if (value < 0) return "空头";
  return "空仓";
}

function pnlClass(value: string) {
  const parsed = Number(value);
  return parsed > 0 ? "positive" : parsed < 0 ? "negative" : "";
}

onMounted(async () => {
  await loadAccounts();
  await loadPositions();
});
</script>

<style scoped>
.page-head {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 16px;
  margin-bottom: var(--moox-space-3);
}
.page-head h2 {
  margin: 0 0 4px;
}
.page-head span {
  color: var(--color-text-3);
  font-size: 12px;
}
.filter-bar {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 10px;
  margin-bottom: var(--moox-space-3);
  padding: 10px 12px;
  border: 1px solid var(--color-border-2);
  border-radius: 8px;
  background: var(--color-bg-2);
}
.toolbar-label {
  color: var(--color-text-3);
  font-size: 12px;
}
.account-select {
  width: 260px;
}
.symbol-input {
  width: 160px;
}
.account-status {
  margin-bottom: var(--moox-space-3);
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
  min-height: 86px;
  padding: 13px 15px;
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
  font-size: 20px;
  line-height: 1.1;
}
.summary-card.positive strong {
  color: rgb(var(--red-6));
}
.summary-card.negative strong {
  color: rgb(var(--green-6));
}
.position-page :deep(.arco-table-container) {
  border-radius: 8px;
}
.position-page :deep(.arco-table-th) {
  white-space: nowrap;
}
.position-page :deep(.arco-empty) {
  padding: 42px 0;
}
.positive {
  color: rgb(var(--red-6));
}
.negative {
  color: rgb(var(--green-6));
}
@media (max-width: 760px) {
  .page-head {
    flex-direction: column;
  }
  .summary-grid {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }
  .account-select,
  .symbol-input {
    width: 100%;
  }
}
</style>
