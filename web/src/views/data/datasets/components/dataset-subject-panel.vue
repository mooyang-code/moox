<template>
  <div class="panel">
    <div class="panel-toolbar">
      <a-space>
        <a-button type="primary" :disabled="!datasetId" @click="openCreate">
          <template #icon><icon-plus /></template>
          绑定对象
        </a-button>
        <a-button :disabled="!datasetId" @click="load">
          <template #icon><icon-refresh /></template>
          刷新
        </a-button>
      </a-space>
    </div>

    <a-table
      row-key="subject_id"
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
        <a-table-column title="对象ID" data-index="subject_id" :width="180" />
        <a-table-column title="角色" data-index="subject_role" :width="120" />
        <a-table-column title="生效开始" :width="180">
          <template #cell="{ record }">{{ formatTime(record.effective_start_time) }}</template>
        </a-table-column>
        <a-table-column title="生效结束" :width="180">
          <template #cell="{ record }">{{ formatTime(record.effective_end_time) }}</template>
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

    <a-modal v-model:visible="visible" width="640px" :title="modalTitle" @ok="submit">
      <a-form :model="form" auto-label-width>
        <a-form-item field="subject_id" label="对象ID" required>
          <a-input v-model="form.subject_id" :disabled="editing" placeholder="例如 BTC-USDT" />
        </a-form-item>
        <a-form-item field="subject_role" label="对象角色">
          <a-input v-model="form.subject_role" placeholder="默认 primary" />
        </a-form-item>
        <a-form-item field="effective_start_time" label="生效开始">
          <a-input v-model="form.effective_start_time" placeholder="RFC3339，例如 2026-01-01T00:00:00Z" />
        </a-form-item>
        <a-form-item field="effective_end_time" label="生效结束">
          <a-input v-model="form.effective_end_time" placeholder="RFC3339，可留空" />
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
import { bindDatasetSubject, listDatasetSubjects } from '@/api/storage/metadata';
import type { DatasetSubject } from '@/api/storage/types';
import { applyPageResult, defaultPagination, formatTime, statusColor, statusOptions } from '@/views/data/shared/metadata-utils';

defineOptions({ name: 'DatasetSubjectPanel' });

const props = defineProps<{
  spaceId: string;
  datasetId: string;
}>();

const rows = ref<DatasetSubject[]>([]);
const loading = ref(false);
const visible = ref(false);
const editing = ref(false);
const pagination = reactive(defaultPagination());

const form = reactive({
  subject_id: '',
  subject_role: 'primary',
  effective_start_time: '',
  effective_end_time: '',
  status: 'active',
});

const modalTitle = computed(() => (editing.value ? '编辑对象绑定' : '绑定对象'));

async function load() {
  if (!props.spaceId || !props.datasetId) {
    rows.value = [];
    return;
  }
  loading.value = true;
  try {
    const rsp = await listDatasetSubjects({
      space_id: props.spaceId,
      dataset_id: props.datasetId,
      page: { page: pagination.current, size: pagination.pageSize },
    });
    rows.value = rsp.dataset_subjects || [];
    applyPageResult(pagination, rsp.page_result);
  } finally {
    loading.value = false;
  }
}

function resetForm() {
  Object.assign(form, {
    subject_id: '',
    subject_role: 'primary',
    effective_start_time: '',
    effective_end_time: '',
    status: 'active',
  });
}

function openCreate() {
  editing.value = false;
  resetForm();
  visible.value = true;
}

function openEdit(record: DatasetSubject) {
  editing.value = true;
  Object.assign(form, {
    subject_id: record.subject_id,
    subject_role: record.subject_role || 'primary',
    effective_start_time: record.effective_start_time || '',
    effective_end_time: record.effective_end_time || '',
    status: record.status || 'active',
  });
  visible.value = true;
}

async function submit() {
  if (!props.spaceId || !props.datasetId || !form.subject_id) {
    Message.warning('请补全对象ID和数据集');
    return;
  }
  await bindDatasetSubject({
    space_id: props.spaceId,
    dataset_id: props.datasetId,
    subject_id: form.subject_id,
    subject_role: form.subject_role || 'primary',
    effective_start_time: form.effective_start_time,
    effective_end_time: form.effective_end_time,
    status: form.status,
  });
  Message.success('对象绑定已保存');
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
