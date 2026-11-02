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
          <a-select
            v-model="logicalAccountId"
            allow-clear
            placeholder="按组合账户查看"
            class="account-select"
            @change="logicalChanged"
          >
            <a-option v-for="logical in logicalAccounts" :key="logical.logical_account_id" :value="logical.logical_account_id">{{
              logical.name
            }}</a-option>
          </a-select>
          <a-tag v-if="selectedAccount" :color="selectedAccount.ready ? 'green' : 'orange'">
            {{ selectedAccount.ready ? "就绪" : "未就绪" }}
          </a-tag>
          <a-input v-model="filterSymbol" placeholder="交易标的" allow-clear class="symbol-input" @press-enter="searchOrders" />
          <a-select v-model="orderState" allow-clear placeholder="订单状态" class="state-select">
            <a-option v-for="item in orderStateOptions" :key="item.value" :value="item.value">{{ item.label }}</a-option>
          </a-select>
          <a-range-picker v-model="orderTimeRange" value-format="timestamp" class="time-range" @change="searchOrders" />
          <a-button type="primary" :disabled="!tradingAccountId && !logicalAccountId" @click="searchOrders">
            <template #icon><icon-search /></template>
            查询
          </a-button>
        </a-space>
        <a-radio-group v-model="recordTab" type="button" size="small" class="record-tabs" @change="searchOrders">
          <a-radio value="orders">订单</a-radio>
          <a-radio value="fills">成交</a-radio>
        </a-radio-group>
        <div v-if="recordTab === 'fills'" class="orders-table-region">
          <a-table
            row-key="fill_id"
            size="small"
            :bordered="{ cell: true }"
            :data="fills"
            :loading="loading"
            :pagination="orderPagination"
            :scroll="{ x: 'max-content' }"
            @page-change="changeOrderPage"
          >
            <template #columns>
              <a-table-column title="交易标的" data-index="instrument_id" />
              <a-table-column title="执行账户" data-index="trading_account_id" />
              <a-table-column title="方向">
                <template #cell="{ record }">{{ orderSideLabels[record.side] }}</template>
              </a-table-column>
              <a-table-column title="价格" data-index="price" />
              <a-table-column title="数量" data-index="quantity" />
              <a-table-column title="手续费">
                <template #cell="{ record }">{{ record.fee }} {{ record.fee_asset }}</template>
              </a-table-column>
              <a-table-column title="成交时间">
                <template #cell="{ record }">{{ formatTimestamp(record.traded_at) }}</template>
              </a-table-column>
            </template>
          </a-table>
        </div>
        <div v-else class="orders-table-region">
          <a-empty v-if="!loading && !orders.length" class="orders-empty-state" description="暂无订单" />
          <a-table
            v-else
            row-key="order_id"
            size="small"
            :bordered="{ cell: true }"
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
              <a-table-column title="来源">
                <template #cell="{ record }">{{ orderOwnerLabels[record.owner_type] || "-" }}</template>
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
                    <a-button
                      v-if="canCancel(record.state) && record.logical_account_id"
                      size="mini"
                      type="text"
                      status="danger"
                      @click="openCancel(record)"
                      >撤单</a-button
                    >
                    <a-tooltip v-else-if="canCancel(record.state)" content="外部订单请到交易所处理">
                      <span class="muted">外部订单</span>
                    </a-tooltip>
                  </a-space>
                </template>
              </a-table-column>
            </template>
          </a-table>
        </div>
      </section>

      <a-modal
        v-model:visible="cancelVisible"
        width="min(480px, calc(100vw - 24px))"
        title="撤单确认"
        ok-text="确认撤单"
        :on-before-ok="confirmCancel"
      >
        <a-alert v-if="cancelPausesAccount" type="warning" show-icon class="cancel-alert">撤单会暂停整个组合账户。</a-alert>
        <a-alert v-if="cancelError" type="error" show-icon class="cancel-alert">{{ cancelError }}</a-alert>
        <a-form :model="cancelForm" auto-label-width>
          <a-form-item label="原因" required><a-input v-model="cancelForm.reason" /></a-form-item>
        </a-form>
      </a-modal>

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
            <a-descriptions-item label="来源">{{ orderOwnerLabels[selectedOrder.owner_type] || "-" }}</a-descriptions-item>
            <a-descriptions-item label="参考价">{{ selectedOrder.reference_price || "-" }}</a-descriptions-item>
            <a-descriptions-item v-if="selectedOrder.reject_reason" label="失败原因" :span="2">{{
              selectedOrder.reject_reason
            }}</a-descriptions-item>
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
                :bordered="{ cell: true }"
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
import { useSpaceStore } from "@/store/modules/space";
import { createClientId } from "@/utils/client-id";
import { createLatestRequestGuard } from "@/utils/latest-request";
import {
  canCancelOrderState,
  cancelOrder,
  getLogicalAccount,
  orderOwnerLabels,
  formatTimestamp,
  listLogicalAccounts,
  listTradingAccounts,
  listFills,
  listOrders,
  orderSideColors,
  orderSideLabels,
  orderStateLabels
} from "@/api/trade";
import type { TradingAccount, Fill, LogicalAccount, Order } from "@/api/trade/types";
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
const fills = ref<Fill[]>([]);
const recordTab = ref<"orders" | "fills">("orders");
const logicalAccounts = ref<LogicalAccount[]>([]);
const logicalAccountId = ref("");
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
  void listLogicalAccounts({ page: 1, size: 500 })
    .then(rsp => {
      logicalAccounts.value = rsp.logical_accounts || [];
    })
    .catch(() => undefined);
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

