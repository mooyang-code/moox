<template>
  <div class="items">
    <a-alert v-if="result.status === 'skipped'" type="warning" show-icon class="block"
      >本期跳过：{{ skipReasonLabel(result.skip_reason) }}<template v-if="detail">。{{ detail }}</template></a-alert
    >
    <a-alert v-else-if="result.status === 'ok' && !result.targets.length" type="info" show-icon class="block"
      >本期没有选出标的，目标为空，转为现金。</a-alert
    >
    <div v-if="ruleRows.length" class="rules">
      <div v-for="row in ruleRows" :key="row.id" class="rule">
        <strong>{{ row.id }}</strong>
        <span
          >预期 {{ row.summary.expected }} · 年龄剔除 {{ row.summary.aged_out }} · 可用 {{ row.summary.available }} · 缺数
          {{ row.summary.missing }}</span
        >
        <span
          >过滤 {{ row.summary.filtered }} · 打分 {{ row.summary.scored }} · 入选 {{ row.summary.selected }} · 配权
          {{ row.summary.weighted }}</span
        >
        <span>预算 {{ percent(row.summary.budget) }} · 已分配 {{ percent(row.summary.allocated) }}</span>
      </div>
    </div>
    <a-space v-if="summary.gross || summary.cash" class="totals" wrap>
      <a-tag>基础集合 {{ summary.universe }}</a-tag>
      <a-tag>gross {{ percent(summary.gross) }}</a-tag>
      <a-tag>net {{ percent(summary.net) }}</a-tag>
      <a-tag color="arcoblue">现金 {{ percent(summary.cash) }}</a-tag>
      <a-tag>换手 {{ percent(summary.turnover) }}</a-tag>
    </a-space>
    <a-alert v-for="note in summary.notes" :key="note" type="info" class="note">{{ note }}</a-alert>
    <div class="filters">
      <a-select v-model="ruleFilter" allow-clear placeholder="规则" size="small"
        ><a-option v-for="id in ruleIds" :key="id" :value="id">{{ id }}</a-option></a-select
      >
      <a-select v-model="stageFilter" allow-clear placeholder="阶段" size="small"
        ><a-option v-for="stage in stages" :key="stage" :value="stage">{{ stageLabel(stage) }}</a-option></a-select
      >
      <a-input v-model="keyword" allow-clear placeholder="标的" size="small" />
    </div>
    <a-table :row-key="rowKey" size="small" :data="filtered" :pagination="{ pageSize: 50 }" :scroll="{ x: 760 }">
      <template #columns>
        <a-table-column title="规则" data-index="rule_id" :width="140" />
        <a-table-column title="标的" data-index="instrument_id" :width="160" />
        <a-table-column title="阶段" :width="100"
          ><template #cell="{ record }"
            ><a-tag size="small" :color="stageColor(record.stage)">{{ stageLabel(record.stage) }}</a-tag></template
          ></a-table-column
        >
        <a-table-column title="分数" :width="120"
          ><template #cell="{ record }">{{ record.score || "-" }}</template></a-table-column
        >
        <a-table-column title="名次" :width="80"
          ><template #cell="{ record }">{{ record.rank || "-" }}</template></a-table-column
        >
        <a-table-column title="权重" :width="100"
          ><template #cell="{ record }">{{ record.weight ? percent(record.weight) : "-" }}</template></a-table-column
        >
        <a-table-column title="原因" data-index="reason" :ellipsis="true" :tooltip="true" />
      </template>
    </a-table>
  </div>
</template>

<script setup lang="ts">
import { computed, ref } from "vue";
import type { StrategyResult, StrategyResultItem } from "@/api/strategy-types";
import { parseSummary, percent, skipReasonLabel, stageColor, stageLabel } from "@/views/strategy/model";

const props = defineProps<{ result: StrategyResult; items: StrategyResultItem[] }>();
const ruleFilter = ref<string>();
const stageFilter = ref<string>();
const keyword = ref("");
const summary = computed(() => parseSummary(props.result.summary_json));
const ruleRows = computed(() => Object.entries(summary.value.rules).map(([id, value]) => ({ id, summary: value })));
const ruleIds = computed(() => [...new Set(props.items.map(item => item.rule_id))]);
const stages = computed(() => [...new Set(props.items.map(item => item.stage))]);
const detail = computed(() => {
  try {
    return JSON.parse(props.result.input_json || "{}").detail || "";
  } catch {
    return "";
  }
});
const filtered = computed(() =>
  props.items.filter(
    item =>
      (!ruleFilter.value || item.rule_id === ruleFilter.value) &&
      (!stageFilter.value || item.stage === stageFilter.value) &&
      (!keyword.value || item.instrument_id.toLowerCase().includes(keyword.value.trim().toLowerCase()))
  )
);
function rowKey(record: StrategyResultItem) {
  return `${record.rule_id}/${record.instrument_id}`;
}
</script>

<style scoped>
.items {
  display: grid;
  gap: 10px;
}
.block,
.note {
  margin: 0;
}
.rules {
  display: grid;
  gap: 6px;
}
.rule {
  display: flex;
  flex-wrap: wrap;
  gap: 4px 14px;
  padding: 8px 10px;
  background: var(--color-fill-2);
  font-size: 12px;
}
.rule strong {
  min-width: 120px;
}
.filters {
  display: flex;
  gap: 8px;
}
.filters > * {
  width: 180px;
}
@media (max-width: 640px) {
  .filters {
    flex-wrap: wrap;
  }
  .filters > * {
    width: 100%;
  }
}
</style>
