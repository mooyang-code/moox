<template>
  <div class="moox-page orders-page">
    <div class="moox-inner">
      <PageTitleTabs :model-value="activeTab" :items="tabs" aria-label="交易记录" @change="onTabChange" />

      <section class="orders-workbench-content">
        <section v-if="activeTab === 'orders'" class="orders-tab-panel" aria-label="订单">
          <a-space class="filter-bar" wrap>
            <a-select v-model="tradingAccountId" placeholder="执行账户" class="account-select" @change="accountChanged">
              <a-option v-for="account in accounts" :key="account.trading_account_id" :value="account.trading_account_id">
                {{ account.name }} · {{ localMarketTypeLabels[account.market_type] }}
              </a-option>
            </a-select>
            <a-tag v-if="selectedAccount" :color="selectedAccount.ready ? 'green' : 'orange'">
              {{ selectedAccount.ready ? "就绪" : "未就绪" }}
            </a-tag>
            <a-input v-model="filterSymbol" placeholder="交易标的" allow-clear class="symbol-input" @press-enter="searchOrders" />
            <a-select v-model="orderState" allow-clear placeholder="订单状态" class="state-select">
              <a-option v-for="item in orderStateOptions" :key="item.value" :value="item.value">{{ item.label }}</a-option>
            </a-select>
            <a-range-picker v-model="orderTimeRange" value-format="timestamp" class="time-range" @change="searchOrders" />
            <a-button type="primary" :disabled="!tradingAccountId" @click="searchOrders">
              <template #icon><icon-search /></template>
              查询
            </a-button>
          </a-space>
          <div class="orders-table-region">
            <a-empty v-if="!loading && !orders.length" class="orders-empty-state" description="暂无订单" />
            <a-table
              v-else
              row-key="order_id"
              :data="orders"
              :loading="loading"
              :pagination="orderPagination"
              :scroll="{ x: 'max-content' }"
              @page-change="changeOrderPage"
            >
              <template #columns>
                <a-table-column title="交易标的" data-index="instrument_id" />
                <a-table-column title="市场">
                  <template #cell="{ record }">{{ localMarketTypeLabels[record.market_type] }}</template>
                </a-table-column>
                <a-table-column title="方向">
                  <template #cell="{ record }">
                    <a-tag :color="orderSideColors[record.side]">{{ orderSideLabels[record.side] }}</a-tag>
                  </template>
                </a-table-column>
                <a-table-column title="类型">
                  <template #cell="{ record }">{{ localOrderTypeLabels[record.order_type] }}</template>
                </a-table-column>
                <a-table-column title="限价">
                  <template #cell="{ record }">{{ record.limit_price || "-" }}</template>
                </a-table-column>
                <a-table-column title="数量" data-index="quantity" />
                <a-table-column title="成交进度">
                  <template #cell="{ record }">{{ record.filled_quantity || "0" }} / {{ record.quantity }}</template>
                </a-table-column>
                <a-table-column title="均价" data-index="average_price" />
                <a-table-column title="状态">
                  <template #cell="{ record }">
                    <a-tag :color="orderStateColor(record.state)">{{ orderStateLabel(record.state) }}</a-tag>
                  </template>
                </a-table-column>
                <a-table-column title="时间">
                  <template #cell="{ record }">{{ formatTimestamp(record.submitted_at || record.created_at) }}</template>
                </a-table-column>
                <a-table-column title="操作" fixed="right">
                  <template #cell="{ record }">
                    <a-button v-if="canCancel(record.state)" size="mini" status="danger" @click="cancel(record)">撤单</a-button>
                  </template>
                </a-table-column>
              </template>
            </a-table>
          </div>
        </section>

        <section v-else class="orders-tab-panel" aria-label="成交">
          <a-space class="filter-bar" wrap>
            <a-select v-model="tradingAccountId" placeholder="执行账户" class="account-select" @change="accountChanged">
              <a-option v-for="account in accounts" :key="account.trading_account_id" :value="account.trading_account_id">
                {{ account.name }} · {{ localMarketTypeLabels[account.market_type] }}
              </a-option>
            </a-select>
            <a-tag v-if="selectedAccount" :color="selectedAccount.ready ? 'green' : 'orange'">
              {{ selectedAccount.ready ? "就绪" : "未就绪" }}
            </a-tag>
            <a-input v-model="filterSymbol" placeholder="交易标的" allow-clear class="symbol-input" @press-enter="searchFills" />
            <a-range-picker v-model="fillTimeRange" value-format="timestamp" class="time-range" @change="searchFills" />
            <a-button type="primary" :disabled="!tradingAccountId" @click="searchFills">
              <template #icon><icon-search /></template>
              查询
            </a-button>
          </a-space>
          <div class="orders-table-region">
            <a-empty v-if="!loading && !fills.length" class="orders-empty-state" description="暂无成交记录" />
            <a-table
              v-else
              row-key="fill_id"
              :data="fills"
              :loading="loading"
              :pagination="fillPagination"
              :scroll="{ x: 'max-content' }"
              @page-change="changeFillPage"
            >
              <template #columns>
                <a-table-column title="成交编号" data-index="fill_id" ellipsis />
                <a-table-column title="交易标的" data-index="instrument_id" />
                <a-table-column title="市场">
                  <template #cell="{ record }">{{ localMarketTypeLabels[record.market_type] }}</template>
                </a-table-column>
                <a-table-column title="方向">
                  <template #cell="{ record }">{{ orderSideLabels[record.side] }}</template>
                </a-table-column>
                <a-table-column title="价格" data-index="price" />
                <a-table-column title="数量" data-index="quantity" />
                <a-table-column title="手续费">
                  <template #cell="{ record }">{{ record.fee }} {{ record.fee_asset }}</template>
                </a-table-column>
                <a-table-column title="已实现盈亏" data-index="realized_pnl" />
                <a-table-column title="角色">
                  <template #cell="{ record }">{{ fillRoleLabel(record.role) }}</template>
                </a-table-column>
                <a-table-column title="成交时间">
                  <template #cell="{ record }">{{ formatTimestamp(record.traded_at) }}</template>
                </a-table-column>
              </template>
            </a-table>
          </div>
        </section>
      </section>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref, toRef, watch } from "vue";
