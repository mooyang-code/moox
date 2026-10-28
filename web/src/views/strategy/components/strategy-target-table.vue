<template>
  <section class="target-section" :class="{ 'is-stale': stale }" aria-label="当前目标">
    <header class="target-head">
      <div class="target-title">
        <strong>当前目标</strong>
        <a-tag v-if="!loading" size="small" :color="tag.color">{{ tag.label }}</a-tag>
        <span v-if="!loading" class="target-count">{{ targets.length }} 个标的</span>
      </div>
      <div v-if="snapshot && (snapshot.bar_end_time || snapshot.valid_until)" class="target-times">
        <span v-if="snapshot.bar_end_time">K 线结束 {{ formatStrategyTime(snapshot.bar_end_time) }}</span>
        <span v-if="snapshot.valid_until">有效至 {{ formatStrategyTime(snapshot.valid_until) }}</span>
      </div>
    </header>
    <div v-if="loading" class="target-loading">正在读取当前目标…</div>
    <a-alert v-else-if="notice" :type="notice.type" show-icon class="target-alert">{{ notice.text }}</a-alert>

    <div v-if="rows.length" class="target-summary" aria-label="目标汇总">
      <span v-for="item in summary" :key="item.label" class="target-chip"
        >{{ item.label }}<strong>{{ item.value }}</strong></span
      >
    </div>
    <div v-if="paged" class="target-tools">
      <a-input
        v-model="query"
        class="target-search"
        size="small"
        allow-clear
        placeholder="搜索标的，例如 BTC"
        aria-label="搜索标的"
      />
    </div>

    <div v-if="rows.length" class="target-body">
      <div
        v-if="pageRows.length"
        class="target-rows"
        :class="{ 'is-single': columns.length === 1 }"
        role="table"
        aria-label="目标权重"
      >
        <div v-for="(column, index) in columns" :key="index" class="target-col" role="rowgroup">
          <div class="target-row target-row--head" role="row">
            <span role="columnheader">标的</span>
            <span role="columnheader">权重分布</span>
            <span role="columnheader" class="target-weight-col">目标权重</span>
            <span role="columnheader">方向</span>
          </div>
          <div v-for="row in column" :key="row.target.instrument_id" class="target-row" role="row">
            <span role="cell" class="target-id">{{ row.target.instrument_id }}</span>
            <span role="cell" class="target-bar" aria-hidden="true"
              ><span :class="['target-bar__fill', row.weight < 0 ? 'short' : 'long']" :style="{ width: `${row.ratio * 100}%` }"
            /></span>
            <span role="cell" :class="['target-weight-col', row.weight < 0 ? 'short' : 'long']">{{
              targetWeightPercent(row.target)
            }}</span>
            <span role="cell">{{ row.weight < 0 ? "空" : "多" }}</span>
          </div>
        </div>
      </div>
      <div v-else class="target-none">没有匹配的标的</div>
    </div>

    <footer v-if="paged" class="target-pager">
      <span class="target-range">{{ rangeText }}</span>
      <a-pagination
        v-if="filtered.length > pageSize"
        v-model:current="page"
        v-model:page-size="pageSize"
        size="small"
        :total="filtered.length"
        :page-size-options="PAGE_SIZE_OPTIONS"
        show-page-size
      />
    </footer>
  </section>
</template>

<script setup lang="ts">
import { computed, ref, watch } from "vue";
import type { InstrumentTarget, StrategyTargetSnapshot } from "@/api/strategy-types";
import {
  formatStrategyTime,
  percent,
  rankTargets,
  targetStateTag,
  targetWeightPercent,
  type TargetState
} from "@/views/strategy/model";

const props = defineProps<{
  targets: InstrumentTarget[];
  snapshot: StrategyTargetSnapshot | null;
  state: TargetState;
  /** 首次读取中：不显示状态判断。 */
  loading?: boolean;
}>();

/** 默认每页条数；目标不超过这个数时不显示搜索与分页。 */
const DEFAULT_PAGE_SIZE = 20;
const PAGE_SIZE_OPTIONS = [20, 50, 100];
/** 一页超过这个数才分成两列：很少的几个标的单列更好读。 */
const TWO_COLUMN_MIN = 10;

const query = ref("");
const page = ref(1);
const pageSize = ref(DEFAULT_PAGE_SIZE);

const rows = computed(() => rankTargets(props.targets));
const tag = computed(() => targetStateTag(props.state));
const paged = computed(() => rows.value.length > DEFAULT_PAGE_SIZE);

const filtered = computed(() => {
  const keyword = query.value.trim().toUpperCase();
  return keyword ? rows.value.filter(row => row.target.instrument_id.toUpperCase().includes(keyword)) : rows.value;
});
const pageRows = computed(() => {
  const start = (page.value - 1) * pageSize.value;
  return filtered.value.slice(start, start + pageSize.value);
});
// 两列按列优先排：先排满左列再排右列，权重从大到小自上而下、先左后右。
const columns = computed(() => {
  const list = pageRows.value;
  if (list.length <= TWO_COLUMN_MIN) return [list];
  const half = Math.ceil(list.length / 2);
  return [list.slice(0, half), list.slice(half)];
});
const rangeText = computed(() => {
  if (!filtered.value.length) return "共 0 条";
  const start = (page.value - 1) * pageSize.value;
  return `第 ${start + 1}–${start + pageRows.value.length} 条，共 ${filtered.value.length} 条`;
});

