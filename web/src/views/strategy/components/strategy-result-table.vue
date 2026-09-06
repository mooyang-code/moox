<template>
  <div class="result-toolbar">
    <a-radio-group :model-value="scope" type="button" @change="changeScope"><a-radio value="session">本次启用</a-radio><a-radio value="all">全部历史</a-radio></a-radio-group>
    <span class="muted">投递状态不代表成交状态</span>
  </div>
  <a-table row-key="result_id" size="small" :data="results" :pagination="pagination" :scroll="{ x: 820 }" @page-change="changePage">
    <template #columns>
      <a-table-column title="结果 ID" data-index="result_id" :width="230" :ellipsis="true" :tooltip="true" />
      <a-table-column title="K 线结束" :width="180"><template #cell="{ record }">{{ formatStrategyTime(record.bar_end_time || record.period_time) }}</template></a-table-column>
      <a-table-column title="投递状态" :width="110"><template #cell="{ record }"><a-tag size="small" :color="publishColor(record.publish_status)">{{ publishLabel(record.publish_status) }}</a-tag></template></a-table-column>
      <a-table-column title="有效至" :width="180"><template #cell="{ record }">{{ formatStrategyTime(record.valid_until) }}</template></a-table-column>
      <a-table-column title="目标数" :width="90"><template #cell="{ record }">{{ record.targets.length }}</template></a-table-column>
      <a-table-column title="结果详情" :width="170"><template #cell="{ record }"><a-button type="text" size="small" @click="showTargets(record.targets)">目标</a-button><a-button type="text" size="small" @click="showState(record.rule_states_json)">规则</a-button></template></a-table-column>
    </template>
  </a-table>
  <a-empty v-if="!results.length" description="暂无策略结果" />
  <a-modal v-model:visible="targetVisible" title="目标明细（只读）" :footer="false" :width="680"><pre class="state-json">{{ targetText }}</pre></a-modal>
  <a-modal v-model:visible="stateVisible" title="规则状态（只读）" :footer="false" :width="680"><pre class="state-json">{{ stateText }}</pre></a-modal>
</template>

<script setup lang="ts">
import { computed, ref } from "vue";
import type { StrategyResult } from "@/api/strategy-types";
import { formatStrategyTime } from "@/views/strategy/model";
const props = defineProps<{ results: StrategyResult[]; total: number; page: number; pageSize: number; scope: "session" | "all" }>();
const emit = defineEmits<{ page: [number, "session" | "all"] }>();
const stateVisible = ref(false);
const stateText = ref("{}");
const targetVisible = ref(false);
const targetText = ref("[]");
const pagination = computed(() => ({ current: props.page, pageSize: props.pageSize, total: props.total }));
const scope = computed(() => props.scope);
function changePage(page: number) { emit("page", page, props.scope); }
function changeScope(value: string) { emit("page", 1, value === "all" ? "all" : "session"); }
function showTargets(value: StrategyResult["targets"]) { targetText.value = JSON.stringify(value || [], null, 2); targetVisible.value = true; }
function showState(value?: string) { stateText.value = value || "{}"; stateVisible.value = true; }
function publishLabel(value?: string) { return ({ none: "无需投递", pending: "待投递", sent: "已发送", cancelled: "已取消" } as Record<string, string>)[value || ""] || "未知"; }
function publishColor(value?: string) { return ({ pending: "orange", sent: "green", cancelled: "gray" } as Record<string, string>)[value || ""] || "blue"; }
</script>

<style scoped>
.result-toolbar { display: flex; align-items: center; justify-content: space-between; gap: 12px; margin-bottom: 12px; }
.muted { color: var(--color-text-3); font-size: 12px; }
.state-json { max-height: 480px; overflow: auto; padding: 12px; background: var(--color-fill-2); white-space: pre-wrap; font: 12px/1.6 ui-monospace, SFMono-Regular, Menlo, monospace; }
@media (max-width: 640px) { .result-toolbar { align-items: flex-start; flex-direction: column; } }
</style>
