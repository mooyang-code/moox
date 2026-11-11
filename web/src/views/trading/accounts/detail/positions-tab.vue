<template>
  <SectionBlock title="持仓与目标" flush>
    <template #sub>{{
      stockMode
        ? "按股票一行，目标来自策略，实际来自各执行账户；可卖是扣掉冻结和当日买入后的数量"
        : "按币一行，目标来自策略，实际来自各执行账户；市值和未实现盈亏按标记价，偏差分现货、永续各算各的"
    }}</template>
    <div class="toolbar">
      <a-radio-group v-model="filter" type="button" @change="page = 1">
        <a-radio v-for="item in filters" :key="item.key" :value="item.key">{{ item.label }} {{ item.count }}</a-radio>
      </a-radio-group>
      <a-input-search
        v-model="search"
        allow-clear
        :placeholder="stockMode ? '搜索股票代码' : '搜索币'"
        style="width: 200px"
        @input="page = 1"
      />
    </div>
    <a-table
      row-key="coin"
      size="small"
      :data="rows"
      :bordered="{ cell: true }"
      :pagination="{ pageSize: 50, current: page, showTotal: true }"
      :scroll="{ x: 1100 }"
      @page-change="(value: number) => (page = value)"
    >
      <template #columns>
        <a-table-column
          :title="stockMode ? '股票' : '币'"
          data-index="coin"
          :width="130"
          fixed="left"
          :sortable="{ sortDirections: ['ascend', 'descend'] }"
        >
          <template #cell="{ record }"
            ><strong class="tx-nowrap">{{ record.coin }}</strong></template
          >
        </a-table-column>
        <a-table-column
          title="市值"
          data-index="value"
          :width="120"
          align="right"
          :sortable="{ sortDirections: ['descend', 'ascend'], defaultSortOrder: 'descend' }"
        >
          <template #cell="{ record }"
            ><span class="tx-num">{{ record.value ? fmtMoney(record.value) : "—" }}</span></template
          >
        </a-table-column>
        <a-table-column v-if="logical.control_mode === 1" title="目标">
          <a-table-column v-if="showSpot" :title="spotTitle" :width="120" align="right">
            <template #cell="{ record }"
              ><span class="tx-num">{{ targetText(record.targetSpot) }}</span></template
            >
          </a-table-column>
          <a-table-column v-if="showSwap" title="永续" :width="120" align="right">
            <template #cell="{ record }"
              ><span class="tx-num">{{ targetText(record.targetSwap) }}</span></template
            >
          </a-table-column>
        </a-table-column>
        <a-table-column title="实际">
          <a-table-column v-if="showSpot" :title="spotTitle" :width="120" align="right">
            <template #cell="{ record }"
              ><span class="tx-num">{{ actualText(record.actualSpot, "spot") }}</span></template
            >
          </a-table-column>
          <a-table-column v-if="stockMode" title="可卖" :width="110" align="right">
            <template #cell="{ record }"
              ><span class="tx-num">{{ record.actualSpot ? fmtQtyShort(record.sellable) : "—" }}</span></template
            >
          </a-table-column>
          <a-table-column v-if="showSwap" title="永续" :width="140" align="right">
            <template #cell="{ record }">
              <span v-if="record.actualSwap" class="tx-muted side">{{ record.actualSwap > 0 ? "多" : "空" }}</span>
              <span class="tx-num">{{ actualText(record.actualSwap, "swap") }}</span>
            </template>
          </a-table-column>
        </a-table-column>
        <a-table-column v-if="showSwap" title="开仓均价" :width="110" align="right">
          <template #cell="{ record }"
            ><span class="tx-num">{{ fmtPrice(record.entrySwap) }}</span></template
          >
        </a-table-column>
        <a-table-column
          title="未实现盈亏"
          data-index="unrealizedPnl"
          :width="120"
          align="right"
          :sortable="{ sortDirections: ['descend', 'ascend'] }"
        >
          <template #cell="{ record }"
            ><span class="tx-num" :class="pnlClass(record.unrealizedPnl)">{{ fmtSigned(record.unrealizedPnl) }}</span></template
          >
        </a-table-column>
        <a-table-column v-if="logical.control_mode === 1" title="偏差" :width="190">
          <template #cell="{ record }">
            <span v-if="record.state === 'blocked'" class="tx-neg">未执行：{{ record.blockedReason }}</span>
            <span v-else-if="record.state === 'extra'" class="tx-st tx-st--warn">不在目标内</span>
            <template v-else>
              <div
                v-if="record.deviationSpot !== null"
                class="tx-num"
                :class="deviationClass(record.deviationSpot, record.offSpot)"
              >
                {{ spotTitle }} {{ signed(record.deviationSpot) }}
              </div>
              <div v-else-if="record.offSpot" class="tx-neg">{{ spotTitle }} 不在目标内</div>
              <div
                v-if="record.deviationSwap !== null"
                class="tx-num"
                :class="deviationClass(record.deviationSwap, record.offSwap)"
              >
                永续 {{ signed(record.deviationSwap) }}
              </div>
              <div v-else-if="record.offSwap" class="tx-neg">永续 不在目标内</div>
              <span
                v-if="record.deviationSpot === null && record.deviationSwap === null && record.state === 'ok'"
                class="tx-muted"
                >保持</span
              >
              <span v-else-if="record.state === 'ok'" class="tx-muted">到位</span>
              <div v-if="record.deferredReason" class="tx-muted">顺延：{{ record.deferredReason }}</div>
            </template>
          </template>
        </a-table-column>
        <a-table-column v-if="showSpot && !stockMode" title="现货余额" :width="120" align="right">
          <template #cell="{ record }"
            ><span class="tx-num">{{ record.spotBalance ? fmtQtyShort(record.spotBalance) : "—" }}</span></template
          >
        </a-table-column>
        <a-table-column title="操作" :width="170" :fixed="isNarrow ? undefined : 'right'">
          <template #cell="{ record }">
            <a-space :size="0">
              <a-button
                v-for="hint in orderHints(record)"
                :key="hint.market"
                size="mini"
                type="text"
                :disabled="!canOrder || !memberFor(record, hint)"
                @click="openRowOrder(record, hint)"
                >下单·{{ marketLabel(hint.market) }}</a-button
              >
            </a-space>
          </template>
        </a-table-column>
      </template>
    </a-table>
    <a-alert v-if="target?.last_error" type="warning" class="alert">{{ target.last_error }}</a-alert>
  </SectionBlock>
