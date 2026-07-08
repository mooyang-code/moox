<template>
  <div class="factor-page">
    <div class="page-head">
      <div>
        <h2>因子绑定</h2>
        <span>把启用的因子绑定到 K 线数据集、频率和标的范围。</span>
      </div>
      <a-space>
        <a-button type="primary" :disabled="!selectedSpaceId" @click="openCreate">
          <template #icon><icon-plus /></template>
          新增绑定
        </a-button>
        <a-button :disabled="!selectedSpaceId" @click="load">
          <template #icon><icon-refresh /></template>
          刷新
        </a-button>
      </a-space>
    </div>

    <a-alert v-if="!selectedSpaceId" class="top-alert" type="warning" show-icon>请先在顶部选择空间</a-alert>

    <template v-else>
      <a-space class="filters" wrap>
        <a-input v-model="filters.source_dataset" allow-clear placeholder="源数据集" @press-enter="reloadFirstPage" />
        <a-input v-model="filters.freq" allow-clear placeholder="频率" style="width: 120px" @press-enter="reloadFirstPage" />
        <a-select v-model="filters.status" allow-clear placeholder="状态" style="width: 130px" @change="reloadFirstPage">
          <a-option value="enabled">enabled</a-option>
          <a-option value="disabled">disabled</a-option>
        </a-select>
        <a-button @click="reloadFirstPage">查询</a-button>
      </a-space>

      <a-table
        row-key="binding_id"
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
          <a-table-column title="因子" data-index="factor_id" :width="130" />
          <a-table-column title="源数据集" data-index="source_dataset" :width="180" />
          <a-table-column title="目标数据集" data-index="target_dataset" :width="180" />
          <a-table-column title="频率" data-index="freq" :width="90" />
          <a-table-column title="标的模式" data-index="subject_mode" :width="110" />
          <a-table-column title="标的列表" data-index="subjects_json" :width="220" :ellipsis="true" :tooltip="true" />
          <a-table-column title="状态" :width="100">
            <template #cell="{ record }">
              <a-tag size="small" :color="bindingStatusColor(record.status)">{{ record.status }}</a-tag>
            </template>
          </a-table-column>
          <a-table-column title="更新时间" :width="180">
            <template #cell="{ record }">{{ formatTime(record.updated_at) }}</template>
          </a-table-column>
          <a-table-column title="操作" :width="210" align="center" :fixed="'right'">
            <template #cell="{ record }">
              <a-space>
                <a-button size="mini" type="text" @click="openEdit(record)">编辑</a-button>
                <a-button size="mini" type="text" @click="toggleStatus(record)">
                  {{ record.status === 'enabled' ? '禁用' : '启用' }}
                </a-button>
                <a-popconfirm content="确认删除该绑定？" @ok="remove(record)">
                  <a-button size="mini" type="text" status="danger">删除</a-button>
                </a-popconfirm>
              </a-space>
            </template>
          </a-table-column>
        </template>
      </a-table>
    </template>

    <a-modal
      v-model:visible="visible"
      width="820px"
      :title="modalTitle"
      :align-center="false"
      :top="'48px'"
      :modal-style="{ maxWidth: 'calc(100vw - 32px)' }"
      :body-style="{ maxHeight: 'calc(100vh - 176px)', overflowY: 'auto', padding: '18px 24px 14px' }"
      @ok="submit"
    >
      <a-form class="binding-form" :model="form" layout="vertical">
        <a-form-item field="factor_id" label="因子" required>
          <a-select v-model="form.factor_id" allow-search>
            <a-option v-for="item in factorOptions" :key="item.factor_id" :value="item.factor_id">
              {{ item.factor_id }} / {{ item.name }}
            </a-option>
          </a-select>
        </a-form-item>
        <a-form-item field="source_dataset" label="源数据集" required>
          <a-select v-model="form.source_dataset" allow-search allow-create @change="onSourceDatasetChange">
            <a-option v-for="item in datasetOptions" :key="item.dataset_id" :value="item.dataset_id">
              {{ item.dataset_id }}
            </a-option>
          </a-select>
        </a-form-item>
        <a-form-item field="freq" label="频率" required>
          <a-input v-model="form.freq" placeholder="1m" />
        </a-form-item>
        <a-form-item field="target_dataset" label="目标数据集">
          <a-input v-model="form.target_dataset" placeholder="留空由系统生成" />
        </a-form-item>
        <a-form-item field="subject_mode" label="标的范围">
          <a-select v-model="form.subject_mode">
            <a-option value="all">全部标的</a-option>
            <a-option value="include">白名单</a-option>
          </a-select>
        </a-form-item>
        <a-form-item field="status" label="状态">
          <a-select v-model="form.status">
            <a-option value="enabled">enabled</a-option>
            <a-option value="disabled">disabled</a-option>
          </a-select>
        </a-form-item>
        <a-form-item class="form-span-2" field="subjects_json" label="标的白名单JSON">
          <a-textarea v-model="form.subjects_json" :disabled="form.subject_mode !== 'include'" :auto-size="{ minRows: 3, maxRows: 6 }" placeholder='["BTC-USDT","ETH-USDT"]' />
        </a-form-item>
      </a-form>
    </a-modal>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref, watch } from 'vue';
