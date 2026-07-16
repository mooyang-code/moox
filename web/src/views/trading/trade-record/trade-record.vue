<template>
  <div class="moox-page">
    <div class="moox-inner">
      <PageTitleTabs
        class="record-view-tabs"
        :model-value="activeTab"
        :items="tabs"
        aria-label="订单与成交"
        @change="switchTab"
      />
      <template v-if="activeTab === 'orders'">
        <!-- 订单列表 -->
        <section>
          <div v-if="accounts.length" class="account-tabs-block">
            <div
              class="record-account-tabs order-account-tabs"
              :class="{ expanded: orderAccountTabsExpanded }"
              role="tablist"
              aria-label="订单账户"
            >
              <button
                v-for="acc in accounts"
                :key="acc.account_id"
                type="button"
                class="record-account-tab"
                :class="{ active: orderFilter.account_id === acc.account_id }"
                role="tab"
                :aria-selected="orderFilter.account_id === acc.account_id"
                @click="onOrderAccountTabClick(acc.account_id)"
              >
                <span class="account-tab-name">{{ acc.account_name }}</span>
                <span class="account-tab-type">{{ accountTypeLabels[acc.account_type] || acc.account_type }}</span>
              </button>
            </div>
            <a-button
              class="account-tabs-toggle"
              size="mini"
              type="text"
              :aria-label="orderAccountTabsExpanded ? '收起账户' : '展开账户'"
              :title="orderAccountTabsExpanded ? '收起账户' : '展开账户'"
              @click="toggleOrderAccountTabs"
            >
              <template #icon>
                <icon-up v-if="orderAccountTabsExpanded" />
                <icon-down v-else />
              </template>
            </a-button>
          </div>
          <a-empty v-else description="暂无账户" />

          <div class="filter-bar">
            <a-input v-model="orderFilter.symbol" placeholder="交易对" style="width: 140px" allow-clear @press-enter="loadOrders" />
            <a-select v-model="orderFilter.status" placeholder="状态" style="width: 120px" allow-clear @change="loadOrders">
              <a-option v-for="(label, val) in orderStatusLabels" :key="val" :value="Number(val)">{{ label }}</a-option>
            </a-select>
            <a-checkbox v-model="orderFilter.only_open" @change="loadOrders">仅显示未完成</a-checkbox>
            <a-button type="primary" size="small" :disabled="!orderFilter.account_id" @click="loadOrders">
              <template #icon><icon-search /></template>
              查询
            </a-button>
            <a-button size="small" :loading="syncingOrders" :disabled="!orderFilter.account_id" @click="onSyncOrders">
              <template #icon><icon-sync /></template>
              同步订单
            </a-button>
          </div>

          <a-table
            row-key="order_id"
            size="small"
            :bordered="{ cell: true }"
            :loading="orderLoading"
            :data="orders"
            :pagination="orderPagination"
            :scroll="{ x: 'max-content' }"
            @page-change="onOrderPageChange"
            @page-size-change="onOrderPageSizeChange"
          >
            <template #columns>
              <a-table-column title="账户" :width="100">
                <template #cell="{ record }">{{ accountName(record.account_id) }}</template>
              </a-table-column>
              <a-table-column title="交易所" data-index="exchange" :width="80" />
              <a-table-column title="交易对" data-index="symbol" :width="110" />
              <a-table-column title="方向" :width="60">
                <template #cell="{ record }">
                  <a-tag size="small" :color="orderSideColors[record.side]">{{ orderSideLabels[record.side] }}</a-tag>
                </template>
              </a-table-column>
              <a-table-column title="类型" :width="70">
                <template #cell="{ record }">{{ orderTypeLabels[record.order_type] || record.order_type }}</template>
              </a-table-column>
              <a-table-column title="价格" data-index="price" :width="100" />
              <a-table-column title="数量" data-index="quantity" :width="100" />
              <a-table-column title="已成交" data-index="filled_qty" :width="100" />
              <a-table-column title="均价" data-index="avg_price" :width="100" />
              <a-table-column title="状态" :width="80">
                <template #cell="{ record }">
                  <a-tag size="small" :color="orderStatusColors[record.status]">{{ orderStatusLabels[record.status] }}</a-tag>
                </template>
              </a-table-column>
              <a-table-column title="委托时间" :width="170">
                <template #cell="{ record }">{{ formatTimestamp(record.submitted_at || record.created_at) }}</template>
              </a-table-column>
              <a-table-column title="操作" :width="140" align="center" fixed="right">
                <template #cell="{ record }">
                  <a-space>
                    <a-button
                      v-if="canCancel(record.status)"
                      size="mini"
                      type="text"
                      status="danger"
                      @click="onCancelOrder(record)"
                    >撤单</a-button>
                    <a-button size="mini" type="text" @click="openOrderTrades(record)">成交</a-button>
                  </a-space>
                </template>
              </a-table-column>
            </template>
          </a-table>
        </section>
      </template>

      <template v-else>
        <!-- 成交明细 -->
        <section>
          <div v-if="accounts.length" class="account-tabs-block">
            <div
              class="record-account-tabs trade-account-tabs"
              :class="{ expanded: tradeAccountTabsExpanded }"
              role="tablist"
              aria-label="成交账户"
            >
              <button
                v-for="acc in accounts"
                :key="acc.account_id"
                type="button"
                class="record-account-tab"
                :class="{ active: tradeFilter.account_id === acc.account_id }"
                role="tab"
                :aria-selected="tradeFilter.account_id === acc.account_id"
                @click="onTradeAccountTabClick(acc.account_id)"
              >
                <span class="account-tab-name">{{ acc.account_name }}</span>
                <span class="account-tab-type">{{ accountTypeLabels[acc.account_type] || acc.account_type }}</span>
              </button>
            </div>
            <a-button
              class="account-tabs-toggle"
              size="mini"
              type="text"
              :aria-label="tradeAccountTabsExpanded ? '收起账户' : '展开账户'"
              :title="tradeAccountTabsExpanded ? '收起账户' : '展开账户'"
              @click="toggleTradeAccountTabs"
            >
              <template #icon>
                <icon-up v-if="tradeAccountTabsExpanded" />
                <icon-down v-else />
              </template>
            </a-button>
          </div>
          <a-empty v-else description="暂无账户" />

          <div class="filter-bar">
            <a-input v-model="tradeFilter.symbol" placeholder="交易对" style="width: 140px" allow-clear @press-enter="loadTrades" />
            <a-button type="primary" size="small" :disabled="!tradeFilter.account_id" @click="loadTrades">
              <template #icon><icon-search /></template>
              查询
            </a-button>
            <a-button size="small" :loading="syncingTrades" :disabled="!tradeFilter.account_id" @click="onSyncTrades">
              <template #icon><icon-sync /></template>
              同步成交
            </a-button>
          </div>

          <a-table
            row-key="trade_id"
            size="small"
            :bordered="{ cell: true }"
            :loading="tradeLoading"
            :data="trades"
            :pagination="tradePagination"
            :scroll="{ x: 'max-content' }"
            @page-change="onTradePageChange"
            @page-size-change="onTradePageSizeChange"
          >
            <template #columns>
              <a-table-column title="账户" :width="100">
                <template #cell="{ record }">{{ accountName(record.account_id) }}</template>
              </a-table-column>
              <a-table-column title="交易所" data-index="exchange" :width="80" />
              <a-table-column title="交易对" data-index="symbol" :width="110" />
              <a-table-column title="方向" :width="60">
                <template #cell="{ record }">
                  <a-tag size="small" :color="orderSideColors[record.side]">{{ orderSideLabels[record.side] }}</a-tag>
                </template>
              </a-table-column>
              <a-table-column title="成交价" data-index="price" :width="100" />
              <a-table-column title="成交量" data-index="quantity" :width="100" />
              <a-table-column title="成交额" data-index="amount" :width="110" />
              <a-table-column title="手续费" :width="110">
                <template #cell="{ record }">{{ record.fee }} {{ record.fee_currency }}</template>
              </a-table-column>
              <a-table-column title="角色" data-index="role" :width="70" />
              <a-table-column title="订单ID" data-index="order_id" :width="160" ellipsis />
              <a-table-column title="成交时间" :width="170">
                <template #cell="{ record }">{{ formatTimestamp(record.traded_at) }}</template>
              </a-table-column>
            </template>
          </a-table>
        </section>
      </template>

      <!-- 订单成交明细弹窗 -->
      <a-modal v-model:visible="orderTradesVisible" width="900px" :title="`订单成交明细 - ${selectedOrder?.symbol || ''}`" :footer="false">
        <a-table
          row-key="trade_id"
          size="small"
          :bordered="{ cell: true }"
          :loading="orderTradesLoading"
          :data="orderTrades"
          :pagination="false"
        >
          <template #columns>
            <a-table-column title="成交ID" data-index="trade_id" :width="160" ellipsis />
            <a-table-column title="方向" :width="60">
              <template #cell="{ record }">
                <a-tag size="small" :color="orderSideColors[record.side]">{{ orderSideLabels[record.side] }}</a-tag>
              </template>
            </a-table-column>
            <a-table-column title="成交价" data-index="price" :width="100" />
            <a-table-column title="成交量" data-index="quantity" :width="100" />
            <a-table-column title="成交额" data-index="amount" :width="110" />
            <a-table-column title="手续费" :width="110">
              <template #cell="{ record }">{{ record.fee }} {{ record.fee_currency }}</template>
            </a-table-column>
            <a-table-column title="角色" data-index="role" :width="70" />
            <a-table-column title="成交时间" :width="170">
              <template #cell="{ record }">{{ formatTimestamp(record.traded_at) }}</template>
            </a-table-column>
          </template>
        </a-table>
      </a-modal>
    </div>
  </div>
