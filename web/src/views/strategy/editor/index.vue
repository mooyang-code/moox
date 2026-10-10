<template>
  <div class="moox-page">
    <div class="moox-inner">
      <div class="page-head">
        <div>
          <a-button type="text" @click="leave"
            ><template #icon><icon-left /></template>返回定义</a-button
          >
          <h2>{{ editing ? "编辑策略定义" : "新建策略定义" }}</h2>
          <span>这里只保存 DSL，不会创建实例或启用策略。</span>
        </div>
        <a-space
          ><a-button :disabled="loading || saving" @click="loadTemplate('ranked')">截面选股模板</a-button
          ><a-button :disabled="loading || saving" @click="loadTemplate('signal')">择时模板</a-button
          ><a-button type="primary" :disabled="!loaded || loading" :loading="saving" @click="save">保存定义</a-button></a-space
        >
      </div>
      <a-alert v-if="error" type="error" show-icon class="top-alert">{{ error }}</a-alert>
      <a-grid :cols="{ xs: 1, sm: 2 }" :col-gap="20" :row-gap="16">
        <a-grid-item
          ><a-form layout="vertical"
            ><a-form-item label="策略 ID"
              ><a-input
                v-model="strategyId"
                :disabled="editing || loading"
                placeholder="留空自动生成，例如 momentum_hourly" /></a-form-item
            ><a-alert v-if="parse.diagnostics.length" type="warning" show-icon
              ><div v-for="item in parse.diagnostics" :key="item.message">{{ item.message }}</div></a-alert
            >
            <div class="editor-label">DSL YAML</div>
            <a-textarea
              v-model="source"
              :disabled="!loaded || loading"
              :readonly="saving"
              class="code-input"
              :auto-size="{ minRows: 24, maxRows: 36 }" /></a-form
        ></a-grid-item>
        <a-grid-item>
          <div class="summary-pane">
            <div class="section-title">定义摘要</div>
            <a-empty v-if="!parse.preview" description="等待合法 YAML" />
            <a-descriptions v-else :column="1" bordered size="small">
              <a-descriptions-item label="名称">{{ parse.preview.name }}</a-descriptions-item>
              <a-descriptions-item label="bar 断言">{{ parse.preview.bar }}</a-descriptions-item>
              <a-descriptions-item label="标的池">{{ parse.preview.universe }}</a-descriptions-item>
              <a-descriptions-item label="杠杆上限">{{ parse.preview.leverage }}</a-descriptions-item>
              <a-descriptions-item label="规则"
                ><div v-for="rule in parse.preview.rules" :key="rule.id">
                  {{ rule.name || rule.id }} · <span class="mono">{{ rule.id }}</span> ·
                  {{ rule.type === "rank" ? "截面选股" : rule.type === "signal" ? "固定池择时" : rule.type }}
                </div></a-descriptions-item
              >
            </a-descriptions>
            <a-alert type="info" show-icon class="note"
              >规则 id 是规则状态与解释明细的稳定标识，修改 id 等于从零开始；name 只用于展示。</a-alert
            >
            <TrialPanel :source="source" :space-id="spaceStore.selectedSpaceId" />
          </div>
        </a-grid-item>
      </a-grid>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref, watch } from "vue";
import { Message } from "@arco-design/web-vue";
import { onBeforeRouteLeave, onBeforeRouteUpdate, useRoute, useRouter } from "vue-router";
import { createStrategy, getStrategy, updateStrategy } from "@/api/strategy";
import { useSpaceStore } from "@/store/modules/space";
import TrialPanel from "@/views/strategy/components/strategy-trial-panel.vue";
import { parseDSL, rankedTemplate, signalTemplate } from "@/views/strategy/dsl";

defineOptions({ name: "StrategyEditor" });
const route = useRoute();
const router = useRouter();
const spaceStore = useSpaceStore();
const editing = computed(() => Boolean(route.params.strategyId));
const strategyId = ref(String(route.params.strategyId || ""));
const source = ref(editing.value ? "" : rankedTemplate);
const original = ref(source.value);
const loaded = ref(!editing.value);
const loading = ref(false);
const saving = ref(false);
const error = ref("");
const parse = computed(() => parseDSL(source.value));
const dirty = computed(() => source.value !== original.value);
let loadRequest = 0;

async function load() {
  if (!editing.value) return;
  const requestId = ++loadRequest;
  loading.value = true;
  loaded.value = false;
  error.value = "";
  source.value = "";
  original.value = "";
  try {
    const strategy = await getStrategy(strategyId.value);
    if (requestId !== loadRequest || strategy.strategy_id !== strategyId.value) return;
    source.value = strategy.dsl_yaml;
    original.value = source.value;
    loaded.value = true;
  } catch (err) {
    if (requestId === loadRequest) error.value = err instanceof Error ? err.message : "策略定义加载失败";
  } finally {
    if (requestId === loadRequest) loading.value = false;
  }
}
function loadTemplate(kind: "ranked" | "signal") {
  if (!loaded.value) return;
  if (dirty.value && !window.confirm("当前 DSL 尚未保存，确认替换？")) return;
  source.value = kind === "ranked" ? rankedTemplate : signalTemplate;
}
async function save() {
  error.value = "";
  if (!loaded.value) {
    error.value = "策略定义尚未加载完成，暂不能保存";
    return;
  }
  if (!parse.value.preview) {
    error.value = "请先修正 YAML 解析错误";
    return;
  }
  saving.value = true;
  // 保存的是点击时的文本快照：只把这份快照标记为已保存，请求期间若文本变了，变化仍算未保存的草稿。
  const submitted = source.value;
  try {
    if (editing.value) await updateStrategy(strategyId.value.trim(), submitted);
    else await createStrategy({ strategy_id: strategyId.value.trim() || undefined, dsl_yaml: submitted });
    original.value = submitted;
    Message.success("策略定义已保存");
    router.push({ name: "strategy-overview" });
  } catch (err) {
    error.value = err instanceof Error ? err.message : "策略定义保存失败";
  } finally {
    saving.value = false;
  }
}
function leave() {
  router.push({ name: "strategy-overview" });
}
onBeforeRouteLeave(() => {
  if (!dirty.value || window.confirm("当前 DSL 尚未保存，确认离开？")) return true;
  return false;
});
onBeforeRouteUpdate(() => {
  if (!dirty.value || window.confirm("当前 DSL 尚未保存，确认切换策略定义？")) return true;
  return false;
});
watch(
  () => route.params.strategyId,
  () => {
    strategyId.value = String(route.params.strategyId || "");
    if (editing.value) load();
    else {
      loadRequest += 1;
      loading.value = false;
      loaded.value = true;
      source.value = rankedTemplate;
      original.value = source.value;
      error.value = "";
    }
  }
);
onMounted(load);
</script>

<style scoped>
.page-head {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 16px;
  margin-bottom: var(--moox-space-2);
}
.page-head h2 {
  margin: 8px 0 4px;
}
.page-head span {
  color: var(--color-text-3);
  font-size: 12px;
}
.top-alert {
  margin-bottom: var(--moox-space-2);
}
.editor-label,
.section-title {
  margin-bottom: 8px;
  font-weight: 600;
}
.code-input {
  font:
    12px/1.6 ui-monospace,
    SFMono-Regular,
    Menlo,
    monospace;
}
.mono {
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  font-size: 12px;
}
.summary-pane {
  min-height: 100%;
  padding-top: 31px;
}
.note {
  margin-top: 16px;
}
@media (max-width: 640px) {
  .page-head {
    align-items: flex-start;
    flex-direction: column;
  }
  .summary-pane {
    padding-top: 0;
  }
}
</style>
