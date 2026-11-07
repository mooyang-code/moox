<template>
  <SectionBlock title="持仓与目标" flush>
    <template #sub>按币一行，目标来自策略，实际来自各执行账户；偏差分现货、永续各算各的</template>
    <div class="toolbar">
      <a-radio-group v-model="filter" type="button" @change="page = 1">
        <a-radio v-for="item in filters" :key="item.key" :value="item.key">{{ item.label }} {{ item.count }}</a-radio>
      </a-radio-group>
      <a-input-search v-model="search" allow-clear placeholder="搜索币" style="width: 200px" @input="page = 1" />
    </div>
    <a-table
      row-key="coin"
      size="small"
      :data="rows"
      :bordered="{ cell: true }"
      :pagination="{ pageSize: 50, current: page, showTotal: true }"
      :scroll="{ x: 1000 }"
      @page-change="(value: number) => (page = value)"
    >
      <template #columns>
        <a-table-column title="币" :width="90" fixed="left">
          <template #cell="{ record }"
            ><strong>{{ record.coin }}</strong></template
          >
        </a-table-column>
        <a-table-column v-if="logical.control_mode === 1" title="目标">
          <a-table-column title="现货" :width="110" align="right">
            <template #cell="{ record }"
              ><span class="tx-num">{{ targetText(record.targetSpot) }}</span></template
            >
          </a-table-column>
          <a-table-column title="永续" :width="110" align="right">
            <template #cell="{ record }"
              ><span class="tx-num">{{ targetText(record.targetSwap) }}</span></template
            >
          </a-table-column>
        </a-table-column>
        <a-table-column title="实际">
          <a-table-column title="现货" :width="110" align="right">
            <template #cell="{ record }"
              ><span class="tx-num">{{ actualText(record.actualSpot, "spot") }}</span></template
            >
          </a-table-column>
          <a-table-column title="永续" :width="110" align="right">
            <template #cell="{ record }"
              ><span class="tx-num">{{ actualText(record.actualSwap, "swap") }}</span></template
            >
          </a-table-column>
        </a-table-column>
        <a-table-column v-if="logical.control_mode === 1" title="偏差" :width="190">
          <template #cell="{ record }">
            <span v-if="record.state === 'blocked'" class="tx-neg">已拦下：{{ record.blockedReason }}</span>
            <span v-else-if="record.state === 'extra'" class="tx-st tx-st--warn">目标外</span>
            <template v-else>
              <div
                v-if="record.deviationSpot !== null"
                class="tx-num"
                :class="deviationClass(record.deviationSpot, record, 'targetSpot')"
              >
                现货 {{ signed(record.deviationSpot) }}
              </div>
              <div
                v-if="record.deviationSwap !== null"
                class="tx-num"
                :class="deviationClass(record.deviationSwap, record, 'targetSwap')"
              >
                永续 {{ signed(record.deviationSwap) }}
              </div>
              <span v-if="record.deviationSpot === null && record.deviationSwap === null" class="tx-muted">保持</span>
              <span v-else-if="record.state === 'ok'" class="tx-muted">到位</span>
            </template>
          </template>
        </a-table-column>
        <a-table-column title="现货余额" :width="120" align="right">
          <template #cell="{ record }"
            ><span class="tx-num">{{ record.spotBalance ? fmtQty(cleanNumber(record.spotBalance)) : "—" }}</span></template
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
                :disabled="!canOrder || !memberFor(hint.instrumentId)"
                @click="openRowOrder(hint)"
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
import { useRoute } from "vue-router";
import { fmtQty } from "../shared/format";
import { marketLabel, marketOfInstrument } from "../shared/markets";
import SectionBlock from "../shared/section-block.vue";
import { detailContextKey } from "./context";
import {
  cleanNumber,
  filterCoinRows,
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

const filter = ref<PositionFilter>(route.query.filter === "off" ? "off" : "all");
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
const canOrder = computed(() => [1, 2].includes(logical.value.control_mode) && !flattenRunning.value);

function targetText(cell?: TargetCell) {
  if (!cell) return "—";
  return cell.hold ? "保持" : fmtQty(cell.quantity);
}
function actualText(value: number, market: "spot" | "swap") {
  if (value !== 0) return fmtQty(cleanNumber(value));
  return logical.value.markets?.includes(market === "spot" ? 1 : 2) ? "0" : "—";
}
function signed(value: number) {
  const clean = cleanNumber(value);
  return `${clean > 0 ? "+" : ""}${fmtQty(clean)}`;
}
/** 偏差超出 1% 才着色：偏多为绿，偏少为红。 */
function deviationClass(deviation: number, row: CoinRow, key: "targetSpot" | "targetSwap") {
  const base = Math.abs(row[key]?.quantity ?? 0);
  const ratio = base === 0 ? (deviation === 0 ? 0 : 1) : Math.abs(deviation) / base;
  if (ratio <= 0.01) return "";
  return deviation > 0 ? "tx-pos" : "tx-neg";
}
/** 能下这个标的的成员：启用，并且已开通标的所在的市场。 */
function memberFor(instrumentId: string): string | undefined {
  const market = marketOfInstrument(instrumentId);
  return (logical.value.members || []).find(
    member => member.enabled && accounts.value.get(member.trading_account_id)?.markets?.includes(market)
  )?.trading_account_id;
}
/** 行内下单：每个有目标或有持仓的市场一个入口，带上市场、方向和与目标的差额。 */
function orderHints(row: CoinRow) {
  return rowOrderHints(row, logical.value.settlement_asset);
}
function openRowOrder(hint: OrderHint) {
  openOrder(hint.instrumentId, memberFor(hint.instrumentId), { side: hint.side, quantity: hint.quantity });
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

.alert {
  width: auto;
  margin: var(--moox-space-3) 16px;
}
</style>
