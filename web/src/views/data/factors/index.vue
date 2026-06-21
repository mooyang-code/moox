<template>
  <div class="metadata-page">
    <div class="page-head">
      <h2>因子管理</h2>
      <a-space>
        <a-button type="primary" :disabled="!selectedSpaceId" @click="openCreate">
          <template #icon><icon-plus /></template>
          新增因子
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
      row-key="factor_id"
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
        <a-table-column title="因子ID" data-index="factor_id" :width="160" />
        <a-table-column title="名称" data-index="name" :width="160" />
        <a-table-column title="算法" data-index="algorithm" :width="150" />
        <a-table-column title="值类型" :width="120">
          <template #cell="{ record }">{{ optionLabel(fieldValueTypeOptions, record.value_type) }}</template>
        </a-table-column>
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
        <a-form-item field="factor_id" label="因子ID" required>
          <a-input v-model="form.factor_id" :disabled="editing" placeholder="例如 ma20_close" />
        </a-form-item>
        <a-form-item field="name" label="名称" required>
          <a-input v-model="form.name" />
        </a-form-item>
        <a-form-item field="description" label="描述">
          <a-textarea v-model="form.description" :auto-size="{ minRows: 3, maxRows: 5 }" />
        </a-form-item>
        <a-form-item field="algorithm" label="算法" required>
          <a-input v-model="form.algorithm" placeholder="例如 MA、EMA、RSI" />
        </a-form-item>
        <a-form-item field="params_json" label="参数JSON">
          <a-textarea v-model="form.params_json" :auto-size="{ minRows: 4, maxRows: 8 }" />
        </a-form-item>
        <a-form-item field="value_type" label="值类型" required>
          <a-select v-model="form.value_type">
            <a-option v-for="item in fieldValueTypeOptions" :key="item.value" :value="item.value">{{ item.label }}</a-option>
          </a-select>
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
import { createFactor, listFactors, updateFactor } from '@/api/storage/metadata';
import type { Factor } from '@/api/storage/types';
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

defineOptions({ name: 'DataFactors' });

const spaceStore = useSpaceStore();
const selectedSpaceId = computed(() => spaceStore.selectedSpaceId);
const rows = ref<Factor[]>([]);
const loading = ref(false);
const visible = ref(false);
const editing = ref(false);
const pagination = reactive(defaultPagination());

const form = reactive<Factor>({
  space_id: '',
  factor_id: '',
  name: '',
  description: '',
  algorithm: '',
  params_json: '{}',
  value_type: 'FIELD_VALUE_TYPE_DOUBLE',
  status: 'active',
});

const modalTitle = computed(() => (editing.value ? '编辑因子' : '新增因子'));

async function load() {
  if (!selectedSpaceId.value) {
    rows.value = [];
    return;
  }
  loading.value = true;
  try {
    const rsp = await listFactors({
      space_id: selectedSpaceId.value,
      page: { page: pagination.current, size: pagination.pageSize },
    });
    rows.value = rsp.factors || [];
    applyPageResult(pagination, rsp.page_result);
  } finally {
    loading.value = false;
  }
}

function resetForm() {
  Object.assign(form, {
    space_id: selectedSpaceId.value,
    factor_id: '',
    name: '',
    description: '',
    algorithm: '',
    params_json: '{}',
    value_type: 'FIELD_VALUE_TYPE_DOUBLE',
    status: 'active',
  });
}

function openCreate() {
  editing.value = false;
  resetForm();
  visible.value = true;
}

function openEdit(record: Factor) {
  editing.value = true;
  Object.assign(form, {
    ...record,
    params_json: jsonText(record.params_json),
  });
  visible.value = true;
}

async function submit() {
  const spaceId = spaceStore.requireSpaceId();
  if (!form.factor_id || !form.name || !form.algorithm || !form.value_type) {
    Message.warning('请补全因子ID、名称、算法和值类型');
    return;
  }
  const payload = { ...form, space_id: spaceId, params_json: jsonText(form.params_json) };
  if (editing.value) await updateFactor(payload);
  else await createFactor(payload);
  Message.success('因子已保存');
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
