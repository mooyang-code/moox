<template>
  <div class="moox-page">
    <div class="moox-inner">
      <SpaceContextBar />

      <a-tabs v-model:active-key="activeTab" type="rounded">
        <!-- 订单列表 -->
        <a-tab-pane key="orders" title="订单记录">
          <div class="filter-bar">
            <a-select
              v-model="orderFilter.account_id"
              placeholder="选择账户"
              style="width: 220px"
              allow-clear
              @change="loadOrders"
            >
              <a-option v-for="acc in accounts" :key="acc.account_id" :value="acc.account_id">
                {{ acc.account_name }} ({{ accountTypeLabels[acc.account_type] }})
              </a-option>
            </a-select>
            <a-input v-model="orderFilter.symbol" placeholder="交易对" style="width: 140px" allow-clear @press-enter="loadOrders" />
            <a-select v-model="orderFilter.status" placeholder="状态" style="width: 120px" allow-clear @change="loadOrders">
              <a-option v-for="(label, val) in orderStatusLabels" :key="val" :value="Number(val)">{{ label }}</a-option>
            </a-select>
            <a-checkbox v-model="orderFilter.only_open" @change="loadOrders">仅显示未完成</a-checkbox>
            <a-button type="primary" size="small" @click="loadOrders">查询</a-button>
            <a-button size="small" @click="loadOrders">
              <template #icon><icon-refresh /></template>
              刷新
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
        </a-tab-pane>

        <!-- 成交明细 -->
        <a-tab-pane key="trades" title="成交明细">
          <div class="filter-bar">
            <a-select
              v-model="tradeFilter.account_id"
              placeholder="选择账户"
              style="width: 220px"
              allow-clear
              @change="loadTrades"
            >
              <a-option v-for="acc in accounts" :key="acc.account_id" :value="acc.account_id">
                {{ acc.account_name }} ({{ accountTypeLabels[acc.account_type] }})
              </a-option>
            </a-select>
            <a-input v-model="tradeFilter.symbol" placeholder="交易对" style="width: 140px" allow-clear @press-enter="loadTrades" />
            <a-button type="primary" size="small" @click="loadTrades">查询</a-button>
            <a-button size="small" @click="loadTrades">
              <template #icon><icon-refresh /></template>
              刷新
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
        </a-tab-pane>
      </a-tabs>

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
import SpaceContextBar from '@/components/SpaceContextBar/index.vue';
import {
  listAccounts, listOrders, listTrades, cancelOrder,
  accountTypeLabels,
  orderSideLabels, orderSideColors, orderTypeLabels,
  orderStatusLabels, orderStatusColors,
  formatTimestamp,
} from '@/api/trade';
import type { Account, Order, OrderStatus, Trade } from '@/api/trade/types';
import { defaultPagination, applyPageResult } from '@/views/data/shared/metadata-utils';

defineOptions({ name: 'trade-record' });

const activeTab = ref('orders');
const accounts = ref<Account[]>([]);

async function loadAccounts() {
  const rsp = await listAccounts({ page: { page: 1, size: 200 } });
  accounts.value = rsp.accounts || [];
}

function accountName(accountId: string): string {
  const acc = accounts.value.find((a) => a.account_id === accountId);
  return acc ? acc.account_name : accountId || '-';
}

function canCancel(status: number): boolean {
  return [0, 1, 2].includes(status);
}

// ========== 订单列表 ==========
const orders = ref<Order[]>([]);
const orderLoading = ref(false);
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

onMounted(loadAccounts);
</script>

<style scoped>
.filter-bar {
  display: flex;
  gap: 8px;
  align-items: center;
  margin-bottom: 16px;
  flex-wrap: wrap;
}
</style>