</template>

<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue';
import { Message } from '@arco-design/web-vue';
import PageTitleTabs from '@/components/page-title-tabs/index.vue';
import {
  listAccounts, listOrders, syncOrders, listTrades, syncTrades, cancelOrder,
  accountTypeLabels,
  orderSideLabels, orderSideColors, orderTypeLabels,
  orderStatusLabels, orderStatusColors,
  formatTimestamp,
} from '@/api/trade';
import type { Account, Order, OrderStatus, Trade } from '@/api/trade/types';
import { defaultPagination, applyPageResult } from '@/views/data/shared/metadata-utils';

defineOptions({ name: 'trade-record' });

type TradeRecordTab = 'orders' | 'trades';

const activeTab = ref<TradeRecordTab>('orders');
const tabs = [
  { key: 'orders', label: '订单记录' },
  { key: 'trades', label: '成交明细' },
] as const;
const accounts = ref<Account[]>([]);
const orderAccountTabsExpanded = ref(false);
const tradeAccountTabsExpanded = ref(false);

function switchTab(key: string) {
  activeTab.value = key === 'trades' ? 'trades' : 'orders';
}

async function loadAccounts() {
  const rsp = await listAccounts({ page: { page: 1, size: 200 } });
  accounts.value = rsp.accounts || [];
}

