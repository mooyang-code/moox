<template>
  <div class="moox-page orders-page">
    <div class="moox-inner">
      <div class="page-head">
        <div>
          <h2>交易记录</h2>
          <span>查看订单状态和成交进度。</span>
        </div>
      </div>

      <section class="orders-workbench-content">
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
              <a-table-column title="操作" fixed="right" :width="130">
                <template #cell="{ record }">
                  <a-space>
                    <a-button size="mini" type="text" @click="openOrderDetail(record)">详情</a-button>
                    <a-button v-if="canCancel(record.state)" size="mini" type="text" status="danger" @click="cancel(record)"
                      >撤单</a-button
                    >
                  </a-space>
                </template>
              </a-table-column>
            </template>
          </a-table>
        </div>
      </section>

      <a-drawer
        v-model:visible="detailVisible"
        title="订单详情"
        width="min(760px, 100vw)"
        :footer="false"
        @cancel="closeOrderDetail"
      >
        <template v-if="selectedOrder">
          <a-descriptions :column="{ xs: 1, sm: 2 }" bordered size="small">
            <a-descriptions-item label="订单编号">{{ selectedOrder.order_id }}</a-descriptions-item>
            <a-descriptions-item label="执行账户">{{ selectedOrder.trading_account_id }}</a-descriptions-item>
            <a-descriptions-item label="交易标的">{{ selectedOrder.instrument_id }}</a-descriptions-item>
            <a-descriptions-item label="市场">{{ localMarketTypeLabels[selectedOrder.market_type] }}</a-descriptions-item>
            <a-descriptions-item label="方向">{{ orderSideLabels[selectedOrder.side] }}</a-descriptions-item>
            <a-descriptions-item label="类型">{{ localOrderTypeLabels[selectedOrder.order_type] }}</a-descriptions-item>
            <a-descriptions-item label="数量">{{ selectedOrder.quantity }}</a-descriptions-item>
            <a-descriptions-item label="成交数量">{{ selectedOrder.filled_quantity || "0" }}</a-descriptions-item>
            <a-descriptions-item label="均价">{{ selectedOrder.average_price || "-" }}</a-descriptions-item>
            <a-descriptions-item label="状态">
              <a-tag :color="orderStateColor(selectedOrder.state)">{{ orderStateLabel(selectedOrder.state) }}</a-tag>
            </a-descriptions-item>
            <a-descriptions-item label="提交时间">{{
              formatTimestamp(selectedOrder.submitted_at || selectedOrder.created_at)
            }}</a-descriptions-item>
            <a-descriptions-item label="完成时间">{{ formatTimestamp(selectedOrder.finished_at) }}</a-descriptions-item>
          </a-descriptions>

          <div class="detail-section">
            <div class="detail-section-head">
              <h3>成交明细</h3>
              <span v-if="orderFills.length" class="muted">{{ orderFills.length }} 条</span>
            </div>
            <a-alert v-if="detailError" type="error" show-icon>{{ detailError }}</a-alert>
            <a-spin :loading="detailLoading">
              <a-empty v-if="!detailLoading && !detailError && !orderFills.length" description="暂无成交明细" />
              <a-table
                v-else-if="!detailError"
                row-key="fill_id"
                :data="orderFills"
                :pagination="false"
                size="small"
                :scroll="{ x: 'max-content' }"
              >
                <template #columns>
                  <a-table-column title="成交编号" data-index="fill_id" ellipsis />
                  <a-table-column title="价格" data-index="price" />
                  <a-table-column title="数量" data-index="quantity" />
                  <a-table-column title="手续费">
                    <template #cell="{ record }">{{ record.fee }} {{ record.fee_asset }}</template>
                  </a-table-column>
                  <a-table-column title="已实现盈亏" data-index="realized_pnl" />
                  <a-table-column title="成交时间">
                    <template #cell="{ record }">{{ formatTimestamp(record.traded_at) }}</template>
                  </a-table-column>
                </template>
              </a-table>
            </a-spin>
          </div>
        </template>
      </a-drawer>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref, toRef, watch } from "vue";
