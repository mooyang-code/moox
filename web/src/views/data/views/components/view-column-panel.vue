<template>
  <div class="panel">
    <div class="panel-toolbar">
      <a-space>
        <a-button type="primary" :disabled="!viewId" @click="openCreate">
          <template #icon><icon-plus /></template>
          新增结果列
        </a-button>
        <a-button :disabled="!viewId" @click="load">
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
        <a-table-column title="列名" data-index="column_name" :width="150" />
        <a-table-column title="来源类型" :width="130">
          <template #cell="{ record }">
            {{ optionLabel(viewColumnOriginOptions, record.origin_type) }}
          </template>
        </a-table-column>
        <a-table-column title="来源ID" data-index="origin_id" :width="200" />
        <a-table-column title="值类型" :width="110">
          <template #cell="{ record }">
            {{ optionLabel(fieldValueTypeOptions, record.value_type) }}
          </template>
        </a-table-column>
        <a-table-column title="上线时间" :width="180">
          <template #cell="{ record }">{{ formatTime(record.online_time) }}</template>
        </a-table-column>
        <a-table-column title="排序" data-index="sort_order" :width="80" />
        <a-table-column title="操作" :width="90" align="center" :fixed="'right'">
          <template #cell="{ record }">
            <a-button size="mini" type="text" @click="openEdit(record)">编辑</a-button>
          </template>
        </a-table-column>
      </template>
    </a-table>

    <a-modal v-model:visible="visible" width="720px" :title="modalTitle" @ok="submit">
      <a-form :model="form" auto-label-width>
        <a-form-item field="column_name" label="列名" required>
          <a-input v-model="form.column_name" :disabled="editing" placeholder="例如 close" />
        </a-form-item>
        <a-form-item field="origin_type" label="来源类型" required>
          <a-select v-model="form.origin_type">
            <a-option v-for="item in viewColumnOriginOptions" :key="item.value" :value="item.value">{{ item.label }}</a-option>
          </a-select>
        </a-form-item>
        <a-form-item field="origin_id" label="来源ID" required>
          <a-input v-model="form.origin_id" placeholder="例如 kline.close、subject_id 或表达式ID" />
        </a-form-item>
        <a-form-item field="value_type" label="值类型" required>
          <a-select v-model="form.value_type">
            <a-option v-for="item in fieldValueTypeOptions" :key="item.value" :value="item.value">{{ item.label }}</a-option>
          </a-select>
        </a-form-item>
        <a-form-item field="online_time" label="上线时间">
          <a-input v-model="form.online_time" placeholder="RFC3339，可留空" />
        </a-form-item>
        <a-form-item field="sort_order" label="排序">
          <a-input-number v-model="form.sort_order" :min="0" />
        </a-form-item>
      </a-form>
    </a-modal>
  </div>
</template>

<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue';
import { Message } from '@arco-design/web-vue';
import { listViewColumns, upsertViewColumn } from '@/api/storage/metadata';
import type { ViewColumn } from '@/api/storage/types';
import {
  applyPageResult,
  defaultPagination,
  fieldValueTypeOptions,
  formatTime,
  optionLabel,
  viewColumnOriginOptions,
} from '@/views/data/shared/metadata-utils';

defineOptions({ name: 'ViewColumnPanel' });

const props = defineProps<{
  spaceId: string;
  viewId: string;
}>();

const rows = ref<ViewColumn[]>([]);
const loading = ref(false);
const visible = ref(false);
const editing = ref(false);
const pagination = reactive(defaultPagination());

const form = reactive({
  column_name: '',
  origin_type: 'COLUMN_ORIGIN_TYPE_DATASET_COLUMN' as ViewColumn['origin_type'],
  origin_id: '',
  value_type: 'FIELD_VALUE_TYPE_STRING' as ViewColumn['value_type'],
  online_time: '',
  sort_order: 0,
});

const modalTitle = computed(() => (editing.value ? '编辑结果列' : '新增结果列'));

async function load() {
  if (!props.spaceId || !props.viewId) {
    rows.value = [];
    return;
  }
  loading.value = true;
  try {
    const rsp = await listViewColumns({
      space_id: props.spaceId,
      view_id: props.viewId,
      page: { page: pagination.current, size: pagination.pageSize },
    });
    rows.value = rsp.view_columns || [];
    applyPageResult(pagination, rsp.page_result);
  } finally {
    loading.value = false;
  }
}

function resetForm() {
  Object.assign(form, {
    column_name: '',
    origin_type: 'COLUMN_ORIGIN_TYPE_DATASET_COLUMN',
    origin_id: '',
    value_type: 'FIELD_VALUE_TYPE_STRING',
    online_time: '',
    sort_order: 0,
  });
}

function openCreate() {
  editing.value = false;
  resetForm();
  visible.value = true;
}

function openEdit(record: ViewColumn) {
  editing.value = true;
  Object.assign(form, {
    column_name: record.column_name,
    origin_type: record.origin_type || 'COLUMN_ORIGIN_TYPE_DATASET_COLUMN',
    origin_id: record.origin_id,
    value_type: record.value_type || 'FIELD_VALUE_TYPE_STRING',
    online_time: record.online_time || '',
    sort_order: record.sort_order || 0,
  });
  visible.value = true;
}

async function submit() {
  if (!props.spaceId || !props.viewId || !form.column_name || !form.origin_id) {
    Message.warning('请补全列名、来源ID和视图');
    return;
  }
  await upsertViewColumn({
    space_id: props.spaceId,
    view_id: props.viewId,
    column_name: form.column_name,
    origin_type: form.origin_type,
    origin_id: form.origin_id,
    value_type: form.value_type,
    online_time: form.online_time,
    sort_order: form.sort_order,
  });
  Message.success('结果列已保存');
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

watch(() => [props.spaceId, props.viewId], load, { immediate: true });
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