</template>

<script setup lang="ts">
import { useWindowSize } from "@vueuse/core";
import { computed, inject, ref } from "vue";
import { useRoute, useRouter } from "vue-router";
import { fmtMoney, fmtPrice, fmtQtyShort, fmtSigned, pnlClass } from "../shared/format";
import { marketLabel } from "../shared/markets";
import SectionBlock from "../shared/section-block.vue";
import { detailContextKey } from "./context";
import {
  cleanNumber,
  filterCoinRows,
  hasSpotSide,
  hasSwapSide,
  largestHolder,
  rowOrderHints,
  type CoinRow,
  type OrderHint,
  type PositionFilter,
  type TargetCell
} from "./positions-model";

const { logical, accounts, target, coinRows, flattenRunning, openOrder } = inject(detailContextKey)!;
// 窄屏时右侧操作列不固定，不然会盖住数据列
const { width: viewportWidth } = useWindowSize();
const isNarrow = computed(() => viewportWidth.value < 768);
const route = useRoute();

const router = useRouter();
const filter = ref<PositionFilter>(route.query.filter === "off" ? "off" : "all");
// filter=off 只是概览“查看未到位”带过来的一次性入口：读到后从地址里去掉，之后切换筛选、换页签都不再带着它
if (route.query.filter !== undefined) void router.replace({ query: { ...route.query, filter: undefined } });
const search = ref("");
const page = ref(1);

const filters = computed(() => [
  { key: "all" as const, label: "全部", count: coinRows.value.length },
  ...(logical.value.control_mode === 1
    ? [{ key: "off" as const, label: "未到位", count: filterCoinRows(coinRows.value, "off", "").length }]
    : []),
  { key: "holding" as const, label: "有持仓", count: filterCoinRows(coinRows.value, "holding", "").length }
]);
const rows = computed(() => filterCoinRows(coinRows.value, filter.value, search.value));
// A 股组合：一列股票、加“可卖”列，没有永续
const stockMode = computed(() => Boolean(logical.value.markets?.includes(3)));
const spotTitle = computed(() => (stockMode.value ? "股票" : "现货"));
// 组合里完全没有现货（或永续）一侧的目标和持仓时，不显示那一侧的列
const showSpot = computed(() => stockMode.value || hasSpotSide(coinRows.value));
const showSwap = computed(() => !stockMode.value && hasSwapSide(coinRows.value));
const canOrder = computed(() => [1, 2].includes(logical.value.control_mode) && !flattenRunning.value);

function targetText(cell?: TargetCell) {
  if (!cell) return "—";
  return cell.hold ? "保持" : fmtQtyShort(cell.quantity);
}
function actualText(value: number, market: "spot" | "swap") {
  if (value !== 0) return fmtQtyShort(cleanNumber(value));
  const opened = market === "spot" ? (stockMode.value ? 3 : 1) : 2;
  return logical.value.markets?.includes(opened) ? "0" : "—";
}
function signed(value: number) {
  const clean = cleanNumber(value);
  return `${clean > 0 ? "+" : ""}${fmtQtyShort(clean)}`;
}
/** 这个市场未到位才着色：偏多为绿，偏少为红。 */
function deviationClass(deviation: number, off: boolean) {
  if (!off || deviation === 0) return "";
  return deviation > 0 ? "tx-pos" : "tx-neg";
}
/** 默认的执行账户：启用并开通了标的所在市场的成员里，这个市场持仓最大的；都没有持仓时取第一个。 */
function memberFor(row: CoinRow, hint: OrderHint): string | undefined {
  const candidates = (logical.value.members || [])
    .filter(member => member.enabled && accounts.value.get(member.trading_account_id)?.markets?.includes(hint.market))
    .map(member => member.trading_account_id);
  return largestHolder(row, hint.market, candidates) ?? candidates[0];
}
/** 行内下单：每个有目标或有持仓的市场一个入口，带上市场、方向和与目标的差额。 */
function orderHints(row: CoinRow) {
  return rowOrderHints(row, logical.value.settlement_asset);
}
function openRowOrder(row: CoinRow, hint: OrderHint) {
  openOrder(hint.instrumentId, memberFor(row, hint), { side: hint.side, quantity: hint.quantity });
}
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

.side {
  margin-right: 6px;
  font-size: 12px;
}

.alert {
  width: auto;
  margin: var(--moox-space-3) 16px;
}
</style>
