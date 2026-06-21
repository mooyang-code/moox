<template>
  <div class="metadata-page">
    <div class="page-head">
      <h2>数据源</h2>
      <a-space>
        <a-button type="primary" :disabled="!selectedSpaceId" @click="openCreate">
          <template #icon><icon-plus /></template>
          新增来源
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
      row-key="data_source_id"
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
        <a-table-column title="来源ID" data-index="data_source_id" :width="180" />
        <a-table-column title="名称" data-index="name" :width="180" />
        <a-table-column title="类型" data-index="kind" :width="120" />
        <a-table-column title="市场" data-index="market" :width="100" />
        <a-table-column title="时区" data-index="timezone" :width="150" />
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
        <a-form-item field="data_source_id" label="来源ID" required>
          <a-input v-model="form.data_source_id" :disabled="editing" placeholder="例如 binance" />
        </a-form-item>
        <a-form-item field="name" label="名称" required>
          <a-input v-model="form.name" placeholder="例如 Binance" />
        </a-form-item>
        <a-form-item field="kind" label="类型" required>
          <a-input v-model="form.kind" placeholder="例如 exchange、csv、api" />
        </a-form-item>
        <a-form-item field="market" label="市场">
          <a-input v-model="form.market" />
        </a-form-item>
        <a-form-item field="timezone" label="时区">
          <a-input v-model="form.timezone" placeholder="Asia/Shanghai" />
        </a-form-item>
        <a-form-item field="config_json" label="配置JSON">
          <a-textarea v-model="form.config_json" :auto-size="{ minRows: 4, maxRows: 8 }" />
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
import { createDataSource, listDataSources, updateDataSource } from '@/api/storage/metadata';
import type { DataSource } from '@/api/storage/types';
import { useSpaceStore } from '@/store/modules/space';
import { applyPageResult, defaultPagination, formatTime, jsonText, statusColor, statusOptions } from '@/views/data/shared/metadata-utils';

defineOptions({ name: 'DataSources' });

const spaceStore = useSpaceStore();
const selectedSpaceId = computed(() => spaceStore.selectedSpaceId);
const rows = ref<DataSource[]>([]);
const loading = ref(false);
const visible = ref(false);
const editing = ref(false);
const pagination = reactive(defaultPagination());

const form = reactive<DataSource>({
  space_id: '',
  data_source_id: '',
  name: '',
  kind: '',
  market: '',
  timezone: 'Asia/Shanghai',
  config_json: '{}',
  status: 'active',
});

const modalTitle = computed(() => (editing.value ? '编辑数据源' : '新增数据源'));

async function load() {
  if (!selectedSpaceId.value) {
    rows.value = [];
    return;
  }
  loading.value = true;
  try {
    const rsp = await listDataSources({
      space_id: selectedSpaceId.value,
      page: { page: pagination.current, size: pagination.pageSize },
    });
    rows.value = rsp.data_sources || [];
    applyPageResult(pagination, rsp.page_result);
  } finally {
    loading.value = false;
  }
}

function resetForm() {
  Object.assign(form, {
    space_id: selectedSpaceId.value,
    data_source_id: '',
    name: '',
    kind: '',
    market: '',
    timezone: 'Asia/Shanghai',
    config_json: '{}',
    status: 'active',
  });
}

function openCreate() {
  editing.value = false;
  resetForm();
  visible.value = true;
}

function openEdit(record: DataSource) {
  editing.value = true;
  Object.assign(form, {
    ...record,
    config_json: jsonText(record.config_json),
  });
  visible.value = true;
}

async function submit() {
  const spaceId = spaceStore.requireSpaceId();
  if (!form.data_source_id || !form.name || !form.kind) {
    Message.warning('请补全来源ID、名称和类型');
    return;
  }
  const payload = { ...form, space_id: spaceId, config_json: jsonText(form.config_json) };
  if (editing.value) await updateDataSource(payload);
  else await createDataSource(payload);
  Message.success('数据源已保存');
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