function defaultAccountID(): string {
  return accounts.value.find((acc) => acc.is_default)?.account_id || accounts.value[0]?.account_id || '';
}

function accountName(accountId: string): string {
  const acc = accounts.value.find((a) => a.account_id === accountId);
  return acc ? acc.account_name : accountId || '-';
}

function canCancel(status: number): boolean {
  return [0, 1, 2].includes(status);
}

function toggleOrderAccountTabs() {
  orderAccountTabsExpanded.value = !orderAccountTabsExpanded.value;
}

function toggleTradeAccountTabs() {
  tradeAccountTabsExpanded.value = !tradeAccountTabsExpanded.value;
}

// ========== 订单列表 ==========
const orders = ref<Order[]>([]);
const orderLoading = ref(false);
const syncingOrders = ref(false);
const orderPagination = reactive(defaultPagination());
const orderFilter = reactive({
  account_id: '',
  symbol: '',
  status: undefined as OrderStatus | undefined,
  only_open: false,
});

async function loadOrders() {
  if (!orderFilter.account_id) {
    orders.value = [];
    return;
  }
  orderLoading.value = true;
  try {
    const rsp = await listOrders({
      account_id: orderFilter.account_id,
      symbol: orderFilter.symbol || undefined,
      status: orderFilter.status,
      only_open: orderFilter.only_open || undefined,
      page: { page: orderPagination.current, size: orderPagination.pageSize },
    });
    orders.value = rsp.orders || [];
    applyPageResult(orderPagination, rsp.page_result);
  } finally {
    orderLoading.value = false;
  }
}

