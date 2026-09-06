<template>
  <div class="moox-page"><div class="moox-inner">
    <div class="page-head"><div><a-button type="text" @click="leave"><template #icon><icon-left /></template>返回定义</a-button><h2>{{ editing ? "编辑策略定义" : "新建策略定义" }}</h2><span>这里只保存 DSL，不会创建实例或启用策略。</span></div><a-space><a-button :disabled="loading || saving" @click="loadTemplate('ranked')">排序模板</a-button><a-button :disabled="loading || saving" @click="loadTemplate('signal')">时序模板</a-button><a-button type="primary" :disabled="!loaded || loading" :loading="saving" @click="save">保存定义</a-button></a-space></div>
    <a-alert v-if="error" type="error" show-icon class="top-alert">{{ error }}</a-alert>
    <a-grid :cols="{ xs: 1, sm: 2 }" :col-gap="20" :row-gap="16">
      <a-grid-item><a-form layout="vertical"><a-form-item label="策略 ID" required><a-input v-model="strategyId" :disabled="editing || loading" placeholder="例如 momentum_hourly" /></a-form-item><a-alert v-if="parse.diagnostics.length" type="warning" show-icon><div v-for="item in parse.diagnostics" :key="item.message">{{ item.message }}</div></a-alert><div class="editor-label">DSL YAML</div><a-textarea v-model="source" :disabled="!loaded || loading" class="code-input" :auto-size="{ minRows: 24, maxRows: 34 }" /></a-form></a-grid-item>
      <a-grid-item><div class="summary-pane"><div class="section-title">定义摘要</div><a-empty v-if="!parse.preview" description="等待合法 YAML" /><a-descriptions v-else :column="1" bordered><a-descriptions-item label="名称">{{ parse.preview.name }}</a-descriptions-item><a-descriptions-item label="K 线周期">{{ parse.preview.bar }}</a-descriptions-item><a-descriptions-item label="交易日历">{{ parse.preview.calendar }}</a-descriptions-item><a-descriptions-item label="触发方式">{{ parse.preview.triggers.join("、") || "未配置" }}</a-descriptions-item><a-descriptions-item label="规则">{{ parse.preview.rules.join("、") || "未配置" }}</a-descriptions-item></a-descriptions><a-alert type="info" show-icon class="note">YAML 语法通过不等于运行依赖通过。实例启用时，后台会再次校验 View、Factor、频率和账户会话。</a-alert></div></a-grid-item>
    </a-grid>
  </div></div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref, watch } from "vue";
import { Message } from "@arco-design/web-vue";
import { onBeforeRouteLeave, onBeforeRouteUpdate, useRoute, useRouter } from "vue-router";
import { createStrategy, getStrategy, updateStrategy } from "@/api/strategy";
import { parseDSL, rankedTemplate, signalTemplate } from "@/views/strategy/dsl";

defineOptions({ name: "StrategyEditor" });
const route = useRoute();
const router = useRouter();
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
    const response = await getStrategy(strategyId.value);
    if (requestId !== loadRequest || response.strategy.strategy_id !== strategyId.value) return;
    source.value = response.strategy.dsl_yaml;
    original.value = source.value;
    loaded.value = true;
  } catch (err) { if (requestId === loadRequest) error.value = err instanceof Error ? err.message : "策略定义加载失败"; }
  finally { if (requestId === loadRequest) loading.value = false; }
}
function loadTemplate(kind: "ranked" | "signal") { if (!loaded.value) return; if (dirty.value && !window.confirm("当前 DSL 尚未保存，确认替换？")) return; source.value = kind === "ranked" ? rankedTemplate : signalTemplate; }
async function save() {
  error.value = "";
  if (!loaded.value) { error.value = "策略定义尚未加载完成，暂不能保存"; return; }
  if (!strategyId.value.trim()) { error.value = "策略 ID 不能为空"; return; }
  if (parse.value.diagnostics.some(item => item.message)) { error.value = "请先修正 YAML 解析错误"; return; }
  saving.value = true;
  try {
    if (editing.value) await updateStrategy(strategyId.value.trim(), source.value);
    else await createStrategy({ strategy_id: strategyId.value.trim(), dsl_yaml: source.value });
    original.value = source.value;
    Message.success("策略定义已保存");
    router.push({ name: "strategy-overview" });
  } catch (err) { error.value = err instanceof Error ? err.message : "策略定义保存失败"; }
  finally { saving.value = false; }
}
function leave() { router.push({ name: "strategy-overview" }); }
onBeforeRouteLeave(() => { if (!dirty.value || window.confirm("当前 DSL 尚未保存，确认离开？")) return true; return false; });
onBeforeRouteUpdate(() => { if (!dirty.value || window.confirm("当前 DSL 尚未保存，确认切换策略定义？")) return true; return false; });
watch(() => route.params.strategyId, () => {
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
});
onMounted(load);
</script>

<style scoped>
.page-head { display: flex; align-items: flex-start; justify-content: space-between; gap: 16px; margin-bottom: var(--moox-space-2); }
.page-head h2 { margin: 8px 0 4px; }
.page-head span { color: var(--color-text-3); font-size: 12px; }
.top-alert { margin-bottom: var(--moox-space-2); }
.editor-label, .section-title { margin-bottom: 8px; font-weight: 600; }
.code-input, .state-json { font: 12px/1.6 ui-monospace, SFMono-Regular, Menlo, monospace; }
.summary-pane { min-height: 100%; padding-top: 31px; }
.note { margin-top: 16px; }
@media (max-width: 640px) { .page-head { align-items: flex-start; flex-direction: column; } .summary-pane { padding-top: 0; } }
</style>
