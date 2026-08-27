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

      <div class="account-context-row">
        <div class="account-context-main">
          <span class="toolbar-label">执行账户</span>
          <a-select
            v-model="tradingAccountId"
            placeholder="选择执行账户"
            class="account-select"
            :loading="accountsLoading"
            :disabled="accountsLoading || accounts.length === 0"
            @change="onAccountChange"
          >
            <a-option v-for="account in accounts" :key="account.trading_account_id" :value="account.trading_account_id">
              {{ account.name }} · {{ localMarketTypeLabels[account.market_type] }}
            </a-option>
          </a-select>
        </div>
        <div v-if="selectedAccount" class="account-context-meta">
          <a-tag
            >{{ localExchangeLabels[selectedAccount.exchange] }} · {{ localMarketTypeLabels[selectedAccount.market_type] }}</a-tag
          >
          <a-tag :color="selectedAccount.ready ? 'green' : 'orange'">
            {{ selectedAccount.ready ? "已就绪" : "未就绪" }}
          </a-tag>
          <span class="sync-time">最近同步 {{ formatTimestamp(selectedAccount.last_sync_at) }}</span>
        </div>
      </div>

      <a-alert v-if="accountsError" type="error" show-icon class="account-load-error">
        {{ accountsError }}
        <template #action>
          <a-button type="text" size="small" @click="refresh">重新加载</a-button>
        </template>
      </a-alert>

      <a-alert
        v-if="selectedAccount && (!selectedAccount.ready || selectedAccount.last_error)"
        class="account-status"
        :type="selectedAccount.ready ? 'info' : 'warning'"
      >
        {{ selectedAccount.last_error || "账户尚未完成同步，请先同步账户" }}
      </a-alert>

      <div class="position-filter-bar">
        <span class="toolbar-label">持仓筛选</span>
        <a-input v-model="instrumentId" placeholder="交易标的" allow-clear class="symbol-input" @press-enter="loadPositions" />
        <a-button :disabled="!tradingAccountId" @click="loadPositions">
          <template #icon><icon-search /></template>
          查询
        </a-button>
      </div>

      <template v-if="selectedAccount">
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
            ><strong>{{ selectedAccount.market_type === 1 ? visibleHoldings.length : positions.length }}</strong
            ><small>当前账户</small>
          </div>
        </div>

        <a-alert v-if="positionsError" type="error" show-icon class="position-load-error">
          {{ positionsError }}
          <template #action>
            <a-button type="text" size="small" @click="loadPositions">重新加载</a-button>
          </template>
        </a-alert>

        <div v-if="!positionsError" class="position-table-region">
          <a-empty
            v-if="
              !loading &&
              ((selectedAccount.market_type === 1 && !visibleHoldings.length) ||
                (selectedAccount.market_type === 2 && !positions.length))
            "
            class="position-empty-state"
            description="暂无持仓数据"
          />

          <a-table
            v-else-if="selectedAccount.market_type === 1"
            row-key="asset"
            :data="visibleHoldings"
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
        </div>
      </template>
      <div v-else class="position-empty-workbench">
        <a-empty :description="accountsLoading ? '正在加载执行账户' : '暂无可用执行账户'" />
        <p v-if="!accountsLoading && !accountsError">请先在账户总览中创建或启用执行账户。</p>
        <a-button v-if="!accountsLoading && !accountsError" type="text" @click="goAccounts">去账户总览</a-button>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref, watch } from "vue";
import { Message } from "@arco-design/web-vue";
import { useRoute, useRouter } from "vue-router";
import { createLatestRequestGuard } from "@/utils/latest-request";
import { formatTimestamp, listTradingAccounts, listHoldings, listPositions, syncTradingAccount } from "@/api/trade";
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
const accountsLoading = ref(false);
const accountsError = ref("");
const positionsError = ref("");
const accountsLoaded = ref(false);
const accountRequests = createLatestRequestGuard();
const positionRequests = createLatestRequestGuard();
const selectedAccount = computed(
  () => accounts.value.find(account => account.trading_account_id === tradingAccountId.value) || null
);
const visibleHoldings = computed(() => {
  const filter = instrumentId.value.trim().toUpperCase();
  if (!filter) return holdings.value;
  return holdings.value.filter(item =>
    [item.instrument_id, item.exchange_symbol, item.asset].some(value => value?.toUpperCase().includes(filter))
  );
});
const localExchangeLabels: Record<number, string> = { 0: "-", 1: "币安", 2: "欧易" };
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
  accountsLoading.value = true;
  accountsError.value = "";
  try {
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
  } catch {
    if (!request.isLatest()) return;
    accounts.value = [];
    tradingAccountId.value = "";
    accountsLoaded.value = true;
    accountsError.value = "执行账户加载失败，请检查交易服务后重试。";
  } finally {
    if (request.isLatest()) accountsLoading.value = false;
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
  if (tradingAccountId.value === requested) {
    await loadPositions();
    return;
  }
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
  positionsError.value = "";
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
  } catch {
    if (!request.isLatest() || tradingAccountId.value !== accountId) return;
    positions.value = [];
    holdings.value = [];
    positionsError.value = "持仓加载失败，请稍后重试。";
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
}