async function onOrderAccountTabClick(accountID: string) {
  if (!accountID || orderFilter.account_id === accountID) {
    return;
  }
  orderFilter.account_id = accountID;
  orderPagination.current = 1;
  await loadOrders();
}

async function onSyncOrders() {
  if (!orderFilter.account_id) {
    Message.warning('请先选择账户');
    return;
  }
  const symbol = orderFilter.symbol.trim().toUpperCase();
  const syncOnlyOpen = orderFilter.only_open || !symbol;
  syncingOrders.value = true;
  try {
    const rsp = await syncOrders({
      account_id: orderFilter.account_id,
      symbol: symbol || undefined,
      only_open: syncOnlyOpen,
      page: { page: orderPagination.current, size: orderPagination.pageSize },
    });
    orders.value = rsp.orders || [];
    applyPageResult(orderPagination, rsp.page_result);
    orderFilter.symbol = symbol;
    if (syncOnlyOpen && !symbol) {
      orderFilter.only_open = true;
    }
    Message.success(`已同步 ${orders.value.length} 条订单`);
  } finally {
    syncingOrders.value = false;
  }
}

function onOrderPageChange(page: number) {
  orderPagination.current = page;
  loadOrders();
}

function onOrderPageSizeChange(size: number) {
  orderPagination.current = 1;
  orderPagination.pageSize = size;
  loadOrders();
}

async function onCancelOrder(record: Order) {
  await cancelOrder({
    account_id: record.account_id,
    channel_id: record.channel_id,
    order_id: record.order_id,
  });
  Message.success('撤单请求已提交');
  await loadOrders();
}

// ========== 成交明细 ==========
const trades = ref<Trade[]>([]);
const tradeLoading = ref(false);
const syncingTrades = ref(false);
const tradePagination = reactive(defaultPagination());
const tradeFilter = reactive({ account_id: '', symbol: '' });

async function loadTrades() {
  if (!tradeFilter.account_id) {
    trades.value = [];
    return;
  }
  tradeLoading.value = true;
  try {
    const rsp = await listTrades({
      account_id: tradeFilter.account_id,
      symbol: tradeFilter.symbol || undefined,
      page: { page: tradePagination.current, size: tradePagination.pageSize },
    });
    trades.value = rsp.trades || [];
    applyPageResult(tradePagination, rsp.page_result);
  } finally {
    tradeLoading.value = false;
  }
}

async function onTradeAccountTabClick(accountID: string) {
  if (!accountID || tradeFilter.account_id === accountID) {
    return;
  }
  tradeFilter.account_id = accountID;
  tradePagination.current = 1;
  await loadTrades();
}

