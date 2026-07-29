<template>
  <div class="moox-page">
    <div class="moox-inner">
      <div class="page-head">
        <h2>交易执行</h2>
        <a-space>
          <a-select v-model="exchangeAccountId" placeholder="Exchange 账户" style="width: 260px" @change="accountChanged">
            <a-option v-for="account in accounts" :key="account.exchange_account_id" :value="account.exchange_account_id">
              {{ account.name }} · {{ marketTypeLabels[account.market_type] }}
            </a-option>
          </a-select>
          <a-tag v-if="selectedAccount" :color="accountExecutable ? 'green' : 'orange'">
            {{ accountExecutable ? "Ready" : selectedAccount.paused ? "Paused" : "Not Ready" }}
          </a-tag>
          <a-button title="刷新账户状态" aria-label="刷新账户状态" @click="refreshAccounts">
            <template #icon><icon-refresh /></template>
          </a-button>
        </a-space>
      </div>

      <section class="order-entry">
        <h3>提交订单</h3>
        <a-form :model="form" layout="inline" auto-label-width>
          <a-form-item label="Symbol" required>
            <a-input v-model="form.symbol" style="width: 140px" placeholder="BTCUSDT" />
          </a-form-item>
          <a-form-item label="类型" required>
            <a-radio-group v-model="form.order_type" type="button">
              <a-radio :value="1">MARKET</a-radio>
              <a-radio :value="2">LIMIT</a-radio>
            </a-radio-group>
          </a-form-item>
          <a-form-item label="方向" required>
            <a-radio-group v-model="form.side" type="button">
              <a-radio :value="1">买入</a-radio>
              <a-radio :value="2">卖出</a-radio>
            </a-radio-group>
          </a-form-item>
          <a-form-item label="数量" required>
            <a-input v-model="form.quantity" style="width: 130px" />
          </a-form-item>
          <a-form-item v-if="form.order_type === 2" label="限价" required>
            <a-input v-model="form.limit_price" style="width: 130px" />
          </a-form-item>
          <template v-if="selectedAccount?.market_type === 2">
            <a-form-item label="杠杆" required>
              <a-input v-model="form.leverage" style="width: 90px" />
              <a-button :loading="settingLeverage" @click="saveLeverage">设置</a-button>
            </a-form-item>
            <a-form-item label="只减仓">
              <a-switch v-model="form.reduce_only" />
            </a-form-item>
          </template>
          <a-form-item>
            <a-button type="primary" :loading="submitting" :disabled="!accountExecutable" @click="submit">
              <template #icon><icon-send /></template>
              提交
            </a-button>
          </a-form-item>
        </a-form>
        <a-alert v-if="selectedAccount && !accountExecutable" type="warning" class="entry-warning">
          账户已暂停或尚未 Ready，不能提交新订单。
        </a-alert>
      </section>

      <a-tabs v-model:active-key="activeTab" class="record-view-tabs" @change="loadActiveTab">
        <a-tab-pane key="orders" title="Orders">
          <div class="filter-bar">
            <a-input v-model="filterSymbol" placeholder="Symbol" allow-clear style="width: 150px" @press-enter="loadOrders" />
            <a-checkbox v-model="onlyOpen" @change="loadOrders">仅未完成</a-checkbox>
            <a-button :disabled="!exchangeAccountId" @click="loadOrders">
              <template #icon><icon-search /></template>
              查询
            </a-button>
          </div>
          <a-table
            row-key="order_id"
            :data="orders"
            :loading="loading"
            :pagination="orderPagination"
            :scroll="{ x: 'max-content' }"
            @page-change="changeOrderPage"
          >
            <template #columns>
              <a-table-column title="Symbol" data-index="symbol" />
              <a-table-column title="市场">
                <template #cell="{ record }">{{ marketTypeLabels[record.market_type] }}</template>
              </a-table-column>
              <a-table-column title="方向">
                <template #cell="{ record }">
                  <a-tag :color="orderSideColors[record.side]">{{ orderSideLabels[record.side] }}</a-tag>
                </template>
              </a-table-column>
              <a-table-column title="类型">
                <template #cell="{ record }">{{ orderTypeLabels[record.order_type] }}</template>
              </a-table-column>
              <a-table-column title="限价">
                <template #cell="{ record }">{{ record.limit_price || "-" }}</template>
              </a-table-column>
              <a-table-column title="数量" data-index="quantity" />
              <a-table-column title="已成交" data-index="filled_quantity" />
              <a-table-column title="均价" data-index="average_price" />
              <a-table-column title="状态" data-index="state" />
              <a-table-column title="时间">
                <template #cell="{ record }">{{ formatTimestamp(record.submitted_at || record.created_at) }}</template>
              </a-table-column>
              <a-table-column title="操作" fixed="right">
                <template #cell="{ record }">
                  <a-button v-if="canCancel(record.state)" size="mini" status="danger" @click="cancel(record)"> 撤单 </a-button>
                </template>
              </a-table-column>
            </template>
          </a-table>
        </a-tab-pane>

        <a-tab-pane key="fills" title="Fills">
          <div class="filter-bar">
            <a-input v-model="filterSymbol" placeholder="Symbol" allow-clear style="width: 150px" @press-enter="loadFills" />
            <a-button :disabled="!exchangeAccountId" @click="loadFills">
              <template #icon><icon-search /></template>
              查询
            </a-button>
          </div>
          <a-table
            row-key="fill_id"
            :data="fills"
            :loading="loading"
            :pagination="fillPagination"
            :scroll="{ x: 'max-content' }"
            @page-change="changeFillPage"
          >
            <template #columns>
              <a-table-column title="Fill ID" data-index="fill_id" ellipsis />
              <a-table-column title="Symbol" data-index="symbol" />
              <a-table-column title="市场">
                <template #cell="{ record }">{{ marketTypeLabels[record.market_type] }}</template>
              </a-table-column>
              <a-table-column title="方向">
                <template #cell="{ record }">{{ orderSideLabels[record.side] }}</template>
              </a-table-column>
              <a-table-column title="价格" data-index="price" />
              <a-table-column title="数量" data-index="quantity" />
              <a-table-column title="手续费">
                <template #cell="{ record }">{{ record.fee }} {{ record.fee_asset }}</template>
              </a-table-column>
              <a-table-column title="Realized PnL" data-index="realized_pnl" />
              <a-table-column title="角色" data-index="role" />
              <a-table-column title="成交时间">
                <template #cell="{ record }">{{ formatTimestamp(record.traded_at) }}</template>
              </a-table-column>
            </template>
          </a-table>
        </a-tab-pane>
      </a-tabs>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref, watch } from "vue";
