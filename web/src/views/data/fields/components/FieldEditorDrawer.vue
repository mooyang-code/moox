<template>
  <a-drawer
    class="field-editor-container"
    :visible="visible"
    width="min(560px, 100vw)"
    :title="editing ? '编辑字段' : '新增字段'"
    :footer="false"
    :mask="false"
    :mask-closable="false"
    :esc-to-close="false"
    unmount-on-close
    @cancel="requestClose"
  >
    <a-form ref="formRef" :model="form" layout="vertical">
      <a-divider orientation="left">基本信息</a-divider>
      <div class="form-grid">
        <a-form-item field="field_id" label="字段 ID" required>
          <a-input v-model="form.field_id" :disabled="editing" placeholder="例如 close" />
        </a-form-item>
        <a-form-item field="name" label="中文名" required><a-input v-model="form.name" /></a-form-item>
        <a-form-item class="span-2" field="description" label="描述"
          ><a-textarea v-model="form.description" :auto-size="{ minRows: 2, maxRows: 4 }"
        /></a-form-item>
      </div>

      <a-divider orientation="left">数据定义</a-divider>
      <div class="form-grid">
        <a-form-item field="value_type" label="值类型" required>
          <a-select v-model="form.value_type"
            ><a-option v-for="item in fieldValueTypeOptions" :key="item.value" :value="item.value">{{
              item.label
            }}</a-option></a-select
          >
        </a-form-item>
        <a-form-item field="unit" label="单位"><a-input v-model="form.unit" placeholder="例如 CNY、percent" /></a-form-item>
        <a-form-item
          class="span-2"
          field="validation_rule_json"
          label="校验规则 JSON"
          :validate-status="jsonError ? 'error' : undefined"
          :help="jsonError"
        >
          <a-textarea
            v-model="form.validation_rule_json"
            class="code-input"
            :auto-size="{ minRows: 3, maxRows: 7 }"
            @blur="validateJSON"
          />
        </a-form-item>
        <a-form-item class="span-2" field="write_example" label="写入示例"
          ><a-textarea v-model="form.write_example" :auto-size="{ minRows: 2, maxRows: 5 }"
        /></a-form-item>
      </div>

      <a-divider orientation="left">组织与管理</a-divider>
      <div class="form-grid">
        <a-form-item field="group_id" label="字段组" required>
          <a-select v-model="form.group_id" allow-search>
            <a-option v-for="group in groups" :key="group.group_id" :value="group.group_id">{{
              groupPath(groups, group.group_id)
            }}</a-option>
          </a-select>
        </a-form-item>
        <a-form-item field="sort_order" label="组内顺序"><a-input-number v-model="form.sort_order" :min="0" /></a-form-item>
        <a-form-item field="status" label="启用状态">
          <a-switch v-model="form.status" checked-value="active" unchecked-value="disabled" />
        </a-form-item>
      </div>
    </a-form>
    <div class="drawer-footer">
      <a-button :disabled="saving" @click="requestClose">取消</a-button>
      <a-button type="primary" :loading="saving" @click="submit">保存</a-button>
    </div>
  </a-drawer>
</template>

<script setup lang="ts">
import { computed, reactive, ref, watch } from "vue";
import { Message, Modal } from "@arco-design/web-vue";
import type { Field, FieldGroup } from "@/api/storage/types";
import { fieldValueTypeOptions, jsonText } from "@/views/data/shared/metadata-utils";
import { groupPath } from "../field-workbench";

const props = defineProps<{
  visible: boolean;
  field: Field | null;
  groups: FieldGroup[];
  initialGroupID: string;
  spaceID: string;
  saving: boolean;
}>();

const emit = defineEmits<{ close: []; save: [field: Field]; dirtyChange: [dirty: boolean] }>();
const editing = computed(() => Boolean(props.field?.field_id));
const formRef = ref();
const jsonError = ref("");
let snapshot = "";
const form = reactive<Field>(emptyField());

function emptyField(): Field {
  return {
    space_id: props.spaceID,
    group_id: props.initialGroupID || props.groups[0]?.group_id || "",
    field_id: "",
    name: "",
    description: "",
    value_type: "FIELD_VALUE_TYPE_STRING",
    unit: "",
    validation_rule_json: "{}",
    write_example: "",
    sort_order: 0,
    status: "active"
  };
}

watch(
  () => props.visible,
  visible => {
    if (!visible) {
      emit("dirtyChange", false);
      return;
    }
    Object.assign(
      form,
      emptyField(),
      props.field ? { ...props.field, validation_rule_json: jsonText(props.field.validation_rule_json) } : {}
    );
    jsonError.value = "";
    snapshot = JSON.stringify(form);
  }
);

watch(
  form,
  () => {
    if (props.visible) emit("dirtyChange", JSON.stringify(form) !== snapshot);
  },
  { deep: true }
);

function validateJSON() {
  try {
    JSON.parse(form.validation_rule_json || "{}");
    jsonError.value = "";
    return true;
  } catch {
    jsonError.value = "请输入合法的 JSON";
    return false;
  }
}

function requestClose() {
  if (JSON.stringify(form) === snapshot) {
    emit("close");
    return;
  }
  Modal.confirm({
    title: "放弃未保存修改？",
    content: "当前字段存在未保存修改。",
    okText: "放弃修改",
    onOk: () => emit("close")
  });
}

async function submit() {
  if (!form.field_id.trim() || !form.name.trim() || !form.group_id || !form.value_type) {
    Message.warning("请补全字段 ID、中文名、字段组和值类型");
    return;
  }
  if (!validateJSON()) return;
  const errors = await formRef.value?.validate?.();
  if (errors) return;
  emit("save", { ...form, space_id: props.spaceID, validation_rule_json: jsonText(form.validation_rule_json) });
}
</script>

<style scoped>
.form-grid {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 0 var(--moox-space-4);
}
.span-2 {
  grid-column: 1 / -1;
}
.code-input :deep(textarea) {
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
}
.drawer-footer {
  position: sticky;
  bottom: 0;
  display: flex;
  min-height: 60px;
  margin: var(--moox-space-2) calc(-1 * var(--moox-space-4)) calc(-1 * var(--moox-space-4));
  padding: var(--moox-space-3) var(--moox-space-4);
  border-top: 1px solid var(--color-border-2);
  align-items: center;
  justify-content: flex-end;
  gap: var(--moox-space-2);
  background: var(--color-bg-2);
}
:global(.field-editor-container) {
  pointer-events: none;
}
:global(.field-editor-container .arco-drawer) {
  pointer-events: auto;
}
@media (max-width: 520px) {
  .form-grid {
    grid-template-columns: 1fr;
  }
  .span-2 {
    grid-column: auto;
  }
}
</style>
