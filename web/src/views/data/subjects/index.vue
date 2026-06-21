<template>
  <div class="metadata-page">
    <div class="page-head">
      <h2>数据对象</h2>
      <a-space>
        <a-button type="primary" :disabled="!selectedSpaceId" @click="openCreate">
          <template #icon><icon-plus /></template>
          新增对象
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
        <a-table-column title="对象类型" data-index="subject_type" :width="130" />
        <a-table-column title="名称" data-index="name" :width="180" />
        <a-table-column title="市场" data-index="market" :width="100" />
        <a-table-column title="币种" data-index="currency" :width="100" />
        <a-table-column title="时区" data-index="timezone" :width="150" />
        <a-table-column title="状态" :width="90">
          <template #cell="{ record }">
            <a-tag size="small" :color="statusColor(record.status)">{{ record.status }}</a-tag>
          </template>
        </a-table-column>
        <a-table-column title="更新时间" :width="180">
          <template #cell="{ record }">{{ formatTime(record.updated_at) }}</template>
        </a-table-column>
        <a-table-column title="操作" :width="170" align="center" :fixed="'right'">
          <template #cell="{ record }">
            <a-space>
              <a-button size="mini" type="text" @click="openSymbols(record)">符号</a-button>
              <a-button size="mini" type="text" @click="openEdit(record)">编辑</a-button>
            </a-space>
          </template>
        </a-table-column>
      </template>
    </a-table>

    <a-modal v-model:visible="visible" width="720px" :title="modalTitle" @ok="submit">
      <a-form :model="form" auto-label-width>
        <a-form-item field="subject_id" label="对象ID" required>
          <a-input v-model="form.subject_id" :disabled="editing" placeholder="例如 BTC-USDT 或 000001.SZ" />
        </a-form-item>
        <a-form-item field="subject_type" label="对象类型" required>
          <a-input v-model="form.subject_type" placeholder="例如 stock、crypto_pair、index" />
        </a-form-item>
        <a-form-item field="name" label="名称" required>
          <a-input v-model="form.name" />
        </a-form-item>
        <a-form-item field="market" label="市场">
          <a-input v-model="form.market" />
        </a-form-item>
        <a-form-item field="currency" label="币种">
          <a-input v-model="form.currency" />
        </a-form-item>
        <a-form-item field="timezone" label="时区">
          <a-input v-model="form.timezone" />
        </a-form-item>
        <a-form-item field="status" label="状态">
          <a-select v-model="form.status">
            <a-option v-for="item in statusOptions" :key="item.value" :value="item.value">{{ item.label }}</a-option>
          </a-select>
        </a-form-item>
      </a-form>
    </a-modal>

    <a-drawer v-model:visible="symbolDrawerVisible" width="760px" :footer="false">
      <template #title>外部符号：{{ activeSubject?.subject_id }}</template>
      <div class="drawer-toolbar">
        <a-space>
          <a-button type="primary" @click="openSymbolCreate">
            <template #icon><icon-plus /></template>
            新增符号
          </a-button>
          <a-button @click="loadSymbols">
            <template #icon><icon-refresh /></template>
            刷新
          </a-button>
        </a-space>
      </div>
      <a-table
        row-key="external_symbol"
        size="small"
        :bordered="{ cell: true }"
        :loading="symbolLoading"
        :data="symbolRows"
        :pagination="false"
      >
        <template #columns>
          <a-table-column title="数据来源ID" data-index="data_source_id" />
          <a-table-column title="外部符号" data-index="external_symbol" />
          <a-table-column title="状态" :width="90">
            <template #cell="{ record }">
              <a-tag size="small" :color="statusColor(record.status)">{{ record.status }}</a-tag>
            </template>
          </a-table-column>
          <a-table-column title="操作" :width="90" align="center">
            <template #cell="{ record }">
              <a-button size="mini" type="text" @click="openSymbolEdit(record)">编辑</a-button>
            </template>
          </a-table-column>
        </template>
      </a-table>
    </a-drawer>

    <a-modal v-model:visible="symbolVisible" width="560px" title="外部符号" @ok="submitSymbol">
      <a-form :model="symbolForm" auto-label-width>
        <a-form-item field="data_source_id" label="数据来源ID" required>
          <a-input v-model="symbolForm.data_source_id" :disabled="symbolEditing" placeholder="例如 binance" />
        </a-form-item>
        <a-form-item field="external_symbol" label="外部符号" required>
          <a-input v-model="symbolForm.external_symbol" :disabled="symbolEditing" placeholder="例如 BTCUSDT" />
        </a-form-item>
        <a-form-item field="status" label="状态">
          <a-select v-model="symbolForm.status">
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
import { listSubjectSymbols, listSubjects, upsertSubject, upsertSubjectSymbol } from '@/api/storage/metadata';
import type { Subject, SubjectSymbol } from '@/api/storage/types';
import { useSpaceStore } from '@/store/modules/space';
import { applyPageResult, defaultPagination, formatTime, statusColor, statusOptions } from '@/views/data/shared/metadata-utils';