import { Message } from "@arco-design/web-vue";
import { createClientId } from "@/utils/client-id";
import { createLatestRequestGuard } from "@/utils/latest-request";
import {
  canCancelOrderState,
  cancelOrder,
  formatTimestamp,
  listAccounts,
  listFills,
  listOrders,
  marketTypeLabels,
  orderSideColors,
  orderSideLabels,
  orderTypeLabels,
  placeOrder,
  setLeverage
} from "@/api/trade";
import type { ExchangeAccount, Fill, Order } from "@/api/trade/types";

defineOptions({ name: "trade-record" });

const accounts = ref<ExchangeAccount[]>([]);
const exchangeAccountId = ref("");
const selectedAccount = computed(
  () => accounts.value.find(account => account.exchange_account_id === exchangeAccountId.value) || null
);
const accountExecutable = computed(() =>
  Boolean(
    selectedAccount.value?.ready && !selectedAccount.value?.paused && selectedAccount.value?.status.toUpperCase() === "ENABLED"
  )
);
const activeTab = ref("orders");
const filterSymbol = ref("");
const onlyOpen = ref(false);
const orders = ref<Order[]>([]);
const fills = ref<Fill[]>([]);
const loading = ref(false);
const submitting = ref(false);
const settingLeverage = ref(false);
const orderPagination = reactive({ current: 1, pageSize: 20, total: 0 });
const fillPagination = reactive({ current: 1, pageSize: 20, total: 0 });
const accountRequests = createLatestRequestGuard();
const orderRequests = createLatestRequestGuard();
const fillRequests = createLatestRequestGuard();
const form = reactive({
  symbol: "",
  order_type: 1 as 1 | 2,
  side: 1 as 1 | 2,
  quantity: "",
  limit_price: "",
  leverage: "1",
  reduce_only: false
});

watch(
  () => form.order_type,
  value => {
    if (value === 1) form.limit_price = "";
  }
);
watch(selectedAccount, account => {
  if (account?.market_type !== 2) {
    form.leverage = "1";
    form.reduce_only = false;
  }
});

async function loadAccounts() {
  const request = accountRequests.begin();
  const response = await listAccounts({ page: { page: 1, size: 200 } });
  if (!request.isLatest()) return;
  accounts.value = response.accounts || [];
  exchangeAccountId.value ||= accounts.value[0]?.exchange_account_id || "";
}

async function accountChanged() {
  orderPagination.current = 1;
  fillPagination.current = 1;
  await loadActiveTab();
}

