<template>
  <div class="metadata-page">
    <div class="page-head">
      <h2>字段管理</h2>
      <a-space>
        <a-button type="primary" :disabled="!selectedSpaceId" @click="openCreate">
          <template #icon><icon-plus /></template>
          新增字段
        </a-button>
        <a-button :disabled="!selectedSpaceId" @click="load">
          <template #icon><icon-refresh /></template>
          刷新
        </a-button>
      </a-space>
    </div>

    <a-alert v-if="!selectedSpaceId" type="warning" show-icon>请先在顶部选择空间</a-alert>

    <a-table
      v-else
      row-key="field_id"
      size="small"
      :bordered="{ cell: true }"
      :loading="loading"
      :data="rows"
      :pagination="pagination"
      :scroll="{ x: 'max-content' }"
      @page-change="onPageChange"
      @page-size-change="onPageSizeChange"
    >
      <template #columns>
        <a-table-column title="字段ID" data-index="field_id" :width="160" />
        <a-table-column title="名称" data-index="name" :width="160" />
        <a-table-column title="值类型" :width="120">
          <template #cell="{ record }">{{ optionLabel(fieldValueTypeOptions, record.value_type) }}</template>
        </a-table-column>
        <a-table-column title="单位" data-index="unit" :width="100" />
        <a-table-column title="状态" :width="90">
          <template #cell="{ record }">
            <a-tag size="small" :color="statusColor(record.status)">{{ record.status }}</a-tag>
          </template>
        </a-table-column>
        <a-table-column title="更新时间" :width="180">
          <template #cell="{ record }">{{ formatTime(record.updated_at) }}</template>
        </a-table-column>
        <a-table-column title="操作" :width="90" align="center" :fixed="'right'">
          <template #cell="{ record }">
            <a-button size="mini" type="text" @click="openEdit(record)">编辑</a-button>
          </template>
        </a-table-column>
      </template>
    </a-table>

    <a-modal v-model:visible="visible" width="760px" :title="modalTitle" @ok="submit">
      <a-form :model="form" auto-label-width>
        <a-form-item field="field_id" label="字段ID" required>
          <a-input v-model="form.field_id" :disabled="editing" placeholder="例如 close" />
        </a-form-item>
        <a-form-item field="name" label="名称" required>
          <a-input v-model="form.name" />
        </a-form-item>
        <a-form-item field="description" label="描述">
          <a-textarea v-model="form.description" :auto-size="{ minRows: 3, maxRows: 5 }" />
        </a-form-item>
        <a-form-item field="value_type" label="值类型" required>
          <a-select v-model="form.value_type">
            <a-option v-for="item in fieldValueTypeOptions" :key="item.value" :value="item.value">{{ item.label }}</a-option>
          </a-select>
        </a-form-item>
        <a-form-item field="unit" label="单位">
          <a-input v-model="form.unit" />
        </a-form-item>
        <a-form-item field="validation_rule_json" label="校验规则JSON">
          <a-textarea v-model="form.validation_rule_json" :auto-size="{ minRows: 3, maxRows: 6 }" />
        </a-form-item>
        <a-form-item field="write_example" label="写入示例">
          <a-textarea v-model="form.write_example" :auto-size="{ minRows: 3, maxRows: 6 }" />
        </a-form-item>
        <a-form-item field="status" label="状态">
          <a-select v-model="form.status">
            <a-option v-for="item in statusOptions" :key="item.value" :value="item.value">{{ item.label }}</a-option>
          </a-select>
        </a-form-item>
      </a-form>
    </a-modal>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref, watch } from 'vue';
import { Message } from '@arco-design/web-vue';
import { createField, listFields, updateField } from '@/api/storage/metadata';
import type { Field } from '@/api/storage/types';
import { useSpaceStore } from '@/store/modules/space';
import {
  applyPageResult,
  defaultPagination,
  fieldValueTypeOptions,
  formatTime,
  jsonText,
  optionLabel,
  statusColor,
  statusOptions,
} from '@/views/data/shared/metadata-utils';

defineOptions({ name: 'DataFields' });

const spaceStore = useSpaceStore();
const selectedSpaceId = computed(() => spaceStore.selectedSpaceId);
const rows = ref<Field[]>([]);
const loading = ref(false);
const visible = ref(false);
const editing = ref(false);
const pagination = reactive(defaultPagination());

const form = reactive<Field>({
  space_id: '',
  field_id: '',
  name: '',
  description: '',
  value_type: 'FIELD_VALUE_TYPE_STRING',
  unit: '',
  validation_rule_json: '{}',
  write_example: '',
  status: 'active',
});

const modalTitle = computed(() => (editing.value ? '编辑字段' : '新增字段'));

async function load() {
  if (!selectedSpaceId.value) {
    rows.value = [];
    return;
  }
  loading.value = true;
  try {
    const rsp = await listFields({
      space_id: selectedSpaceId.value,
      page: { page: pagination.current, size: pagination.pageSize },
    });
    rows.value = rsp.fields || [];
    applyPageResult(pagination, rsp.page_result);
  } finally {
    loading.value = false;
  }
}

function resetForm() {
  Object.assign(form, {
    space_id: selectedSpaceId.value,
    field_id: '',
    name: '',
    description: '',
    value_type: 'FIELD_VALUE_TYPE_STRING',
    unit: '',
    validation_rule_json: '{}',
    write_example: '',
    status: 'active',
  });
}

function openCreate() {
  editing.value = false;
  resetForm();
  visible.value = true;
}

function openEdit(record: Field) {
  editing.value = true;
  Object.assign(form, {
    ...record,
    validation_rule_json: jsonText(record.validation_rule_json),
  });
  visible.value = true;
}

async function submit() {
  const spaceId = spaceStore.requireSpaceId();
  if (!form.field_id || !form.name || !form.value_type) {
    Message.warning('请补全字段ID、名称和值类型');
    return;
  }
  const payload = { ...form, space_id: spaceId, validation_rule_json: jsonText(form.validation_rule_json) };
  if (editing.value) await updateField(payload);
  else await createField(payload);
  Message.success('字段已保存');
  visible.value = false;
  await load();
}

function onPageChange(page: number) {
  pagination.current = page;
  load();
}

function onPageSizeChange(pageSize: number) {
  pagination.current = 1;
  pagination.pageSize = pageSize;
  load();
}

watch(selectedSpaceId, () => {
  pagination.current = 1;
  load();
});
onMounted(load);
</script>

<style scoped>
.metadata-page {
  padding: 20px;
}

.page-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 16px;
}

.page-head h2 {
  margin: 0;
  font-size: 20px;
  font-weight: 600;
}
</style>