async function logicalChanged() {
  closeOrderDetail();
  resetPagination();
  await loadOrders();
}

async function loadOrders() {
  if (!tradingAccountId.value && !logicalAccountId.value) {
    loading.value = false;
    orders.value = [];
    return;
  }
  const request = orderRequests.begin();
  // 选了组合账户时查全部成员的记录。
  const scope = logicalAccountId.value
    ? { logical_account_id: logicalAccountId.value }
    : { trading_account_id: tradingAccountId.value };
  if (recordTab.value === "fills") {
    loading.value = true;
    try {
      const response = await listFills({
        ...scope,
        instrument_id: filterSymbol.value.trim().toUpperCase(),
        ...rangeToTime(orderTimeRange.value),
        page: { page: orderPagination.current, size: orderPagination.pageSize }
      });
      if (!request.isLatest()) return;
      fills.value = response.fills || [];
      orderPagination.total = response.page_result?.total || 0;
    } finally {
      if (request.isLatest()) loading.value = false;
    }
    return;
  }
  const query = {
    ...scope,
    instrument_id: filterSymbol.value.trim().toUpperCase(),
    state: orderState.value || undefined,
    ...rangeToTime(orderTimeRange.value),
    page: { page: orderPagination.current, size: orderPagination.pageSize }
  };
  loading.value = true;
  try {
    const response = await listOrders(query);
    if (!request.isLatest()) return;
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

const cancelVisible = ref(false);
const cancelError = ref("");
const cancelPausesAccount = ref(false);
const cancelForm = reactive({ order_id: "", action_id: "", reason: "" });

function openCancel(order: Order) {
  if (order.trading_account_id !== tradingAccountId.value) {
    Message.warning("账户已切换，请重新查询后重试");
    return;
  }
  // 操作编号在弹窗打开时生成，失败重试沿用同一个编号。
  Object.assign(cancelForm, { order_id: order.order_id, action_id: createClientId(), reason: "控制台手动撤单" });
  cancelError.value = "";
  cancelPausesAccount.value = order.owner_type === "TARGET";
  cancelVisible.value = true;
  getLogicalAccount(order.logical_account_id)
    .then(response => {
      if (cancelForm.order_id === order.order_id) cancelPausesAccount.value = response.logical_account.control_mode === 1;
    })
    .catch(() => undefined);
}

async function confirmCancel() {
  if (!cancelForm.reason.trim()) {
    cancelError.value = "请填写撤单原因";
    return false;
  }
  cancelError.value = "";
  try {
    await cancelOrder(cancelForm.action_id, cancelForm.order_id, cancelForm.reason.trim());
  } catch (error) {
    cancelError.value = error instanceof Error ? error.message : "撤单失败，请重试";
    return false;
  }
  Message.success("撤单请求已提交");
  void loadOrders();
  return true;
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

// 切换空间：账户、订单和详情都属于旧空间，清空后加载新空间的账户与订单（KeepAlive 恢复的页面同样走这条监听）。
const spaceStore = useSpaceStore();
watch(
  () => spaceStore.selectedSpaceId,
  (next, previous) => {
    if (!next || next === previous) return;
    orderRequests.invalidate();
    accountRequests.invalidate();
    closeOrderDetail();
    accounts.value = [];
    accountsLoaded.value = false;
    tradingAccountId.value = "";
    orders.value = [];
    loading.value = false;
    resetPagination();
    void (async () => {
      if (route.query.trading_account_id) await router.replace({ query: { ...route.query, trading_account_id: undefined } });
      await loadAccounts();
      await loadOrders();
    })();
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
.record-tabs {
  margin-bottom: 8px;
}
.cancel-alert {
  margin-bottom: 12px;
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
