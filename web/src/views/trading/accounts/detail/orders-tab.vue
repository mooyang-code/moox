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
import { useRoute, useRouter } from "vue-router";
import { canCancelOrderState, getOrder, isActiveOrderState, listOrders, orderOwnerLabels, orderStateLabels } from "@/api/trade";
import type { MarketType, Order } from "@/api/trade";
import { createLatestRequestGuard } from "@/utils/latest-request";
import { usePolling } from "@/hooks/usePolling";
import { fmtQty } from "../shared/format";
import { instrumentName, marketLabel } from "../shared/markets";
import { orderStateTone } from "./detail-model";
import { instrumentFilter } from "./instrument-filter";
import SectionBlock from "../shared/section-block.vue";
import StatusMark from "../shared/status-mark.vue";
import { detailContextKey } from "./context";
import OrderDetailDrawer from "./order-detail-drawer.vue";
import { rangeFromQuery, rangeParams, recordRanges, type RecordRange } from "./record-range";
import { formatInstant } from "@/utils/time-zone";
import { useDisplayZone } from "@/hooks/useDisplayZone";

// 交易页的时间按当前空间登记的时区显示
const displayZone = useDisplayZone();
const { logical, accounts, dataVersion, openCancel } = inject(detailContextKey)!;
// 窄屏时右侧操作列不固定，不然会盖住数据列
const { width: viewportWidth } = useWindowSize();
const isNarrow = computed(() => viewportWidth.value < 768);

// 筛选条件写在地址里（o 开头），切换页签回来、刷新页面后仍保持
const route = useRoute();
const router = useRouter();
const queryText = (key: string) => (typeof route.query[key] === "string" ? (route.query[key] as string) : undefined);
const range = ref<RecordRange>(rangeFromQuery(route.query.orange));
const state = ref<string | undefined>(queryText("ostate"));
const ownerType = ref<string | undefined>(queryText("oowner"));
const accountId = ref<string | undefined>(queryText("oaccount"));
const symbol = ref(queryText("osymbol") ?? "");
// 已提交的币名：输入框打到一半时，轮询、翻页仍按上次查询的币名读，回车或点“查询”才换
const appliedSymbol = ref(symbol.value.trim());
// 地址里的值都是字符串，市场要转回数字，否则和可选市场比较不相等、会被重置成第一个
const marketQuery = Number(queryText("omarket"));
const market = ref<MarketType | undefined>([0, 1, 2, 3].includes(marketQuery) ? (marketQuery as MarketType) : undefined);
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
        logical_account_id: logical.value.logical_account_id,
        trading_account_id: accountId.value,
        instrument_id: instrumentFilter(appliedSymbol.value, market.value, logical.value.settlement_asset),
        state: state.value,
        owner_type: ownerType.value || undefined,
        ...rangeParams(range.value),
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

function search() {
  page.value = 1;
  appliedSymbol.value = symbol.value.trim();
  void router.replace({
    query: {
      ...route.query,
      orange: range.value === "7d" ? undefined : range.value,
      ostate: state.value || undefined,
      oowner: ownerType.value || undefined,
      oaccount: accountId.value || undefined,
      osymbol: appliedSymbol.value || undefined,
      omarket: appliedSymbol.value ? market.value : undefined
    }
  });
  void load();
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
