<template>
  <SectionBlock title="订单" flush>
    <div class="toolbar">
      <a-radio-group v-model="range" type="button" @change="search">
        <a-radio v-for="item in recordRanges" :key="item.key" :value="item.key">{{ item.label }}</a-radio>
      </a-radio-group>
      <a-select v-model="state" allow-clear placeholder="订单状态" style="width: 150px" @change="search">
        <a-option v-for="(label, value) in orderStateLabels" :key="value" :value="value">{{ label }}</a-option>
      </a-select>
      <a-select v-model="ownerType" allow-clear placeholder="订单来源" style="width: 120px" @change="search">
        <a-option v-for="(label, value) in orderOwnerLabels" :key="value" :value="value">{{ label }}</a-option>
      </a-select>
      <a-select
        v-if="memberOptions.length > 1"
        v-model="accountId"
        allow-clear
        placeholder="执行账户"
        style="width: 150px"
        @change="search"
      >
        <a-option v-for="item in memberOptions" :key="item.id" :value="item.id">{{ item.label }}</a-option>
      </a-select>
      <a-select v-if="marketOptions.length > 1" v-model="market" :disabled="!symbol.trim()" style="width: 110px" @change="search">
        <a-option v-for="item in marketOptions" :key="item" :value="item">{{ marketLabel(item) }}</a-option>
      </a-select>
      <a-input v-model="symbol" allow-clear placeholder="币，如 BTC" style="width: 150px" @press-enter="search" @clear="search" />
      <a-button :loading="loading" @click="search">查询</a-button>
    </div>
    <a-alert v-if="error" type="error" class="alert">{{ error }}</a-alert>
    <a-table
      :bordered="{ cell: true }"
      row-key="order_id"
      size="small"
      :data="orders"
      :loading="loading"
      :pagination="{ current: page, pageSize, total, showTotal: true }"
      :scroll="{ x: 1100 }"
      :row-class="() => 'clickable'"
      @row-click="(record: unknown) => openDrawer(record as Order)"
      @page-change="changePage"
    >
      <template #columns>
        <a-table-column title="提交时间" :width="140">
          <template #cell="{ record }"
            ><span class="tx-muted tx-nowrap">{{ fmtClock(record.submitted_at || record.created_at, true) }}</span></template
          >
        </a-table-column>
        <a-table-column v-if="memberOptions.length > 1" title="执行账户" :width="130" ellipsis tooltip>
          <template #cell="{ record }">{{ accountName(record.trading_account_id) }}</template>
        </a-table-column>
        <a-table-column title="标的" :width="170">
          <template #cell="{ record }">
            <strong>{{ instrumentName(record.instrument_id) }}</strong>
            <span class="tx-muted"> {{ marketLabel(record.market_type) }}</span>
            <span v-if="record.reduce_position_only" class="tx-muted"> 只减仓</span>
          </template>
        </a-table-column>
        <a-table-column title="方向" :width="70">
          <template #cell="{ record }"
            ><span :class="record.side === 1 ? 'tx-pos' : 'tx-neg'">{{ record.side === 1 ? "买入" : "卖出" }}</span></template
          >
        </a-table-column>
        <a-table-column title="类型" :width="70">
          <template #cell="{ record }">{{ record.order_type === 1 ? "市价" : "限价" }}</template>
        </a-table-column>
        <a-table-column title="数量" :width="110" align="right">
          <template #cell="{ record }"
            ><span class="tx-num">{{ fmtQty(record.quantity) }}</span></template
          >
        </a-table-column>
        <a-table-column title="已成交" :width="110" align="right">
          <template #cell="{ record }"
            ><span class="tx-num">{{ fmtQty(record.filled_quantity) }}</span></template
          >
        </a-table-column>
        <a-table-column title="均价" :width="110" align="right">
          <template #cell="{ record }"
            ><span class="tx-num">{{ Number(record.average_price) ? fmtQty(record.average_price) : "—" }}</span></template
          >
        </a-table-column>
        <a-table-column title="状态" :width="120">
          <template #cell="{ record }">
            <StatusMark :tone="stateTone(record.state)">{{ orderStateLabels[record.state] || record.state }}</StatusMark>
            <div v-if="record.reject_reason" class="reason">{{ orderReasonText(record.reject_reason) }}</div>
          </template>
        </a-table-column>
        <a-table-column title="来源" :width="80">
          <template #cell="{ record }">{{ orderOwnerLabels[record.owner_type] || record.owner_type }}</template>
        </a-table-column>
        <a-table-column title="操作" :width="80" :fixed="isNarrow ? undefined : 'right'">
          <template #cell="{ record }">
            <a-button
              v-if="canCancelOrderState(record.state)"
              size="mini"
              type="text"
              status="danger"
              @click.stop="openCancel(record)"
            >
              撤单
            </a-button>
          </template>
        </a-table-column>
      </template>
    </a-table>
    <OrderDetailDrawer v-model:visible="drawerVisible" :order="drawerOrder" :account-name="accountName" />
  </SectionBlock>