async function refresh() {
  await loadAccounts();
  await loadPositions();
}

function goAccounts() {
  void router.push({ path: "/trading/accounts" });
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
.position-page {
  display: flex;
  min-height: 0;
  flex-direction: column;
  overflow: hidden;
}
.position-page > .moox-inner {
  display: flex;
  height: 100%;
  min-height: 0;
  flex-direction: column;
  overflow: hidden;
}
.page-head {
  display: flex;
  flex: 0 0 auto;
  align-items: flex-start;
  justify-content: space-between;
  gap: 16px;
  margin-bottom: var(--moox-space-3);
}
.page-head h2 {
  margin: 0 0 4px;
}
.page-head > div > span {
  color: var(--color-text-3);
  font-size: 12px;
}
.account-context-row {
  display: flex;
  flex: 0 0 auto;
  align-items: center;
  justify-content: space-between;
  flex-wrap: wrap;
  gap: 10px;
  margin-bottom: var(--moox-space-2);
  padding: 0 0 12px;
  border-bottom: 1px solid var(--color-border-2);
}
.account-context-main,
.account-context-meta {
  display: flex;
  align-items: center;
  min-width: 0;
  gap: 10px;
}
.account-context-main {
  flex: 1;
}
.account-context-main .toolbar-label {
  flex: 0 0 auto;
}
.account-context-meta {
  justify-content: flex-end;
  flex-wrap: wrap;
  color: var(--color-text-3);
  font-size: 12px;
}
.sync-time {
  white-space: nowrap;
}
.account-load-error,
.position-load-error {
  flex: 0 0 auto;
  margin-bottom: var(--moox-space-2);
}
.position-filter-bar {
  display: flex;
  flex: 0 0 auto;
  align-items: center;
  flex-wrap: wrap;
  gap: 10px;
  margin-bottom: var(--moox-space-3);
  padding: 0 0 12px;
  border-bottom: 1px solid var(--color-border-2);
}
.toolbar-label {
  color: var(--color-text-3);
  font-size: 12px;
  white-space: nowrap;
}
.account-select {
  width: 320px;
}
.account-context-main :deep(.account-select) {
  flex: 0 1 320px;
  width: 320px;
}
.symbol-input {
  width: 240px;
}
.account-status {
  margin-bottom: var(--moox-space-3);
}
.summary-grid {
  display: grid;
  flex: 0 0 auto;
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
.position-table-region {
  display: flex;
  flex: 1 1 auto;
  min-width: 0;
  min-height: 200px;
  overflow: auto;
  border: 1px solid var(--color-border-2);
  border-radius: 8px;
  background: var(--color-bg-2);
}
.position-page :deep(.arco-table-container) {
  border-radius: 7px;
}
.position-page :deep(.arco-table-th) {
  white-space: nowrap;
}
.position-page :deep(.arco-empty) {
  padding: 42px 0;
}
.position-empty-state {
  display: flex;
  width: 100%;
  min-height: 180px;
  align-items: center;
  justify-content: center;
}
.position-empty-workbench {
  display: flex;
  flex: 1 1 auto;
  min-height: 240px;
  align-items: center;
  justify-content: center;
  flex-direction: column;
  border: 1px dashed var(--color-border-2);
  border-radius: 8px;
  background: var(--color-bg-2);
}
.position-empty-workbench p {
  margin: 4px 0 8px;
  color: var(--color-text-3);
  font-size: 13px;
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
  .account-context-row {
    align-items: stretch;
  }
  .account-context-main,
  .account-context-meta {
    width: 100%;
  }
  .account-context-main {
    align-items: flex-start;
    flex-direction: column;
  }
  .account-context-main :deep(.account-select) {
    flex-basis: auto;
    width: 100%;
  }
  .account-context-meta {
    justify-content: flex-start;
  }
  .position-filter-bar {
    align-items: stretch;
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
