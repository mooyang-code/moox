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
        <a-table-column title="操作" :width="80" fixed="right">
          <template #cell="{ record }">
            <a-button size="mini" type="text" :disabled="!canOrder" @click="openOrder(instrumentOf(record))">下单</a-button>
          </template>
        </a-table-column>
      </template>
    </a-table>
    <a-alert v-if="target?.last_error" type="warning" class="alert">{{ target.last_error }}</a-alert>
  </SectionBlock>
</template>

<script setup lang="ts">
import { computed, inject, ref } from "vue";
import { useRoute } from "vue-router";
import { fmtQty } from "../shared/format";
import SectionBlock from "../shared/section-block.vue";
import { detailContextKey } from "./context";
import { cleanNumber, filterCoinRows, type CoinRow, type PositionFilter, type TargetCell } from "./positions-model";

const { logical, target, coinRows, flattenRunning, openOrder } = inject(detailContextKey)!;
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
/** 行内下单：有永续目标或永续持仓时默认永续，否则现货。 */
function instrumentOf(row: CoinRow) {
  const quote = logical.value.settlement_asset;
  const swap = row.targetSwap || row.actualSwap !== 0;
  return `${row.coin}-${quote}-${swap ? "SWAP" : "SPOT"}`;
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
  margin: var(--moox-space-3) 16px;
}
</style>