</template>

<script setup lang="ts">
import { useWindowSize } from "@vueuse/core";
import { computed, inject, ref, watch } from "vue";
import { canCancelOrderState, listOrders, orderOwnerLabels, orderReasonText, orderStateLabels } from "@/api/trade";
import type { MarketType, Order } from "@/api/trade";
import { createLatestRequestGuard } from "@/utils/latest-request";
import { fmtClock, fmtQty } from "../shared/format";
import { instrumentName, marketLabel } from "../shared/markets";
import { instrumentFilter } from "./instrument-filter";
import SectionBlock from "../shared/section-block.vue";
import StatusMark from "../shared/status-mark.vue";
import { detailContextKey } from "./context";
import OrderDetailDrawer from "./order-detail-drawer.vue";
import { rangeParams, recordRanges, type RecordRange } from "./record-range";

const { logical, accounts, dataVersion, openCancel } = inject(detailContextKey)!;
// 窄屏时右侧操作列不固定，不然会盖住数据列
const { width: viewportWidth } = useWindowSize();
const isNarrow = computed(() => viewportWidth.value < 768);

const range = ref<RecordRange>("7d");
const state = ref<string>();
const ownerType = ref<string>();
const accountId = ref<string>();
const symbol = ref("");
const market = ref<MarketType>();
const page = ref(1);
const pageSize = 20;
const total = ref(0);
const orders = ref<Order[]>([]);
const loading = ref(false);
const error = ref("");
const drawerVisible = ref(false);
const drawerOrder = ref<Order | null>(null);
const requests = createLatestRequestGuard();

const memberOptions = computed(() =>
  (logical.value.members || []).map(member => ({
    id: member.trading_account_id,
    label: accounts.value.get(member.trading_account_id)?.name || member.trading_account_id
  }))
);
const accountName = (id: string) => accounts.value.get(id)?.name || id;
// 标的筛选按所选市场补全；选了执行账户时只列它开通的市场
const marketOptions = computed<MarketType[]>(() =>
  accountId.value ? (accounts.value.get(accountId.value)?.markets ?? []) : (logical.value.markets ?? [])
);
watch(
  marketOptions,
  options => {
    if (!market.value || !options.includes(market.value)) market.value = options[0];
  },
  { immediate: true }
);

function stateTone(value: string) {
  switch (value) {
    case "FILLED":
      return "ok";
    case "REJECTED":
    case "EXPIRED":
      return "bad";
    case "PARTIALLY_FILLED":
    case "CANCELING":
    case "SUBMIT_UNKNOWN":
    case "CANCEL_UNKNOWN":
      return "warn";
    case "CANCELED":
    case "PARTIALLY_CANCELED":
      return "";
    default:
      return "run";
  }
}

async function load() {
  const request = requests.begin();
  loading.value = true;
  error.value = "";
  try {
    const rsp = await listOrders({
      logical_account_id: logical.value.logical_account_id,
      trading_account_id: accountId.value,
      instrument_id: instrumentFilter(symbol.value, market.value, logical.value.settlement_asset),
      state: state.value,
      owner_type: ownerType.value || undefined,
      ...rangeParams(range.value),
      page: { page: page.value, size: pageSize }
    });
    if (!request.isLatest()) return;
    orders.value = rsp.orders || [];
    total.value = rsp.page_result?.total || 0;
  } catch (cause) {
    if (!request.isLatest()) return;
    orders.value = [];
    error.value = cause instanceof Error ? cause.message : "订单读取失败";
  } finally {
    if (request.isLatest()) loading.value = false;
  }
}

function search() {
  page.value = 1;
  void load();
}
function changePage(value: number) {
  page.value = value;
  void load();
}
function openDrawer(order: Order) {
  drawerOrder.value = order;
  drawerVisible.value = true;
}

watch(() => logical.value.logical_account_id, search, { immediate: true });
watch(dataVersion, () => void load());
</script>

<style src="../shared/accounts.css"></style>
<style scoped>
.reason {
  margin-top: 2px;
  color: var(--color-text-3);
  font-size: 12px;
}

.toolbar {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: var(--moox-space-3);
  padding: 12px 16px;
}

.alert {
  width: auto;
  margin: 0 16px var(--moox-space-3);
}

:deep(.clickable) {
  cursor: pointer;
}
</style>