async function refreshAccounts() {
  await loadAccounts();
  await loadActiveTab();
}

async function loadOrders() {
  if (!exchangeAccountId.value) return;
  const request = orderRequests.begin();
  const accountId = exchangeAccountId.value;
  const query = {
    exchange_account_id: accountId,
    symbol: filterSymbol.value.trim().toUpperCase(),
    only_open: onlyOpen.value,
    page: { page: orderPagination.current, size: orderPagination.pageSize }
  };
  loading.value = true;
  try {
    const response = await listOrders(query);
    if (!request.isLatest() || exchangeAccountId.value !== accountId) return;
    orders.value = response.orders || [];
    orderPagination.total = response.page_result?.total || 0;
  } finally {
    if (request.isLatest()) loading.value = false;
  }
}

async function loadFills() {
  if (!exchangeAccountId.value) return;
  const request = fillRequests.begin();
  const accountId = exchangeAccountId.value;
  const query = {
    exchange_account_id: accountId,
    symbol: filterSymbol.value.trim().toUpperCase(),
    page: { page: fillPagination.current, size: fillPagination.pageSize }
  };
  loading.value = true;
  try {
    const response = await listFills(query);
    if (!request.isLatest() || exchangeAccountId.value !== accountId) return;
    fills.value = response.fills || [];
    fillPagination.total = response.page_result?.total || 0;
  } finally {
    if (request.isLatest()) loading.value = false;
  }
}

function loadActiveTab() {
  return activeTab.value === "fills" ? loadFills() : loadOrders();
}

function changeOrderPage(page: number) {
  orderPagination.current = page;
  loadOrders();
}

function changeFillPage(page: number) {
  fillPagination.current = page;
  loadFills();
}

async function submit() {
  const symbol = form.symbol.trim().toUpperCase();
  if (!accountExecutable.value || !symbol || !form.quantity.trim()) {
    Message.warning("请选择 Ready 账户并填写 Symbol 和数量");
    return;
  }
  if (form.order_type === 2 && !form.limit_price.trim()) {
    Message.warning("LIMIT 订单必须填写限价");
    return;
  }
  submitting.value = true;
  try {
    await placeOrder({
      exchange_account_id: exchangeAccountId.value,
      client_order_id: createClientId(),
      symbol,
      order_type: form.order_type,
      time_in_force: form.order_type === 1 ? 0 : 1,
      side: form.side,
      position_side: selectedAccount.value?.market_type === 2 ? 1 : 0,
      quantity: form.quantity.trim(),
      ...(form.order_type === 2 ? { limit_price: form.limit_price.trim() } : {}),
      reduce_only: selectedAccount.value?.market_type === 2 && form.reduce_only,
      source: "manual"
    });
    Message.success("订单已提交");
    await loadOrders();
  } finally {
    submitting.value = false;
  }
}

async function saveLeverage() {
  const symbol = form.symbol.trim().toUpperCase();
  if (!selectedAccount.value || selectedAccount.value.market_type !== 2 || !symbol || !form.leverage.trim()) {
    Message.warning("请先选择 SWAP 账户并填写 Symbol 和杠杆");
    return;
  }
  settingLeverage.value = true;
  try {
    await setLeverage({
      exchange_account_id: exchangeAccountId.value,
      symbol,
      leverage: form.leverage.trim()
    });
    selectedAccount.value.ready = false;
    Message.success("杠杆已保存；Exchange session 重建并恢复 Ready 后再提交订单");
  } finally {
    settingLeverage.value = false;
  }
}

const canCancel = canCancelOrderState;

async function cancel(order: Order) {
  if (order.exchange_account_id !== exchangeAccountId.value) {
    Message.warning("账户已切换，请刷新后重试");
    return;
  }
  await cancelOrder(order.order_id);
  Message.success("撤单请求已提交");
  await loadOrders();
}

onMounted(async () => {
  await loadAccounts();
  await loadOrders();
});
</script>

<style scoped>
.page-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: var(--moox-space-4);
}

.order-entry {
  padding: var(--moox-space-4) 0;
  border-top: 1px solid var(--color-border-2);
  border-bottom: 1px solid var(--color-border-2);
}

.order-entry h3 {
  margin: 0 0 var(--moox-space-3);
}

.entry-warning {
  margin-top: var(--moox-space-3);
}

.filter-bar {
  display: flex;
  gap: var(--moox-space-3);
  align-items: center;
  margin-bottom: var(--moox-space-2);
}

.record-view-tabs {
  margin-bottom: var(--moox-space-3);
}
</style>
