<template>
  <SectionBlock title="订单" flush>
    <RecordToolbar
      v-model:range="range"
      v-model:account-id="accountId"
      v-model:market="market"
      v-model:symbol="symbol"
      :member-options="memberOptions"
      :market-options="marketOptions"
      :stock-only="stockOnly"
      :loading="loading"
      @search="search"
    >
      <template #filters>
        <a-select v-model="state" allow-clear placeholder="订单状态" style="width: 150px" @change="search">
          <a-option v-for="(label, value) in orderStateLabels" :key="value" :value="value">{{ label }}</a-option>
        </a-select>
        <a-select v-model="ownerType" allow-clear placeholder="订单来源" style="width: 120px" @change="search">
          <a-option v-for="(label, value) in orderOwnerLabels" :key="value" :value="value">{{ label }}</a-option>
        </a-select>
      </template>
    </RecordToolbar>
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
            ><span class="tx-muted tx-nowrap">{{
              formatInstant(record.submitted_at || record.created_at, displayZone)
            }}</span></template
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
            <StatusMark :tone="orderStateTone(record.state)">{{ orderStateLabels[record.state] || record.state }}</StatusMark>
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
import { useRoute } from "vue-router";
import {
  canCancelOrderState,
  getOrder,
  isActiveOrderState,
  listOrders,
  orderOwnerLabels,
  orderReasonText,
  orderStateLabels
} from "@/api/trade";
import type { Order } from "@/api/trade";
import { createLatestRequestGuard } from "@/utils/latest-request";
import { usePolling } from "@/hooks/usePolling";
import { fmtQty } from "../shared/format";
import { instrumentName, marketLabel } from "../shared/markets";
import { orderStateTone } from "./detail-model";
import SectionBlock from "../shared/section-block.vue";
import StatusMark from "../shared/status-mark.vue";
import { detailContextKey } from "./context";
import OrderDetailDrawer from "./order-detail-drawer.vue";
import RecordToolbar from "./record-toolbar.vue";
import { useRecordFilters } from "./record-filters";
import { formatInstant } from "@/utils/time-zone";
import { useDisplayZone } from "@/hooks/useDisplayZone";

// 交易页的时间按当前空间登记的时区显示
const displayZone = useDisplayZone();
const { logical, dataVersion, openCancel } = inject(detailContextKey)!;
// 窄屏时右侧操作列不固定，不然会盖住数据列
const { width: viewportWidth } = useWindowSize();
const isNarrow = computed(() => viewportWidth.value < 768);

// 筛选条件写在地址里（o 开头），切换页签回来、刷新页面后仍保持；时间范围、账户、市场、标的见 useRecordFilters
const route = useRoute();
const queryText = (key: string) => (typeof route.query[key] === "string" ? (route.query[key] as string) : undefined);
const state = ref<string | undefined>(queryText("ostate"));
const ownerType = ref<string | undefined>(queryText("oowner"));
const { range, accountId, symbol, market, page, memberOptions, accountName, marketOptions, stockOnly, queryParams, search } =
  useRecordFilters({
    prefix: "o",
    extraQuery: () => ({ ostate: state.value, oowner: ownerType.value }),
    reload: () => void load()
  });
const pageSize = 20;
const total = ref(0);
const orders = ref<Order[]>([]);
const loading = ref(false);
const error = ref("");
const drawerVisible = ref(false);
const drawerOrder = ref<Order | null>(null);
const requests = createLatestRequestGuard();

/** background：有未完成订单时每 5 秒的后台刷新，不转加载态、不弹提示，失败时保留上次的列表。 */
async function load(background = false) {
  const request = requests.begin();
  if (!background) {
    loading.value = true;
    error.value = "";
  }
  try {
    const rsp = await listOrders(
      {
        ...queryParams(),
        state: state.value,
        owner_type: ownerType.value || undefined,
        page: { page: page.value, size: pageSize }
      },
      { silent: background }
    );
    if (!request.isLatest()) return;
    orders.value = rsp.orders || [];
    void refreshDrawerOrder();
    total.value = rsp.page_result?.total || 0;
    error.value = "";
  } catch (cause) {
    if (!request.isLatest() || background) return;
    orders.value = [];
    error.value = cause instanceof Error ? cause.message : "订单读取失败";
  } finally {
    if (request.isLatest()) loading.value = false;
  }
}

function changePage(value: number) {
  page.value = value;
  void load();
}
/** 抽屉开着时跟着列表刷新：订单还在列表里就用新的那一行；已经不在（例如成交后被状态筛选滤掉、挤到下一页）就单独读一次。 */
async function refreshDrawerOrder() {
  const current = drawerOrder.value;
  if (!drawerVisible.value || !current) return;
  const listed = orders.value.find(order => order.order_id === current.order_id);
  if (listed) {
    drawerOrder.value = listed;
    return;
  }
  try {
    const rsp = await getOrder(current.order_id, { silent: true });
    if (drawerVisible.value && drawerOrder.value?.order_id === current.order_id && rsp.order) drawerOrder.value = rsp.order;
  } catch {
    // 读不到时保留抽屉里的内容，下次刷新再试
  }
}

function openDrawer(order: Order) {
  drawerOrder.value = order;
  drawerVisible.value = true;
}

watch(() => logical.value.logical_account_id, search, { immediate: true });
watch(dataVersion, () => void load());
// 列表里有未完成的订单时每 5 秒刷新一次，看着它成交或撤掉
usePolling(
  () => load(true),
  5000,
  () => orders.value.some(order => isActiveOrderState(order.state))
);
</script>

<style src="../shared/accounts.css"></style>
<style scoped>
.reason {
  margin-top: 2px;
  color: var(--color-text-3);
  font-size: 12px;
}

.alert {
  width: auto;
  margin: 0 16px var(--moox-space-3);
}

:deep(.clickable) {
  cursor: pointer;
}
</style>
