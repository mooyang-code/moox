<template>
  <a-drawer :visible="visible" :width="'min(860px, 100vw)'" title="从模板新建策略" unmount-on-close @cancel="close">
    <a-alert v-if="error" type="error" show-icon class="form-alert">{{ error }}</a-alert>
    <a-form layout="vertical">
      <a-form-item label="模板" required>
        <a-select v-model="templateId" :loading="loading" placeholder="选择一个模板" @change="onTemplateChange">
          <a-option v-for="item in templates" :key="item.template_id" :value="item.template_id"
            >{{ item.name }}（{{ item.template_id }}）</a-option
          >
        </a-select>
        <template #extra>
          <span v-if="!loading && !templates.length">还没有模板。模板用 moox-cli 保存，见策略模块文档“策略模板”一节。</span>
          <span v-else-if="selected?.description">{{ selected.description }}</span>
        </template>
      </a-form-item>
      <template v-if="selected">
        <a-form-item label="策略名" required>
          <a-input v-model="name" placeholder="生成的策略名" />
        </a-form-item>
        <a-form-item v-for="variable in selected.variables" :key="variable" :label="variable">
          <a-input v-model="variables[variable]" placeholder="留空用模板的默认值" />
        </a-form-item>
        <a-form-item v-if="selected.columns.length" label="参数表（CSV）" required>
          <a-textarea v-model="paramsCsv" :auto-size="{ minRows: 4, maxRows: 12 }" class="mono" />
          <template #extra>
            <span>第一行是表头 {{ selected.columns.join(",") }}，之后每行一个子策略。</span>
            <input type="file" accept=".csv,text/csv" class="file-input" @change="onFile" />
          </template>
        </a-form-item>
        <a-space>
          <a-button :loading="rendering" @click="render">生成预览</a-button>
          <a-button type="primary" status="success" :disabled="!dsl" :loading="creating" @click="create">创建策略</a-button>
        </a-space>
        <a-form-item v-if="dsl" label="生成的 DSL" class="preview">
          <a-textarea :model-value="dsl" readonly :auto-size="{ minRows: 8, maxRows: 24 }" class="mono" />
        </a-form-item>
      </template>
    </a-form>
  </a-drawer>
</template>

<script setup lang="ts">
import { computed, reactive, ref, watch } from "vue";
import { Message } from "@arco-design/web-vue";
import { createStrategy, listStrategyTemplates, renderStrategyTemplate } from "@/api/strategy";
import type { StrategyTemplate } from "@/api/strategy-types";

defineOptions({ name: "StrategyTemplateCreate" });
const props = defineProps<{ visible: boolean }>();
const emit = defineEmits<{ close: []; created: [strategyId: string] }>();

const templates = ref<StrategyTemplate[]>([]);
const loading = ref(false);
const templateId = ref("");
const name = ref("");
const variables = reactive<Record<string, string>>({});
const paramsCsv = ref("");
const dsl = ref("");
const error = ref("");
const rendering = ref(false);
const creating = ref(false);
// 预览请求的版本号：输入变化或重新生成后，晚到的旧响应不写回预览，避免创建出与当前表单不一致的策略。
let renderVersion = 0;
let lastRequest = 0;
const selected = computed(() => templates.value.find(item => item.template_id === templateId.value));

function message(err: unknown) {
  return err instanceof Error ? err.message : String(err);
}
async function load() {
  loading.value = true;
  error.value = "";
  try {
    templates.value = await listStrategyTemplates();
  } catch (err) {
    error.value = `模板列表读取失败：${message(err)}`;
  } finally {
    loading.value = false;
  }
}
function onTemplateChange() {
  for (const key of Object.keys(variables)) delete variables[key];
  paramsCsv.value = selected.value?.columns.length ? `${selected.value.columns.join(",")}\n` : "";
  dsl.value = "";
}
async function onFile(event: Event) {
  const file = (event.target as HTMLInputElement).files?.[0];
  if (file) paramsCsv.value = await file.text();
}
// 输入变化后旧预览作废，必须重新生成才能创建。
watch(
  [templateId, name, paramsCsv, () => ({ ...variables })],
  () => {
    renderVersion++;
    dsl.value = "";
  },
  { deep: true }
);
async function render() {
  if (!selected.value) return;
  error.value = "";
  rendering.value = true;
  const version = ++renderVersion;
  lastRequest = version;
  try {
    const values: Record<string, string> = { name: name.value.trim() };
    for (const [key, value] of Object.entries(variables)) if (value.trim()) values[key] = value.trim();
    const text = await renderStrategyTemplate(selected.value.template_id, values, paramsCsv.value);
    if (version === renderVersion) dsl.value = text;
  } catch (err) {
    if (version === renderVersion) error.value = message(err);
  } finally {
    // 只有最后一次发出的请求结束时才取消加载状态；输入变化不会发请求，所以它也会在这里复位。
    if (version === lastRequest) rendering.value = false;
  }
}
async function create() {
  creating.value = true;
  error.value = "";
  try {
    const strategy = await createStrategy({ dsl_yaml: dsl.value });
    Message.success(`策略「${strategy.name}」已创建`);
    emit("created", strategy.strategy_id);
  } catch (err) {
    error.value = message(err);
  } finally {
    creating.value = false;
  }
}
function close() {
  emit("close");
}
watch(
  () => props.visible,
  next => {
    if (next) void load();
  },
  { immediate: true }
);
</script>

<style scoped lang="scss">
.form-alert {
  margin-bottom: 12px;
}
.preview {
  margin-top: 16px;
}
.mono :deep(textarea) {
  font-family: var(--moox-font-mono, monospace);
}
.file-input {
  display: block;
  margin-top: 6px;
}
</style>
