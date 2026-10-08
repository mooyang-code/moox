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
let loadedSpace = "";
let runRequest = 0;

async function ensureViews(visible = true) {
  if (!visible || !props.spaceId || loadedSpace === props.spaceId) return;
  viewsLoading.value = true;
  try {
    const items: View[] = [];
    for (let page = 1; ; page += 1) {
      const rsp = await listViews({ space_id: props.spaceId, status: "active", page: { page, size: 200 } });
      items.push(...(rsp.views || []));
      if (!rsp.page_result?.has_more || !(rsp.views || []).length) break;
    }
    views.value = items;
    loadedSpace = props.spaceId;
  } finally {
    viewsLoading.value = false;
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

watch(
  () => props.spaceId,
  () => {
    loadedSpace = "";
    views.value = [];
    viewId.value = "";
    result.value = null;
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
