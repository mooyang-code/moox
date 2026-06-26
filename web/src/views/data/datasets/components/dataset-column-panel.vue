<template>
  <div class="panel">
    <div class="panel-toolbar">
      <a-space>
        <a-button type="primary" :disabled="!datasetId" @click="openCreate">
          <template #icon><icon-plus /></template>
          新增列
        </a-button>
        <a-button :disabled="!datasetId" @click="load">
          <template #icon><icon-refresh /></template>
          刷新
        </a-button>
      </a-space>
    </div>

    <a-table
      row-key="column_name"
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
        <a-table-column title="中文名" :width="120">
          <template #cell="{ record }">{{ record.attributes?.display_name || '-' }}</template>
        </a-table-column>
        <a-table-column title="技术列名" data-index="column_name" :width="150" />
        <a-table-column title="来源类型" :width="120">
          <template #cell="{ record }">
            {{ optionLabel(datasetColumnOriginOptions, record.origin_type) }}
          </template>
        </a-table-column>
        <a-table-column title="来源ID" data-index="origin_id" :width="180" />
        <a-table-column title="值类型" :width="110">
          <template #cell="{ record }">
            {{ optionLabel(fieldValueTypeOptions, record.value_type) }}
          </template>
        </a-table-column>
        <a-table-column title="必填" :width="80" align="center">
          <template #cell="{ record }">
            <a-tag size="small" :color="record.required ? 'red' : 'gray'">{{ record.required ? '是' : '否' }}</a-tag>
          </template>
        </a-table-column>
        <a-table-column title="唯一" :width="80" align="center">
          <template #cell="{ record }">
            <a-tag size="small" :color="record.is_unique ? 'blue' : 'gray'">{{ record.is_unique ? '是' : '否' }}</a-tag>
          </template>
        </a-table-column>
        <a-table-column title="别名" :width="180">
          <template #cell="{ record }">
            {{ joinList(record.aliases) || '-' }}
          </template>
        </a-table-column>
        <a-table-column title="状态" :width="90">
          <template #cell="{ record }">
            <a-tag size="small" :color="statusColor(record.status)">{{ record.status }}</a-tag>
          </template>
        </a-table-column>
        <a-table-column title="操作" :width="90" align="center" :fixed="'right'">
          <template #cell="{ record }">
            <a-button size="mini" type="text" @click="openEdit(record)">编辑</a-button>
          </template>
        </a-table-column>
      </template>
    </a-table>

    <a-modal v-model:visible="visible" width="720px" :title="modalTitle" @ok="submit">
      <a-form :model="form" auto-label-width>
        <a-form-item field="display_name" label="中文名" required>
          <a-input v-model="form.display_name" :max-length="10" show-word-limit placeholder="例如 收盘价" />
        </a-form-item>
        <a-form-item field="column_name" label="列名" required>
          <a-input v-model="form.column_name" :disabled="editing" placeholder="例如 close" />
        </a-form-item>
        <a-form-item field="origin_type" label="来源类型" required>
          <a-select v-model="form.origin_type">
            <a-option v-for="item in datasetColumnOriginOptions" :key="item.value" :value="item.value">
              {{ item.label }}
            </a-option>
          </a-select>
        </a-form-item>
        <a-form-item field="origin_id" label="来源ID" required>
          <a-input v-model="form.origin_id" placeholder="字段ID、因子ID或系统列名" />
        </a-form-item>
        <a-form-item field="value_type" label="值类型" required>
          <a-select v-model="form.value_type">
            <a-option v-for="item in fieldValueTypeOptions" :key="item.value" :value="item.value">
              {{ item.label }}
            </a-option>
          </a-select>
        </a-form-item>
        <a-form-item label="约束">
          <a-space>
            <a-checkbox v-model="form.required">必填</a-checkbox>
            <a-checkbox v-model="form.is_unique">唯一</a-checkbox>
          </a-space>
        </a-form-item>
        <a-form-item field="aliasesText" label="别名">
          <a-input v-model="form.aliasesText" placeholder="多个别名用逗号分隔" />
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
import { computed, reactive, ref, watch } from 'vue';
import { Message } from '@arco-design/web-vue';
import { listDatasetColumns, upsertDatasetColumn } from '@/api/storage/metadata';
import type { DatasetColumn } from '@/api/storage/types';
import {
  applyPageResult,
  datasetColumnOriginOptions,
  defaultPagination,
  fieldValueTypeOptions,
  joinList,
  optionLabel,
  splitList,
  statusColor,
  statusOptions,
  validateChineseDisplayName,
} from '@/views/data/shared/metadata-utils';