import { Message } from "@arco-design/web-vue";
import { useRoute, useRouter } from "vue-router";
import PageTitleTabs from "@/components/page-title-tabs/index.vue";
import { createClientId } from "@/utils/client-id";
import { createLatestRequestGuard } from "@/utils/latest-request";
import {
  canCancelOrderState,
  cancelOrder,
  formatTimestamp,
  listTradingAccounts,
  listFills,
  listOrders,
  orderSideColors,
  orderSideLabels,
  orderStateLabels
} from "@/api/trade";
import type { TradingAccount, Fill, Order } from "@/api/trade/types";
import { tradeRecordViewState } from "./trade-record-state";
import { rangeToTime } from "./trade-record-utils";

defineOptions({ name: "trade-record" });

const accounts = ref<TradingAccount[]>([]);
const accountsLoaded = ref(false);
const route = useRoute();
const router = useRouter();
const tradingAccountId = ref(typeof route.query.trading_account_id === "string" ? route.query.trading_account_id : "");
const selectedAccount = computed(
  () => accounts.value.find(account => account.trading_account_id === tradingAccountId.value) || null
);
type TradeRecordTab = "orders" | "fills";
const tabs = [
  { key: "orders", label: "订单" },
  { key: "fills", label: "成交" }
] as const;
const activeTab = ref<TradeRecordTab>(tabFromRoute());
const filterSymbol = toRef(tradeRecordViewState, "filterSymbol");
const orderState = toRef(tradeRecordViewState, "orderState");
const orderTimeRange = toRef(tradeRecordViewState, "orderTimeRange");
const fillTimeRange = toRef(tradeRecordViewState, "fillTimeRange");
const orders = ref<Order[]>([]);
const fills = ref<Fill[]>([]);
const loading = ref(false);
const orderPagination = reactive({ current: tradeRecordViewState.orderPage, pageSize: 20, total: 0 });
const fillPagination = reactive({ current: tradeRecordViewState.fillPage, pageSize: 20, total: 0 });
const accountRequests = createLatestRequestGuard();
const orderRequests = createLatestRequestGuard();
const fillRequests = createLatestRequestGuard();
const localMarketTypeLabels: Record<number, string> = { 0: "-", 1: "现货", 2: "合约" };
const localOrderTypeLabels: Record<number, string> = { 0: "-", 1: "市价", 2: "限价" };
const fillRoleLabels: Record<string, string> = { MAKER: "挂单方", TAKER: "吃单方" };
const orderStateOptions = Object.entries(orderStateLabels).map(([value, label]) => ({ value, label }));