// 搜索词或每页条数变化回到第一页；目标刷新后总数变少时，页码不能超出末页。
watch([query, pageSize], () => {
  page.value = 1;
});
watch(
  () => filtered.value.length,
  total => {
    const last = Math.max(1, Math.ceil(total / pageSize.value));
    if (page.value > last) page.value = last;
  }
);

const summary = computed(() => {
  const list = rows.value;
  if (!list.length) return [];
  const short = list.filter(row => row.weight < 0).length;
  const gross = list.reduce((sum, row) => sum + Math.abs(row.weight), 0);
  const largest = Math.abs(list[0].weight);
  const smallest = Math.abs(list[list.length - 1].weight);
  const items = [
    { label: "多 / 空", value: `${list.length - short} / ${short}` },
    { label: "权重合计", value: percent(gross, 1) },
    { label: "最大 / 最小", value: largest === smallest ? percent(largest) : `${percent(largest)} / ${percent(smallest)}` }
  ];
  if (query.value.trim()) items.push({ label: "匹配", value: String(filtered.value.length) });
  return items;
});

// 过期、停用、无法确认时目标仍然展示（作为最近一次结果的参考），但整体淡化，并给出说明。
const stale = computed(() => props.state !== "valid" && props.state !== "zero");
const notice = computed<{ type: "info" | "warning"; text: string } | null>(() => {
  switch (props.state) {
    case "inactive":
      return { type: "info", text: "实例已停用：下面是停用前最近一期的目标，不代表当前持仓，也不判断清仓结果。" };
    case "unknown":
      return { type: "warning", text: "无法确认当前会话的目标，请刷新；下面的数据仅供参考。" };
    case "empty":
      return { type: "info", text: "当前会话尚无策略结果，等待下一根 K 线触发计算。" };
    case "expired":
      return { type: "warning", text: "目标已经过期，仅作为历史参考；新一期结果产出后会自动更新。" };
    case "zero":
      return { type: "info", text: "当前会话明确输出零仓位目标。" };
    default:
      return null;
  }
});
</script>

<style scoped>
.target-section.is-stale .target-body {
  opacity: 0.6;
}
.target-head {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  justify-content: space-between;
  gap: 8px 12px;
  margin-bottom: 10px;
}
.target-title {
  display: flex;
  align-items: center;
  gap: 8px;
}
.target-count,
.target-times {
  color: var(--color-text-3);
  font-size: 12px;
}
.target-times {
  display: flex;
  gap: 12px;
}
.target-loading {
  padding: 16px 0;
  color: var(--color-text-3);
  font-size: 13px;
  text-align: center;
}
.target-alert {
  margin-bottom: 10px;
}
.target-summary {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(120px, 1fr));
  gap: 8px;
  margin-bottom: 10px;
}
.target-chip {
  display: grid;
  gap: 2px;
  padding: 6px 10px;
  color: var(--color-text-3);
  font-size: 12px;
  background: var(--color-fill-1);
  border-radius: 6px;
}
.target-chip strong {
  color: var(--color-text-1);
  font-size: 15px;
  font-weight: 500;
}
.target-tools {
  margin-bottom: 8px;
}
.target-search {
  max-width: 320px;
}
/* 容器宽度够才分两列：详情页、运行结果是整页宽，窄抽屉或窄窗口自动退回单列。 */
.target-body {
  container-type: inline-size;
}
.target-rows {
  display: grid;
  grid-template-columns: minmax(0, 1fr);
  border: 1px solid var(--color-border-2);
  border-radius: 6px;
  overflow: hidden;
}
.target-col {
  display: grid;
  align-content: start;
}
.target-col + .target-col .target-row--head {
  display: none;
}
@container (min-width: 520px) {
  .target-rows {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }
  .target-col + .target-col {
    border-left: 1px solid var(--color-border-2);
  }
  .target-col + .target-col .target-row--head {
    display: grid;
  }
}
/* 只有一列数据（标的很少或搜索结果很少）时，宽容器里也整行单列。 */
.target-rows.is-single {
  grid-template-columns: minmax(0, 1fr);
}
.target-row {
  display: grid;
  grid-template-columns: minmax(90px, 1.1fr) minmax(40px, 1fr) 64px 32px;
  align-items: center;
  gap: 10px;
  padding: 7px 12px;
  font-size: 13px;
}
.target-row + .target-row {
  border-top: 1px solid var(--color-border-1);
}
.target-row--head {
  color: var(--color-text-3);
  font-size: 12px;
  background: var(--color-fill-1);
}
.target-id {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
}
.target-bar {
  height: 8px;
  overflow: hidden;
  border-radius: 4px;
  background: var(--color-fill-2);
}
.target-bar__fill {
  display: block;
  height: 100%;
  border-radius: 4px;
}
.target-bar__fill.long {
  background: rgb(var(--green-5));
}
.target-bar__fill.short {
  background: rgb(var(--red-5));
}
.target-weight-col {
  text-align: right;
}
.long {
  color: rgb(var(--green-6));
}
.short {
  color: rgb(var(--red-6));
}
.target-none {
  padding: 20px 0;
  color: var(--color-text-3);
  font-size: 13px;
  text-align: center;
  border: 1px solid var(--color-border-2);
  border-radius: 6px;
}
.target-pager {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  margin-top: 10px;
}
.target-range {
  color: var(--color-text-3);
  font-size: 12px;
}
</style>