import { Message } from '@arco-design/web-vue';
import { deleteFactorBinding, listFactorBindings, listFactorDefs, upsertFactorBinding } from '@/api/factor';
import type { FactorBinding, FactorDef } from '@/api/factor/types';
import { listDatasets } from '@/api/storage/metadata';
import type { Dataset } from '@/api/storage/types';
import { useSpaceStore } from '@/store/modules/space';
import { applyPageResult, defaultPagination, formatTime } from '@/views/data/shared/metadata-utils';

defineOptions({ name: 'FactorBindings' });

const spaceStore = useSpaceStore();
const selectedSpaceId = computed(() => spaceStore.selectedSpaceId);
const rows = ref<FactorBinding[]>([]);
const factors = ref<FactorDef[]>([]);
const datasets = ref<Dataset[]>([]);
const loading = ref(false);
const visible = ref(false);
const editing = ref(false);
const pagination = reactive(defaultPagination());
const filters = reactive({ source_dataset: '', freq: '', status: '' });

const form = reactive<FactorBinding>({
  binding_id: '',
  factor_id: '',
  space_id: '',
  source_dataset: '',
  freq: '1m',
  subject_mode: 'all',
  subjects_json: '[]',
  target_dataset: '',
  status: 'enabled',
});

const modalTitle = computed(() => (editing.value ? '编辑绑定' : '新增绑定'));
const factorOptions = computed(() => factors.value);
const datasetOptions = computed(() => datasets.value);

async function load() {
  if (!selectedSpaceId.value) {
    rows.value = [];
    return;
  }
  loading.value = true;
  try {
    const [bindingRsp, factorRsp, datasetRsp] = await Promise.all([
      listFactorBindings({
        space_id: selectedSpaceId.value,
        source_dataset: filters.source_dataset || undefined,
        freq: filters.freq || undefined,
        status: filters.status || undefined,
        page: { page: pagination.current, size: pagination.pageSize },
      }),
      listFactorDefs({ page: { page: 1, size: 500 } }),
      listDatasets({ space_id: selectedSpaceId.value, data_kind: 'DATA_KIND_TIME_SERIES', page: { page: 1, size: 500 } }),
    ]);
    rows.value = bindingRsp.bindings || [];
    factors.value = factorRsp.factors || [];
    datasets.value = datasetRsp.datasets || [];
    applyPageResult(pagination, bindingRsp.page_result);
  } finally {
    loading.value = false;
  }
}

function reloadFirstPage() {
  pagination.current = 1;
  load();
}

function resetForm() {
  const datasetID = filters.source_dataset || datasets.value[0]?.dataset_id || '';
  Object.assign(form, {
    binding_id: '',
    factor_id: factors.value[0]?.factor_id || '',
    space_id: selectedSpaceId.value || '',
    source_dataset: datasetID,
    freq: filters.freq || '1m',
    subject_mode: 'all',
    subjects_json: '[]',
    target_dataset: '',
    status: 'enabled',
  });
}

function openCreate() {
  editing.value = false;
  resetForm();
  visible.value = true;
}

function openEdit(record: FactorBinding) {
  editing.value = true;
  Object.assign(form, {
    ...record,
    subjects_json: record.subjects_json || '[]',
  });
  visible.value = true;
}

async function submit() {
  const spaceId = spaceStore.requireSpaceId();
  if (!form.factor_id || !form.source_dataset || !form.freq) {
    Message.warning('请补全因子、源数据集和频率');
    return;
  }
  try {
    JSON.parse(form.subjects_json || '[]');
  } catch (err) {
    Message.warning(`标的白名单不是合法 JSON: ${(err as Error).message}`);
    return;
  }
  await upsertFactorBinding({ ...form, space_id: spaceId, target_dataset: form.target_dataset || '' });
  Message.success('绑定已保存，实时触发将在下一次快照刷新后生效');
  visible.value = false;
  await load();
}

async function toggleStatus(record: FactorBinding) {
  await upsertFactorBinding({ ...record, status: record.status === 'enabled' ? 'disabled' : 'enabled' });
  Message.success('状态已更新');
  await load();
}

async function remove(record: FactorBinding) {
  if (!record.binding_id) return;
  await deleteFactorBinding(record.binding_id);
  Message.success('绑定已删除');
  await load();
}

function onSourceDatasetChange() {
  form.target_dataset = '';
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

function bindingStatusColor(status?: string) {
  if (status === 'enabled') return 'green';
  if (status === 'disabled') return 'orange';
  return 'gray';
}

watch(selectedSpaceId, () => reloadFirstPage());
onMounted(load);
</script>

<style scoped>
.factor-page {
  height: 100%;
  box-sizing: border-box;
  padding: 20px 20px 72px;
  overflow-y: auto;
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

.page-head span {
  color: var(--color-text-3);
  font-size: 13px;
}

.top-alert,
.filters {
  margin-bottom: 14px;
}

.binding-form {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  column-gap: 18px;
}

.form-span-2 {
  grid-column: span 2;
}

@media (max-width: 768px) {
  .page-head {
    align-items: flex-start;
    flex-direction: column;
    gap: 12px;
  }

  .binding-form {
    grid-template-columns: 1fr;
  }

  .form-span-2 {
    grid-column: span 1;
  }
}
</style>
