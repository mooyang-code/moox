<template>
  <SectionBlock title="成交" flush>
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
      <span class="tx-muted hint">点一行看所属订单</span>
    </RecordToolbar>
    <a-alert v-if="error" type="error" class="alert">{{ error }}</a-alert>
    <a-table
      :bordered="{ cell: true }"
      row-key="fill_id"
      size="small"
      :data="fills"
      :loading="loading"
      :pagination="{ current: page, pageSize, total, showTotal: true }"
      :scroll="{ x: 1100 }"
      :row-class="() => 'clickable'"
      @row-click="(record: unknown) => openOrderOf(record as Fill)"
      @page-change="changePage"
    >
      <template #columns>
        <a-table-column title="成交时间" :width="140">
          <template #cell="{ record }"
            ><span class="tx-muted tx-nowrap">{{ formatInstant(record.traded_at, displayZone) }}</span></template
          >
        </a-table-column>
        <a-table-column v-if="memberOptions.length > 1" title="执行账户" :width="130" ellipsis tooltip>
          <template #cell="{ record }">{{ accountName(record.trading_account_id) }}</template>
        </a-table-column>
        <a-table-column title="标的" :width="170">
          <template #cell="{ record }">
            <strong>{{ instrumentName(record.instrument_id) }}</strong>
            <span class="tx-muted"> {{ marketLabel(record.market_type) }}</span>
          </template>
        </a-table-column>
        <a-table-column title="方向" :width="70">
          <template #cell="{ record }"
            ><span :class="record.side === 1 ? 'tx-pos' : 'tx-neg'">{{ record.side === 1 ? "买入" : "卖出" }}</span></template
          >
        </a-table-column>
        <a-table-column title="价格" :width="110" align="right">
          <template #cell="{ record }"
            ><span class="tx-num">{{ fmtQty(record.price) }}</span></template
          >
        </a-table-column>
        <a-table-column title="数量" :width="110" align="right">
          <template #cell="{ record }"
            ><span class="tx-num">{{ fmtQty(record.quantity) }}</span></template
          >
        </a-table-column>
        <a-table-column title="成交额" :width="110" align="right">
          <template #cell="{ record }"
            ><span class="tx-num">{{ fmtMoney(Number(record.price) * Number(record.quantity)) }}</span></template
          >
        </a-table-column>
        <a-table-column title="手续费" :width="130" align="right">
          <template #cell="{ record }"
            ><span class="tx-num">{{ fmtQty(record.fee) }} {{ record.fee_asset }}</span></template
          >
        </a-table-column>
        <a-table-column title="已实现盈亏" :width="120" align="right">
          <template #cell="{ record }">
            <span class="tx-num" :class="pnlClass(record.realized_pnl)">{{ fmtSigned(record.realized_pnl, 4) }}</span>
          </template>
        </a-table-column>
        <a-table-column title="角色" :width="70">
          <template #cell="{ record }">{{ record.role === "TAKER" ? "吃单" : record.role === "MAKER" ? "挂单" : "—" }}</template>
        </a-table-column>
      </template>
    </a-table>
    <OrderDetailDrawer v-model:visible="drawerVisible" :order="drawerOrder" :account-name="accountName" />
  </SectionBlock>
</template>

<script setup lang="ts">
import { inject, ref, watch } from "vue";
import { Message } from "@arco-design/web-vue";
import { getOrder, listFills } from "@/api/trade";
import type { Fill, Order } from "@/api/trade";
import { createLatestRequestGuard } from "@/utils/latest-request";
import { fmtMoney, fmtQty, fmtSigned, pnlClass } from "../shared/format";
import { instrumentName, marketLabel } from "../shared/markets";
import SectionBlock from "../shared/section-block.vue";
import { detailContextKey } from "./context";
import OrderDetailDrawer from "./order-detail-drawer.vue";
import RecordToolbar from "./record-toolbar.vue";
import { useRecordFilters } from "./record-filters";
import { formatInstant } from "@/utils/time-zone";
import { useDisplayZone } from "@/hooks/useDisplayZone";

// 交易页的时间按当前空间登记的时区显示
const displayZone = useDisplayZone();
const { logical, dataVersion } = inject(detailContextKey)!;

// 筛选条件写在地址里（f 开头），切换页签回来、刷新页面后仍保持；时间范围、账户、市场、标的见 useRecordFilters
const { range, accountId, symbol, market, page, memberOptions, accountName, marketOptions, stockOnly, queryParams, search } =
  useRecordFilters({ prefix: "f", reload: () => void load() });
const pageSize = 20;
const total = ref(0);
const fills = ref<Fill[]>([]);
const loading = ref(false);
const error = ref("");
const drawerVisible = ref(false);
const drawerOrder = ref<Order | null>(null);
const requests = createLatestRequestGuard();
// 连续点几行时只认最后一次：先点的订单后到，不能盖掉后点的
const orderRequests = createLatestRequestGuard();

async function load() {
  const request = requests.begin();
  loading.value = true;
  error.value = "";
  try {
    const rsp = await listFills({ ...queryParams(), page: { page: page.value, size: pageSize } });
    if (!request.isLatest()) return;
    fills.value = rsp.fills || [];
    total.value = rsp.page_result?.total || 0;
  } catch (cause) {
    if (!request.isLatest()) return;
    fills.value = [];
    error.value = cause instanceof Error ? cause.message : "成交读取失败";
  } finally {
    if (request.isLatest()) loading.value = false;
  }
}

function changePage(value: number) {
  page.value = value;
  void load();
}
async function openOrderOf(fill: Fill) {
  const request = orderRequests.begin();
  try {
    const rsp = await getOrder(fill.order_id);
    if (!request.isLatest()) return;
    drawerOrder.value = rsp.order;
    drawerVisible.value = true;
  } catch {
    if (request.isLatest()) Message.warning("读不到这笔成交所属的订单");
  }
}

watch(() => logical.value.logical_account_id, search, { immediate: true });
watch(dataVersion, () => void load());
</script>

<style src="../shared/accounts.css"></style>
<style scoped>
.hint {
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
