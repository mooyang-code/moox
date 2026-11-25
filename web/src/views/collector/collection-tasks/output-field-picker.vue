<template>
  <a-drawer
    v-model:visible="visible"
    title="选择输出字段"
    width="720px"
    :mask-closable="false"
    unmount-on-close
    @close="draft = selected.slice()"
  >
    <a-alert type="info" show-icon>
      {{
        editing
          ? "输出字段只能减少，要新增请新建任务。"
          : "按分组列出可选的输出字段；复权因子、资金费率等附加字段只在所选标签和频率支持时出现，默认不勾。"
      }}
    </a-alert>
    <a-alert v-if="loadError" class="output-field-error" type="error" show-icon>
      字段目录加载失败 <a-link @click="emit('retry')">重试</a-link>
    </a-alert>
    <a-spin :loading="loading" class="output-field-picker-spin">
      <div class="output-field-toolbar">
        <a-input v-model="keyword" allow-clear placeholder="搜索字段名称或 ID" />
        <span>{{ draft.length }} 个已选择</span>
      </div>
      <div class="output-field-sections">
        <section v-for="section in sections" :key="section.name" class="output-field-section">
          <header class="output-field-section__header">
            <div>
              <strong>{{ section.name }}</strong
              ><span>{{ section.fields.length }} 个字段</span>
            </div>
            <a-button
              v-if="selectableFields(section.fields).length"
              size="mini"
              type="text"
              @click="toggleSection(section.fields)"
            >
              {{ sectionToggleText(section.fields) }}
            </a-button>
          </header>
          <div class="output-field-grid">
            <label v-for="field in section.fields" :key="field.field_id" class="output-field-option" :title="field.field_id">
              <a-checkbox
                :model-value="draft.includes(field.field_id)"
                :disabled="!canSelect(field.field_id)"
                @change="setField(field.field_id, $event)"
              />
              <span class="output-field-option__text"
                ><strong>{{ field.display_name }}</strong
                ><code>{{ field.field_id }}</code></span
              >
            </label>
          </div>
        </section>
        <a-empty v-if="!loading && !sections.length" :description="keyword ? '没有匹配的字段' : '当前执行任务类型没有可选字段'" />
      </div>
    </a-spin>
    <template #footer>
      <a-space>
        <a-button @click="visible = false">取消</a-button>
        <a-button type="primary" @click="confirm">确认选择</a-button>
      </a-space>
    </template>
  </a-drawer>
</template>

<script setup lang="ts">
import { computed, ref, watch } from "vue";
import { outputFieldSections, outputFieldSelectable, outputFieldVisible, type OutputFieldEntry } from "./collection-task-params";

defineOptions({ name: "OutputFieldPicker" });

const props = defineProps<{
  /** 输出字段目录：GetDataTypeConfigWithFields 的基础输出字段与附加字段。 */
  catalog: OutputFieldEntry[];
  /** 当前条件下可选的附加字段（小写字段 ID）。 */
  available: ReadonlySet<string>;
  /** 基础输出字段。 */
  base: ReadonlySet<string>;
  /** 编辑已有任务：输出字段只能减少。 */
  editing?: boolean;
  /** 任务原有的输出字段（编辑时只有这些能勾）。 */
  originalFields?: string[];
  loading?: boolean;
  loadError?: boolean;
}>();

const visible = defineModel<boolean>("visible", { required: true });
/** 已确认的输出字段；抽屉里先改草稿，点“确认选择”才写回。 */
const selected = defineModel<string[]>("selected", { required: true });

const emit = defineEmits<{ (event: "retry"): void }>();

const keyword = ref("");
const draft = ref<string[]>([]);

watch(
  visible,
  open => {
    if (!open) return;
    keyword.value = "";
    draft.value = selected.value.slice();
  },
  { immediate: true }
);

const sections = computed(() =>
  outputFieldSections(props.catalog, id => outputFieldVisible(id, props.available, props.base), keyword.value)
);

/** 编辑已有任务时输出字段只能减少：不在任务原有字段里的不能勾。 */
function canSelect(fieldId: string): boolean {
  return outputFieldSelectable(fieldId, Boolean(props.editing), props.originalFields ?? [], props.base);
}

function setField(fieldId: string, checked: boolean | string | number) {
  const on = Boolean(checked) && canSelect(fieldId);
  draft.value = on ? [...new Set([...draft.value, fieldId])] : draft.value.filter(id => id !== fieldId);
}

/** 一组里能勾上的字段（编辑任务时不含不能新增的那些）。 */
function selectableFields(fields: OutputFieldEntry[]) {
  return fields.filter(field => canSelect(field.field_id));
}

function sectionAllSelected(fields: OutputFieldEntry[]) {
  return selectableFields(fields).every(field => draft.value.includes(field.field_id));
}

function sectionToggleText(fields: OutputFieldEntry[]) {
  if (sectionAllSelected(fields)) return keyword.value ? "取消匹配项" : "取消本组";
  return keyword.value ? "选择匹配项" : "选择本组";
}

function toggleSection(fields: OutputFieldEntry[]) {
  const ids = new Set(selectableFields(fields).map(field => field.field_id));
  draft.value = sectionAllSelected(fields) ? draft.value.filter(id => !ids.has(id)) : [...new Set([...draft.value, ...ids])];
}

function confirm() {
  selected.value = draft.value.slice();
  visible.value = false;
}
</script>

<style scoped>
.output-field-error {
  margin-top: 12px;
}
.output-field-picker-spin {
  display: block;
  margin-top: 14px;
}
.output-field-toolbar {
  display: flex;
  align-items: center;
  gap: 14px;
  margin-bottom: 12px;
}
.output-field-toolbar > :first-child {
  max-width: 420px;
}
.output-field-toolbar > span {
  flex: none;
  color: var(--color-text-3);
  font-size: 12px;
}
.output-field-sections {
  display: grid;
  gap: 10px;
  max-height: min(56vh, 560px);
  overflow: auto;
  padding: 1px 4px 4px 1px;
}
.output-field-section {
  overflow: hidden;
  border: 1px solid var(--color-border-2);
  border-radius: 6px;
  background: var(--color-bg-2);
}
.output-field-section__header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  padding: 8px 12px;
  border-bottom: 1px solid var(--color-border-2);
  background: var(--color-fill-1);
}
.output-field-section__header > div {
  display: flex;
  align-items: center;
  gap: 10px;
  min-width: 0;
}
.output-field-section__header strong {
  overflow: hidden;
  color: var(--color-text-1);
  font-size: 13px;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.output-field-section__header span {
  flex: none;
  color: var(--color-text-3);
  font-size: 12px;
}
.output-field-grid {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 1px 12px;
  padding: 8px 12px;
}
.output-field-option {
  display: flex;
  align-items: flex-start;
  gap: 8px;
  min-width: 0;
  padding: 6px 2px;
  cursor: pointer;
}
.output-field-option__text {
  display: flex;
  flex-direction: column;
  min-width: 0;
  gap: 2px;
}
.output-field-option__text strong {
  overflow: hidden;
  color: var(--color-text-1);
  font-size: 13px;
  font-weight: 500;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.output-field-option__text code {
  overflow: hidden;
  color: var(--color-text-3);
  font-size: 11px;
  text-overflow: ellipsis;
  white-space: nowrap;
}
@media (max-width: 640px) {
  .output-field-grid {
    grid-template-columns: minmax(0, 1fr);
  }
  .output-field-toolbar {
    align-items: stretch;
    flex-direction: column;
    gap: 6px;
  }
}
</style>