defineOptions({ name: 'DatasetColumnPanel' });

const props = defineProps<{
  spaceId: string;
  datasetId: string;
}>();

const rows = ref<DatasetColumn[]>([]);
const loading = ref(false);
const visible = ref(false);
const editing = ref(false);
const pagination = reactive(defaultPagination());

const form = reactive({
  display_name: '',
  column_name: '',
  origin_type: 'DATASET_COLUMN_ORIGIN_TYPE_FIELD' as DatasetColumn['origin_type'],
  origin_id: '',
  value_type: 'FIELD_VALUE_TYPE_STRING' as DatasetColumn['value_type'],
  required: false,
  is_unique: false,
  aliasesText: '',
  status: 'active',
});

const modalTitle = computed(() => (editing.value ? '编辑数据集列' : '新增数据集列'));

async function load() {
  if (!props.spaceId || !props.datasetId) {
    rows.value = [];
    return;
  }
  loading.value = true;
  try {
    const rsp = await listDatasetColumns({
      space_id: props.spaceId,
      dataset_id: props.datasetId,
      page: { page: pagination.current, size: pagination.pageSize },
    });
    rows.value = rsp.columns || [];
    applyPageResult(pagination, rsp.page_result);
  } finally {
    loading.value = false;
  }
}

function resetForm() {
  Object.assign(form, {
    column_name: '',
    display_name: '',
    origin_type: 'DATASET_COLUMN_ORIGIN_TYPE_FIELD',
    origin_id: '',
    value_type: 'FIELD_VALUE_TYPE_STRING',
    required: false,
    is_unique: false,
    aliasesText: '',
    status: 'active',
  });
}

function openCreate() {
  editing.value = false;
  resetForm();
  visible.value = true;
}

function openEdit(record: DatasetColumn) {
  editing.value = true;
  Object.assign(form, {
    column_name: record.column_name,
    display_name: record.attributes?.display_name || '',
    origin_type: record.origin_type || 'DATASET_COLUMN_ORIGIN_TYPE_FIELD',
    origin_id: record.origin_id,
    value_type: record.value_type || 'FIELD_VALUE_TYPE_STRING',
    required: !!record.required,
    is_unique: !!record.is_unique,
    aliasesText: joinList(record.aliases),
    status: record.status || 'active',
  });
  visible.value = true;
}

async function submit() {
  if (!props.spaceId || !props.datasetId || !form.column_name || !form.origin_id || !form.display_name) {
    Message.warning('请补全中文名、列名、来源ID和数据集');
    return;
  }
  const nameError = validateChineseDisplayName(form.display_name);
  if (nameError) {
    Message.warning(nameError);
    return;
  }
  await upsertDatasetColumn({
    space_id: props.spaceId,
    dataset_id: props.datasetId,
    column_name: form.column_name,
    origin_type: form.origin_type,
    origin_id: form.origin_id,
    value_type: form.value_type,
    required: form.required,
    is_unique: form.is_unique,
    aliases: splitList(form.aliasesText),
    status: form.status,
    attributes: { display_name: form.display_name },
  });
  Message.success('数据集列已保存');
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

watch(() => [props.spaceId, props.datasetId], load, { immediate: true });
</script>

<style scoped>
.panel {
  display: flex;
  flex-direction: column;
  gap: 12px;
}

.panel-toolbar {
  display: flex;
  justify-content: flex-end;
}
</style>