function tabFromRoute(): TradeRecordTab {
  return route.query.tab === "fills" ? "fills" : "orders";
}

function normalizeTab(value: string | number): TradeRecordTab {
  return value === "fills" ? "fills" : "orders";
}

function onTabChange(value: string | number) {
  const tab = normalizeTab(value);
  activeTab.value = tab;
  void router.replace({ query: { ...route.query, tab: tab === "fills" ? "fills" : undefined } });
}

function orderStateLabel(state?: string) {
  const normalized = state?.toUpperCase() || "";
  return orderStateLabels[normalized] || "未知";
}
function orderStateColor(state?: string) {
  switch (state?.toUpperCase()) {
    case "FILLED":
      return "green";
    case "REJECTED":
    case "EXPIRED":
      return "red";
    case "CANCELED":
      return "gray";
    case "PARTIALLY_FILLED":
    case "CANCELING":
      return "orange";
    default:
      return "blue";
  }
}
function fillRoleLabel(role?: string) {
  return fillRoleLabels[role?.toUpperCase() || ""] || "未知";
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
      orderRequests.invalidate();
      fillRequests.invalidate();
      loading.value = false;
      orders.value = [];
      fills.value = [];
    }
    return;
  }
  if (!accounts.value.some(account => account.trading_account_id === requested)) {
    tradingAccountId.value = "";
    orderRequests.invalidate();
    fillRequests.invalidate();
    loading.value = false;
    orders.value = [];
    fills.value = [];
    await router.replace({ query: { ...route.query, trading_account_id: undefined } });
    Message.warning("账户不存在或无权限");
    return;
  }
  if (tradingAccountId.value === requested) return;
  tradingAccountId.value = requested;
  resetPagination();
  await loadActiveTab();
}

async function accountChanged() {
  resetPagination();
  await router.replace({ query: { ...route.query, trading_account_id: tradingAccountId.value || undefined } });
  await loadActiveTab();
}

function resetPagination() {
  orderPagination.current = 1;
  fillPagination.current = 1;
  tradeRecordViewState.orderPage = 1;
  tradeRecordViewState.fillPage = 1;
}

async function loadOrders() {
  if (!tradingAccountId.value) {
    loading.value = false;
    orders.value = [];
    return;
  }
  const request = orderRequests.begin();
  const accountId = tradingAccountId.value;
  const query = {
    trading_account_id: accountId,
    instrument_id: filterSymbol.value.trim().toUpperCase(),
    state: orderState.value || undefined,
    ...rangeToTime(orderTimeRange.value),
    page: { page: orderPagination.current, size: orderPagination.pageSize }
  };
  loading.value = true;
  try {
    const response = await listOrders(query);
    if (!request.isLatest() || tradingAccountId.value !== accountId) return;
    orders.value = response.orders || [];
    orderPagination.total = response.page_result?.total || 0;
  } finally {
    if (request.isLatest()) loading.value = false;
  }
}

