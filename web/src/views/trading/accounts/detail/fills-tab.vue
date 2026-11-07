<template>
  <SectionBlock title="成交" flush>
    <div class="toolbar">
      <a-radio-group v-model="range" type="button" @change="search">
        <a-radio v-for="item in recordRanges" :key="item.key" :value="item.key">{{ item.label }}</a-radio>
      </a-radio-group>
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
      <a-select v-if="marketOptions.length > 1" v-model="market" style="width: 110px" @change="search">
        <a-option v-for="item in marketOptions" :key="item" :value="item">{{ marketLabel(item) }}</a-option>
      </a-select>
      <a-input v-model="symbol" allow-clear placeholder="币，如 BTC" style="width: 150px" @press-enter="search" @clear="search" />
      <a-button :loading="loading" @click="search">查询</a-button>
      <span class="tx-muted hint">点一行看所属订单</span>
    </div>
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
            ><span class="tx-muted tx-nowrap">{{ fmtClock(record.traded_at, true) }}</span></template
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
import { computed, inject, ref, watch } from "vue";
import { Message } from "@arco-design/web-vue";
import { getOrder, listFills } from "@/api/trade";
import type { Fill, MarketType, Order } from "@/api/trade";
import { createLatestRequestGuard } from "@/utils/latest-request";
import { fmtClock, fmtMoney, fmtQty, fmtSigned, pnlClass } from "../shared/format";
import { instrumentName, marketLabel } from "../shared/markets";
import { instrumentFilter } from "./instrument-filter";
import SectionBlock from "../shared/section-block.vue";
import { detailContextKey } from "./context";
import OrderDetailDrawer from "./order-detail-drawer.vue";
import { rangeParams, recordRanges, type RecordRange } from "./record-range";

const { logical, accounts, dataVersion } = inject(detailContextKey)!;

const range = ref<RecordRange>("7d");
const accountId = ref<string>();
const symbol = ref("");
const market = ref<MarketType>();
const page = ref(1);
const pageSize = 20;
const total = ref(0);
const fills = ref<Fill[]>([]);
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

async function load() {
  const request = requests.begin();
  loading.value = true;
  error.value = "";
  try {
    const rsp = await listFills({
      logical_account_id: logical.value.logical_account_id,
      trading_account_id: accountId.value,
      instrument_id: instrumentFilter(symbol.value, market.value, logical.value.settlement_asset),
      ...rangeParams(range.value),
      page: { page: page.value, size: pageSize }
    });
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

function search() {
  page.value = 1;
  void load();
}
function changePage(value: number) {
  page.value = value;
  void load();
}
async function openOrderOf(fill: Fill) {
  try {
    drawerOrder.value = (await getOrder(fill.order_id)).order;
    drawerVisible.value = true;
  } catch {
    Message.warning("读不到这笔成交所属的订单");
  }
}

watch(() => logical.value.logical_account_id, search, { immediate: true });
watch(dataVersion, () => void load());
</script>

<style src="../shared/accounts.css"></style>
<style scoped>
.toolbar {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: var(--moox-space-3);
  padding: 12px 16px;
}

.hint {
  font-size: 12px;
}

.alert {
  margin: 0 16px var(--moox-space-3);
}

:deep(.clickable) {
  cursor: pointer;
}
</style>