async function onSyncTrades() {
  if (!tradeFilter.account_id) {
    Message.warning('请先选择账户');
    return;
  }
  const symbol = tradeFilter.symbol.trim().toUpperCase();
  if (!symbol) {
    Message.warning('请先输入交易对');
    return;
  }
  syncingTrades.value = true;
  try {
    const rsp = await syncTrades({
      account_id: tradeFilter.account_id,
      symbol,
      page: { page: tradePagination.current, size: tradePagination.pageSize },
    });
    trades.value = rsp.trades || [];
    applyPageResult(tradePagination, rsp.page_result);
    tradeFilter.symbol = symbol;
    Message.success(`已同步 ${trades.value.length} 条成交`);
  } finally {
    syncingTrades.value = false;
  }
}

function onTradePageChange(page: number) {
  tradePagination.current = page;
  loadTrades();
}

function onTradePageSizeChange(size: number) {
  tradePagination.current = 1;
  tradePagination.pageSize = size;
  loadTrades();
}

// ========== 订单成交明细弹窗 ==========
const orderTradesVisible = ref(false);
const orderTrades = ref<Trade[]>([]);
const orderTradesLoading = ref(false);
const selectedOrder = ref<Order | null>(null);

async function openOrderTrades(record: Order) {
  selectedOrder.value = record;
  orderTradesVisible.value = true;
  orderTradesLoading.value = true;
  try {
    const rsp = await listTrades({
      account_id: record.account_id,
      order_id: record.order_id,
    });
    orderTrades.value = rsp.trades || [];
  } finally {
    orderTradesLoading.value = false;
  }
}

onMounted(async () => {
  await loadAccounts();
  const accountID = defaultAccountID();
  if (accountID) {
    orderFilter.account_id = accountID;
    tradeFilter.account_id = accountID;
    await Promise.all([loadOrders(), loadTrades()]);
  }
});
</script>

<style scoped>
.record-view-tabs {
  margin-bottom: 12px;
}

.account-tabs-block {
  position: relative;
  padding-bottom: 12px;
  margin-bottom: 12px;
}

.record-account-tabs {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
  max-height: 42px;
  padding: 4px;
  overflow: hidden;
  border: 1px solid var(--color-border-2);
  border-radius: 6px;
  background: var(--color-fill-1);
}

.record-account-tabs.expanded {
  max-height: none;
}

.record-account-tab {
  display: inline-flex;
  flex: 0 0 auto;
  align-items: center;
  gap: 8px;
  min-height: 32px;
  padding: 5px 12px;
  color: var(--color-text-2);
  white-space: nowrap;
  cursor: pointer;
  background: transparent;
  border: 1px solid transparent;
  border-radius: 4px;
  outline: none;
  transition: background-color 0.15s ease, border-color 0.15s ease, color 0.15s ease;
}

.record-account-tab:hover {
  color: rgb(var(--primary-6));
  background: var(--color-fill-2);
}

.record-account-tab.active {
  color: rgb(var(--primary-6));
  background: var(--color-bg-1);
  border-color: rgb(var(--primary-6));
}

.account-tab-name {
  max-width: 180px;
  overflow: hidden;
  font-weight: 600;
  text-overflow: ellipsis;
}

.account-tab-type {
  color: var(--color-text-3);
  font-size: 12px;
}

.account-tabs-toggle {
  position: absolute;
  bottom: -9px;
  left: 50%;
  z-index: 2;
  width: 34px;
  min-width: 34px;
  height: 18px;
  min-height: 18px;
  padding: 0;
  color: rgb(var(--primary-6));
  background: var(--color-bg-1);
  border: 1px solid var(--color-border-2);
  border-radius: 999px;
  box-shadow: 0 1px 4px rgba(15, 23, 42, 0.1);
  transform: translateX(-50%);
}

.account-tabs-toggle:hover {
  background: var(--color-fill-2);
}

.account-tabs-toggle :deep(.arco-btn-icon) {
  margin: 0;
  font-size: 12px;
}

.filter-bar {
  display: flex;
  gap: 8px;
  align-items: center;
  margin-bottom: 8px;
  flex-wrap: wrap;
}

</style>