function searchOrders() {
  orderPagination.current = 1;
  tradeRecordViewState.orderPage = 1;
  return loadOrders();
}

async function loadFills() {
  if (!tradingAccountId.value) {
    loading.value = false;
    fills.value = [];
    return;
  }
  const request = fillRequests.begin();
  const accountId = tradingAccountId.value;
  const query = {
    trading_account_id: accountId,
    instrument_id: filterSymbol.value.trim().toUpperCase(),
    ...rangeToTime(fillTimeRange.value),
    page: { page: fillPagination.current, size: fillPagination.pageSize }
  };
  loading.value = true;
  try {
    const response = await listFills(query);
    if (!request.isLatest() || tradingAccountId.value !== accountId) return;
    fills.value = response.fills || [];
    fillPagination.total = response.page_result?.total || 0;
  } finally {
    if (request.isLatest()) loading.value = false;
  }
}

function searchFills() {
  fillPagination.current = 1;
  tradeRecordViewState.fillPage = 1;
  return loadFills();
}

function loadActiveTab() {
  return activeTab.value === "fills" ? loadFills() : loadOrders();
}

function changeOrderPage(page: number) {
  orderPagination.current = page;
  tradeRecordViewState.orderPage = page;
  loadOrders();
}

function changeFillPage(page: number) {
  fillPagination.current = page;
  tradeRecordViewState.fillPage = page;
  loadFills();
}

const canCancel = canCancelOrderState;

async function cancel(order: Order) {
  if (order.trading_account_id !== tradingAccountId.value) {
    Message.warning("账户已切换，请重新查询后重试");
    return;
  }
  await cancelOrder(createClientId(), order.order_id, "控制台手动撤单");
  Message.success("撤单请求已提交");
  await loadOrders();
}

watch(
  () => route.query.trading_account_id,
  () => {
    void applyRouteAccount();
  }
);

watch(
  () => route.query.tab,
  value => {
    const tab = value === "fills" ? "fills" : "orders";
    if (tab === activeTab.value) return;
    activeTab.value = tab;
    void loadActiveTab();
  }
);

onMounted(async () => {
  await loadAccounts();
  await loadActiveTab();
});
</script>

<style scoped>
.orders-page {
  display: flex;
  height: 100%;
  min-height: 0;
  overflow: hidden;
}
.orders-page > .moox-inner {
  display: flex;
  height: 100%;
  min-height: 0;
  flex-direction: column;
}
.orders-workbench-content {
  display: flex;
  min-height: 0;
  flex: 1;
  flex-direction: column;
  gap: var(--moox-space-3);
  margin-top: var(--moox-space-3);
  overflow: hidden;
}
.filter-bar {
  display: flex;
  width: 100%;
  max-width: 100%;
  min-width: 0;
  justify-content: flex-start;
  margin-bottom: var(--moox-space-tight);
}
.orders-tab-panel {
  display: flex;
  min-height: 0;
  flex: 1;
  flex-direction: column;
}
.orders-table-region {
  min-width: 0;
  min-height: 0;
  flex: 1;
  overflow: auto;
}
.orders-empty-state {
  display: flex;
  min-height: 180px;
  align-items: center;
  justify-content: center;
}
.orders-page :deep(.account-select) {
  width: 200px;
}
.orders-page :deep(.symbol-input) {
  width: 160px;
}
.orders-page :deep(.state-select) {
  width: 140px;
}
.orders-page :deep(.time-range) {
  width: 300px;
}
.orders-table-region :deep(.arco-table-container) {
  border-radius: 8px;
}
.orders-table-region :deep(.arco-table-th) {
  white-space: nowrap;
}
@media (max-width: 900px) {
  .orders-page :deep(.account-select),
  .orders-page :deep(.symbol-input),
  .orders-page :deep(.state-select),
  .orders-page :deep(.time-range) {
    width: 100%;
  }
}
</style>
