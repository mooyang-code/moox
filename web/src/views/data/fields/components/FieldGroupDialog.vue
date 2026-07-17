<template>
  <a-modal
    :visible="visible"
    width="520px"
    :title="editing ? '编辑字段组' : '新增字段组'"
    :ok-loading="saving"
    @cancel="$emit('close')"
    @ok="submit"
  >
    <a-form :model="form" layout="vertical">
      <a-form-item field="parent_group_id" label="父字段组">
        <a-select v-model="form.parent_group_id" allow-clear placeholder="留空表示一级字段组">
          <a-option
            v-for="root in roots"
            :key="root.group_id"
            :value="root.group_id"
            :disabled="root.group_id === form.group_id"
            >{{ root.name }}</a-option
          >
        </a-select>
      </a-form-item>
      <div class="form-grid">
        <a-form-item field="group_id" label="字段组 ID" required
          ><a-input v-model="form.group_id" :disabled="editing" placeholder="例如 market_quote"
        /></a-form-item>
        <a-form-item field="name" label="中文名" required><a-input v-model="form.name" /></a-form-item>
        <a-form-item class="span-2" field="description" label="描述"
          ><a-textarea v-model="form.description" :auto-size="{ minRows: 2, maxRows: 4 }"
        /></a-form-item>
        <a-form-item field="sort_order" label="同级顺序"><a-input-number v-model="form.sort_order" :min="0" /></a-form-item>
        <a-form-item field="status" label="启用状态"
          ><a-switch v-model="form.status" checked-value="active" unchecked-value="disabled"
        /></a-form-item>
      </div>
    </a-form>
  </a-modal>
</template>

<script setup lang="ts">
import { computed, reactive, watch } from "vue";
import { Message } from "@arco-design/web-vue";
import type { FieldGroup } from "@/api/storage/types";

const props = defineProps<{
  visible: boolean;
  group: FieldGroup | null;
  parentID: string;
  groups: FieldGroup[];
  spaceID: string;
  saving: boolean;
}>();

const emit = defineEmits<{ close: []; save: [group: FieldGroup] }>();
const editing = computed(() => Boolean(props.group?.group_id));
const roots = computed(() => props.groups.filter(item => !item.parent_group_id));
const form = reactive<FieldGroup>(emptyGroup());

function emptyGroup(): FieldGroup {
  return {
    space_id: props.spaceID,
    group_id: "",
    name: "",
    description: "",
    parent_group_id: props.parentID,
    sort_order: 0,
    status: "active"
  };
}

watch(
  () => props.visible,
  visible => {
    if (visible) Object.assign(form, emptyGroup(), props.group || {});
  }
);

function submit() {
  if (!form.group_id.trim() || !form.name.trim()) {
    Message.warning("请补全字段组 ID 和中文名");
    return false;
  }
  const parent = roots.value.find(item => item.group_id === form.parent_group_id);
  if (form.parent_group_id && !parent) {
    Message.error("父字段组必须是一级字段组");
    return false;
  }
  emit("save", { ...form, space_id: props.spaceID });
  return true;
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
</style>