defineOptions({ name: 'DataSubjects' });

const spaceStore = useSpaceStore();
const selectedSpaceId = computed(() => spaceStore.selectedSpaceId);
const rows = ref<Subject[]>([]);
const loading = ref(false);
const visible = ref(false);
const editing = ref(false);
const pagination = reactive(defaultPagination());

const form = reactive<Subject>({
  space_id: '',
  subject_id: '',
  subject_type: '',
  name: '',
  market: '',
  currency: '',
  timezone: 'Asia/Shanghai',
  status: 'active',
});

const modalTitle = computed(() => (editing.value ? '编辑数据对象' : '新增数据对象'));

const symbolDrawerVisible = ref(false);
const symbolVisible = ref(false);
const symbolEditing = ref(false);
const symbolLoading = ref(false);
const activeSubject = ref<Subject>();
const symbolRows = ref<SubjectSymbol[]>([]);
const symbolForm = reactive<SubjectSymbol>({
  space_id: '',
  subject_id: '',
  data_source_id: '',
  external_symbol: '',
  status: 'active',
});

async function load() {
  if (!selectedSpaceId.value) {
    rows.value = [];
    return;
  }
  loading.value = true;
  try {
    const rsp = await listSubjects({
      space_id: selectedSpaceId.value,
      page: { page: pagination.current, size: pagination.pageSize },
    });
    rows.value = rsp.subjects || [];
    applyPageResult(pagination, rsp.page_result);
  } finally {
    loading.value = false;
  }
}

function resetForm() {
  Object.assign(form, {
    space_id: selectedSpaceId.value,
    subject_id: '',
    subject_type: '',
    name: '',
    market: '',
    currency: '',
    timezone: 'Asia/Shanghai',
    status: 'active',
  });
}

function openCreate() {
  editing.value = false;
  resetForm();
  visible.value = true;
}

function openEdit(record: Subject) {
  editing.value = true;
  Object.assign(form, {
    ...record,
    timezone: record.timezone || 'Asia/Shanghai',
  });
  visible.value = true;
}

async function submit() {
  const spaceId = spaceStore.requireSpaceId();
  if (!form.subject_id || !form.subject_type || !form.name) {
    Message.warning('请补全对象ID、对象类型和名称');
    return;
  }
  await upsertSubject({ ...form, space_id: spaceId });
  Message.success('数据对象已保存');
  visible.value = false;
  await load();
}

function openSymbols(record: Subject) {
  activeSubject.value = record;
  symbolDrawerVisible.value = true;
  loadSymbols();
}

async function loadSymbols() {
  if (!selectedSpaceId.value || !activeSubject.value) return;
  symbolLoading.value = true;
  try {
    const rsp = await listSubjectSymbols({
      space_id: selectedSpaceId.value,
      subject_id: activeSubject.value.subject_id,
      page: { page: 1, size: 200 },
    });
    symbolRows.value = rsp.subject_symbols || [];
  } finally {
    symbolLoading.value = false;
  }
}

function resetSymbolForm() {
  Object.assign(symbolForm, {
    space_id: selectedSpaceId.value,
    subject_id: activeSubject.value?.subject_id || '',
    data_source_id: '',
    external_symbol: '',
    status: 'active',
  });
}

function openSymbolCreate() {
  symbolEditing.value = false;
  resetSymbolForm();
  symbolVisible.value = true;
}

function openSymbolEdit(record: SubjectSymbol) {
  symbolEditing.value = true;
  Object.assign(symbolForm, record);
  symbolVisible.value = true;
}

async function submitSymbol() {
  const spaceId = spaceStore.requireSpaceId();
  const subjectId = activeSubject.value?.subject_id;
  if (!subjectId || !symbolForm.data_source_id || !symbolForm.external_symbol) {
    Message.warning('请补全数据来源ID和外部符号');
    return;
  }
  await upsertSubjectSymbol({ ...symbolForm, space_id: spaceId, subject_id: subjectId });
  Message.success('外部符号已保存');
  symbolVisible.value = false;
  await loadSymbols();
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

.page-head,
.drawer-toolbar {
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
