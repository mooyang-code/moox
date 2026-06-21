<template>
  <div class="admin-page">
    <div class="page-head">
      <div>
        <h2>空间管理</h2>
        <span>当前空间：{{ spaceStore.selectedSpace?.name || '未选择' }}</span>
      </div>
      <a-space>
        <a-button type="primary" @click="openCreate">
          <template #icon><icon-plus /></template>
          创建空间
        </a-button>
        <a-button @click="load">
          <template #icon><icon-refresh /></template>
          刷新
        </a-button>
      </a-space>
    </div>

    <a-table
      row-key="space_id"
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
        <a-table-column title="空间ID" data-index="space_id" :width="180" />
        <a-table-column title="名称" data-index="name" :width="180" />
        <a-table-column title="负责人" data-index="owner" :width="120" />
        <a-table-column title="市场" data-index="market" :width="110" />
        <a-table-column title="时区" data-index="timezone" :width="150" />
        <a-table-column title="状态" :width="90">
          <template #cell="{ record }">
            <a-tag size="small" :color="statusColor(record.status)">{{ record.status }}</a-tag>
          </template>
        </a-table-column>
        <a-table-column title="更新时间" :width="180">
          <template #cell="{ record }">{{ formatTime(record.updated_at) }}</template>
        </a-table-column>
        <a-table-column title="操作" :width="180" align="center" :fixed="'right'">
          <template #cell="{ record }">
            <a-space>
              <a-button size="mini" type="text" @click="spaceStore.setSelectedSpace(record.space_id)">设为当前</a-button>
              <a-button size="mini" type="text" @click="openEdit(record)">编辑</a-button>
            </a-space>
          </template>
        </a-table-column>
      </template>
    </a-table>

    <a-modal v-model:visible="visible" width="720px" :title="modalTitle" @ok="submit">
      <a-form :model="form" auto-label-width>
        <a-form-item field="space_id" label="空间ID" required>
          <a-input v-model="form.space_id" :disabled="editing" placeholder="例如 cn_stock" />
        </a-form-item>
        <a-form-item field="name" label="名称" required>
          <a-input v-model="form.name" placeholder="例如 A股交易空间" />
        </a-form-item>
        <a-form-item field="description" label="描述">
          <a-textarea v-model="form.description" :auto-size="{ minRows: 3, maxRows: 5 }" />
        </a-form-item>
        <a-form-item field="owner" label="负责人">
          <a-input v-model="form.owner" />
        </a-form-item>
        <a-form-item field="market" label="市场">
          <a-input v-model="form.market" placeholder="例如 CN、US、CRYPTO" />
        </a-form-item>
        <a-form-item field="timezone" label="时区">
          <a-input v-model="form.timezone" placeholder="例如 Asia/Shanghai" />
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
import { computed, onMounted, reactive, ref } from 'vue';
import { Message } from '@arco-design/web-vue';
import { createSpace, listSpaces, updateSpace } from '@/api/control/spaces';
import type { Space } from '@/api/control/types';
import { useSpaceStore } from '@/store/modules/space';
import { applyPageResult, defaultPagination, formatTime, statusColor, statusOptions } from '@/views/data/shared/metadata-utils';

defineOptions({ name: 'SettingsSpaces' });

const spaceStore = useSpaceStore();
const rows = ref<Space[]>([]);
const loading = ref(false);
const visible = ref(false);
const editing = ref(false);
const pagination = reactive(defaultPagination());

const form = reactive<Space>({
  space_id: '',
  name: '',
  description: '',
  owner: '',
  market: '',
  timezone: 'Asia/Shanghai',
  status: 'active',
});

const modalTitle = computed(() => (editing.value ? '编辑空间' : '创建空间'));

async function load() {
  loading.value = true;
  try {
    const rsp = await listSpaces({ page: { page: pagination.current, size: pagination.pageSize } });
    rows.value = rsp.spaces || [];
    applyPageResult(pagination, rsp.page_result);
    await spaceStore.loadSpaces();
  } finally {
    loading.value = false;
  }
}

function resetForm() {
  Object.assign(form, {
    space_id: '',
    name: '',
    description: '',
    owner: '',
    market: '',
    timezone: 'Asia/Shanghai',
    status: 'active',
  });
}

function openCreate() {
  editing.value = false;
  resetForm();
  visible.value = true;
}

function openEdit(record: Space) {
  editing.value = true;
  Object.assign(form, {
    space_id: record.space_id,
    name: record.name,
    description: record.description || '',
    owner: record.owner || '',
    market: record.market || '',
    timezone: record.timezone || 'Asia/Shanghai',
    status: record.status || 'active',
  });
  visible.value = true;
}

async function submit() {
  if (!form.space_id || !form.name) {
    Message.warning('请填写空间ID和名称');
    return;
  }
  if (editing.value) {
    await updateSpace({ ...form });
  } else {
    await createSpace({ ...form });
  }
  Message.success('空间已保存');
  visible.value = false;
  await load();
  spaceStore.setSelectedSpace(form.space_id);
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

onMounted(load);
</script>

<style scoped>
.admin-page {
  padding: 20px;
}

.page-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 16px;
}

.page-head h2 {
  margin: 0 0 4px;
  font-size: 20px;
  font-weight: 600;
}

.page-head span {
  color: var(--color-text-3);
}
</style>
