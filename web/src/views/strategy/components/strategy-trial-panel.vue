<template>
  <div class="trial">
    <div class="trial-head">
      <strong>试算</strong>
      <span class="muted">在选定 View 的最近一期数据上求值一次，结果不落库。</span>
    </div>
    <div class="trial-bar">
      <a-select
        v-model="viewId"
        allow-search
        allow-clear
        :loading="viewsLoading"
        placeholder="选择 View"
        @popup-visible-change="ensureViews"
      >
        <a-option v-for="view in views" :key="view.view_id" :value="view.view_id"
          >{{ view.name || view.view_id }}（{{ view.view_id }} · {{ view.freq || "无周期" }}）</a-option
        >
      </a-select>
      <a-button type="primary" :loading="running" :disabled="!source.trim()" @click="run">{{
        viewId ? "校验并试算" : "仅校验 DSL"
      }}</a-button>
    </div>
    <a-alert v-if="viewsError" type="error" show-icon class="block">View 列表加载失败：{{ viewsError }}</a-alert>
    <template v-if="result">
      <a-alert v-if="!result.diagnostics.length && !result.trial" type="success" show-icon class="block">DSL 校验通过</a-alert>
      <a-alert
        v-for="item in result.diagnostics"
        :key="item"
        :type="result.trial || result.resolved_json ? 'info' : 'error'"
        class="block"
        >{{ item }}</a-alert
      >
      <a-tabs v-if="result.resolved_json || result.trial" default-active-key="trial">
        <a-tab-pane v-if="result.trial" key="trial" title="试算结果">
          <a-space wrap class="block">
            <a-tag :color="result.trial.status === 'ok' ? 'green' : 'orange'">{{
              result.trial.status === "ok" ? "ok" : `跳过：${skipReasonLabel(result.trial.skip_reason)}`
            }}</a-tag>
            <a-tag>K 线结束 {{ formatStrategyTime(result.trial.bar_end_time) }}</a-tag>
            <a-tag v-for="target in result.trial.targets" :key="target.instrument_id" color="arcoblue"
              >{{ target.instrument_id }} {{ percent(target.target_weight) }}</a-tag
            >
          </a-space>
          <ResultItems :result="result.trial" :items="result.trial_items" />
        </a-tab-pane>
        <a-tab-pane v-if="result.resolved_json" key="binding" title="绑定解析"
          ><ResolvedTable :resolved-json="result.resolved_json"
        /></a-tab-pane>
      </a-tabs>
    </template>
  </div>
</template>

<script setup lang="ts">
import { ref, watch } from "vue";
import { validateStrategy } from "@/api/strategy";
import type { ValidateStrategyResult } from "@/api/strategy-types";
import { listViews } from "@/api/storage/metadata";
import type { View } from "@/api/storage/types";
import ResolvedTable from "@/views/strategy/components/strategy-resolved-table.vue";
import ResultItems from "@/views/strategy/components/strategy-result-items.vue";
import { formatStrategyTime, percent, skipReasonLabel } from "@/views/strategy/model";

const props = defineProps<{ source: string; spaceId: string }>();
const viewId = ref("");
const views = ref<View[]>([]);
const viewsLoading = ref(false);
const running = ref(false);
const result = ref<ValidateStrategyResult | null>(null);
const viewsError = ref("");
let loadedSpace = "";
// 切换空间时两个计数都递增，让旧空间的 View 列表与试算结果作废。
let viewsRequest = 0;
let runRequest = 0;

async function ensureViews(visible = true) {
  const spaceId = props.spaceId;
  if (!visible || !spaceId || loadedSpace === spaceId) return;
  const requestId = ++viewsRequest;
  viewsLoading.value = true;
  try {
    const items: View[] = [];
    for (let page = 1; ; page += 1) {
      const rsp = await listViews({ space_id: spaceId, status: "active", page: { page, size: 200 } });
      if (requestId !== viewsRequest) return;
      items.push(...(rsp.views || []));
      if (!rsp.page_result?.has_more || !(rsp.views || []).length) break;
    }
    views.value = items;
    loadedSpace = spaceId;
    viewsError.value = "";
  } catch (err) {
    if (requestId === viewsRequest) viewsError.value = err instanceof Error ? err.message : "View 列表加载失败";
  } finally {
    if (requestId === viewsRequest) viewsLoading.value = false;
  }
}

async function run() {
  const requestId = ++runRequest;
  running.value = true;
  try {
    const next = await validateStrategy(props.source, viewId.value);
    if (requestId === runRequest) result.value = next;
  } catch (err) {
    if (requestId === runRequest)
      result.value = {
        diagnostics: [err instanceof Error ? err.message : "试算失败"],
        resolved_json: "",
        trial: null,
        trial_items: []
      };
  } finally {
    if (requestId === runRequest) running.value = false;
  }
}

// DSL 或 View 一变，在途的试算作废、已有结果清除：不能在当前输入下展示旧输入的校验与解释。
watch(
  () => [props.source, viewId.value],
  () => {
    runRequest += 1;
    result.value = null;
    running.value = false;
  },
  { flush: "sync" }
);

watch(
  () => props.spaceId,
  () => {
    viewsRequest += 1;
    runRequest += 1;
    loadedSpace = "";
    views.value = [];
    viewId.value = "";
    result.value = null;
    viewsLoading.value = false;
    viewsError.value = "";
    running.value = false;
  }
);
</script>

<style scoped>
.trial {
  display: grid;
  gap: 10px;
  margin-top: 16px;
}
.trial-head {
  display: flex;
  align-items: baseline;
  gap: 10px;
}
.trial-bar {
  display: flex;
  gap: 8px;
}
.trial-bar .arco-select {
  flex: 1;
}
.muted {
  color: var(--color-text-3);
  font-size: 12px;
}
.block {
  margin-bottom: 8px;
}
@media (max-width: 640px) {
  .trial-bar {
    flex-direction: column;
  }
}
</style>