import { Message } from "@arco-design/web-vue";
import { useRoute, useRouter } from "vue-router";
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
const filterSymbol = toRef(tradeRecordViewState, "filterSymbol");
const orderState = toRef(tradeRecordViewState, "orderState");
const orderTimeRange = toRef(tradeRecordViewState, "orderTimeRange");
const orders = ref<Order[]>([]);
const loading = ref(false);
const orderPagination = reactive({ current: tradeRecordViewState.orderPage, pageSize: 20, total: 0 });
const accountRequests = createLatestRequestGuard();
const orderRequests = createLatestRequestGuard();
const detailRequests = createLatestRequestGuard();
const detailVisible = ref(false);
const detailLoading = ref(false);
const detailError = ref("");
const selectedOrder = ref<Order | null>(null);
const orderFills = ref<Fill[]>([]);
const localMarketTypeLabels: Record<number, string> = { 0: "-", 1: "现货", 2: "合约" };
const localOrderTypeLabels: Record<number, string> = { 0: "-", 1: "市价", 2: "限价" };
const orderStateOptions = Object.entries(orderStateLabels).map(([value, label]) => ({ value, label }));

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
    closeOrderDetail();
    if (tradingAccountId.value) {
      tradingAccountId.value = "";
      orderRequests.invalidate();
      loading.value = false;
      orders.value = [];
    }
    return;
  }
  if (!accounts.value.some(account => account.trading_account_id === requested)) {
    closeOrderDetail();
    tradingAccountId.value = "";
    orderRequests.invalidate();
    loading.value = false;
    orders.value = [];
    await router.replace({ query: { ...route.query, trading_account_id: undefined } });
    Message.warning("账户不存在或无权限");
    return;
  }
  if (tradingAccountId.value === requested) return;
  closeOrderDetail();
  tradingAccountId.value = requested;
  resetPagination();
  await loadOrders();
}

async function accountChanged() {
  closeOrderDetail();
  resetPagination();
  await router.replace({ query: { ...route.query, trading_account_id: tradingAccountId.value || undefined } });
  await loadOrders();
}

function resetPagination() {
  orderPagination.current = 1;
  tradeRecordViewState.orderPage = 1;
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

async function openOrderDetail(order: Order) {
  detailRequests.invalidate();
  selectedOrder.value = order;
  orderFills.value = [];
  detailError.value = "";
  detailVisible.value = true;
  detailLoading.value = true;
  const request = detailRequests.begin();
  try {
    const response = await listFills({
      trading_account_id: order.trading_account_id,
      order_id: order.order_id,
      page: { page: 1, size: 200 }
    });
    if (!request.isLatest() || selectedOrder.value?.order_id !== order.order_id) return;
    orderFills.value = response.fills || [];
  } catch {
    if (request.isLatest() && selectedOrder.value?.order_id === order.order_id) {
      detailError.value = "成交明细加载失败，请稍后重试";
    }
  } finally {
    if (request.isLatest()) detailLoading.value = false;
  }
}

function closeOrderDetail() {
  detailRequests.invalidate();
  detailVisible.value = false;
  detailLoading.value = false;
  selectedOrder.value = null;
  orderFills.value = [];
  detailError.value = "";
}

function changeOrderPage(page: number) {
  orderPagination.current = page;
  tradeRecordViewState.orderPage = page;
  loadOrders();
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
  () => [route.query.trading_account_id, route.query.tab],
  async ([, tab]) => {
    if (tab) {
      await router.replace({ query: { ...route.query, tab: undefined } });
      return;
    }
    await applyRouteAccount();
  }
);

onMounted(async () => {
  if (route.query.tab) {
    await router.replace({ query: { ...route.query, tab: undefined } });
  }
  await loadAccounts();
  await loadOrders();
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
.page-head {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: var(--moox-space-4);
}
.page-head h2 {
  margin: 0;
  color: var(--color-text-1);
  font-size: 24px;
  line-height: 32px;
}
.page-head span {
  display: block;
  margin-top: var(--moox-space-1);
  color: var(--color-text-3);
  font-size: 14px;
  line-height: 22px;
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
.detail-section {
  margin-top: var(--moox-space-6);
}
.detail-section-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: var(--moox-space-3);
}
.detail-section-head h3 {
  margin: 0;
  color: var(--color-text-1);
  font-size: 16px;
  line-height: 24px;
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
