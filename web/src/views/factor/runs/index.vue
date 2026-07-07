<template>
  <div class="factor-page">
    <div class="page-head">
      <div>
        <h2>因子运行</h2>
        <span>查看实时计算、单标的补算和手动运行记录。</span>
      </div>
      <a-space>
        <a-button type="primary" :disabled="!selectedSpaceId" @click="openRecalc">
          <template #icon><icon-play-arrow /></template>
          发起补算
        </a-button>
        <a-button :disabled="!selectedSpaceId" @click="load">
          <template #icon><icon-refresh /></template>
          刷新
        </a-button>
      </a-space>
    </div>

    <a-alert v-if="!selectedSpaceId" class="top-alert" type="warning" show-icon>请先在顶部选择空间</a-alert>

    <template v-else>
      <div class="status-strip">
        <span>队列深度：{{ engineStatus.queue_depth ?? 0 }}</span>
        <span>过期合并：{{ engineStatus.supersede_count ?? 0 }}</span>
        <span>写回失败：{{ engineStatus.writeback_failures ?? 0 }}</span>
        <span>Worker：{{ engineStatus.workers?.length || 0 }}</span>
      </div>

      <a-space class="filters" wrap>
        <a-input v-model="filters.source_dataset" allow-clear placeholder="源数据集" @press-enter="reloadFirstPage" />
        <a-input v-model="filters.subject_id" allow-clear placeholder="标的" @press-enter="reloadFirstPage" />
        <a-input v-model="filters.freq" allow-clear placeholder="频率" style="width: 120px" @press-enter="reloadFirstPage" />
        <a-select v-model="filters.status" allow-clear placeholder="状态" style="width: 140px" @change="reloadFirstPage">
          <a-option value="succeeded">succeeded</a-option>
          <a-option value="failed">failed</a-option>
          <a-option value="superseded">superseded</a-option>
        </a-select>
        <a-button @click="reloadFirstPage">查询</a-button>
      </a-space>

      <div class="runs-table-shell">
        <a-table
          row-key="run_id"
          size="small"
          :bordered="{ cell: true }"
          :loading="loading"
          :data="rows"
          :pagination="pagination"
          :scroll="tableScroll"
          @page-change="onPageChange"
          @page-size-change="onPageSizeChange"
        >
          <template #columns>
            <a-table-column title="触发" data-index="trigger_type" :width="100" />
            <a-table-column title="源数据集" data-index="source_dataset" :width="180" />
            <a-table-column title="目标数据集" data-index="target_dataset" :width="180" />
            <a-table-column title="标的" data-index="subject_id" :width="150" />
            <a-table-column title="频率" data-index="freq" :width="80" />
            <a-table-column title="Bar时间" :width="180">
              <template #cell="{ record }">{{ formatTime(record.bar_time) }}</template>
            </a-table-column>
            <a-table-column title="因子数" data-index="factor_count" :width="90" />
            <a-table-column title="耗时(ms)" data-index="elapsed_ms" :width="100" />
            <a-table-column title="状态" :width="110">
              <template #cell="{ record }">
                <a-tag size="small" :color="runStatusColor(record.status)">{{ record.status }}</a-tag>
              </template>
            </a-table-column>
            <a-table-column title="错误" data-index="error" :width="280" :ellipsis="true" :tooltip="true" />
            <a-table-column title="记录时间" :width="180">
              <template #cell="{ record }">{{ formatTime(record.created_at) }}</template>
            </a-table-column>
          </template>
        </a-table>
      </div>
    </template>

    <a-modal
      v-model:visible="recalcVisible"
      width="760px"
      title="发起补算"
      :align-center="false"
      :top="'64px'"
      :modal-style="{ maxWidth: 'calc(100vw - 32px)' }"
      @ok="submitRecalc"
    >
      <a-form class="recalc-form" :model="recalcForm" layout="vertical">
        <a-form-item field="factor_id" label="因子">
          <a-select v-model="recalcForm.factor_id" allow-clear allow-search placeholder="留空表示全部已启用因子">
            <a-option v-for="item in factorOptions" :key="item.factor_id" :value="item.factor_id">
              {{ item.factor_id }} / {{ item.name }}
            </a-option>
          </a-select>
        </a-form-item>
        <a-form-item field="source_dataset" label="源数据集" required>
          <a-input v-model="recalcForm.source_dataset" placeholder="binance_spot_kline" />
        </a-form-item>
        <a-form-item field="subject_id" label="标的" required>
          <a-input v-model="recalcForm.subject_id" placeholder="BTC-USDT" />
        </a-form-item>
        <a-form-item field="freq" label="频率" required>
          <a-input v-model="recalcForm.freq" placeholder="1m" />
        </a-form-item>
        <a-form-item field="end_time" label="Bar时间">
          <a-input v-model="recalcForm.end_time" placeholder="留空表示当前时间" />
        </a-form-item>
      </a-form>
      <a-alert v-if="lastRecalcId" class="top-alert" type="success" show-icon>
        最近补算ID：{{ lastRecalcId }}
      </a-alert>
    </a-modal>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref, watch } from 'vue';
import { Message } from '@arco-design/web-vue';
import { getEngineStatus, listFactorDefs, listFactorRuns, recalcFactor } from '@/api/factor';
import type { EngineStatus, FactorDef, FactorRun, RecalcFactorReq } from '@/api/factor/types';
import { useSpaceStore } from '@/store/modules/space';
import { applyPageResult, defaultPagination, formatTime } from '@/views/data/shared/metadata-utils';

defineOptions({ name: 'FactorRuns' });

const spaceStore = useSpaceStore();
const selectedSpaceId = computed(() => spaceStore.selectedSpaceId);
const rows = ref<FactorRun[]>([]);
const factors = ref<FactorDef[]>([]);
const loading = ref(false);
const recalcVisible = ref(false);
const lastRecalcId = ref('');
const pagination = reactive(defaultPagination());
const filters = reactive({ source_dataset: '', subject_id: '', freq: '', status: '' });
const engineStatus = reactive<Partial<EngineStatus>>({});

const recalcForm = reactive<RecalcFactorReq>({
  factor_id: '',
  space_id: '',
  source_dataset: '',
  subject_id: '',
  freq: '1m',
  start_time: '',
  end_time: '',
});

const factorOptions = computed(() => factors.value);
const tableScroll = computed(() => ({
  x: 'max-content',
  y: 'calc(100vh - 360px)',
}));

async function load() {
  if (!selectedSpaceId.value) {
    rows.value = [];
    return;
  }
  loading.value = true;
  try {
    const [runRsp, factorRsp, statusRsp] = await Promise.all([
      listFactorRuns({
        space_id: selectedSpaceId.value,
        source_dataset: filters.source_dataset || undefined,
        subject_id: filters.subject_id || undefined,
        freq: filters.freq || undefined,
        status: filters.status || undefined,
        page: { page: pagination.current, size: pagination.pageSize },
      }),
      listFactorDefs({ page: { page: 1, size: 500 } }),
      getEngineStatus(),
    ]);
    rows.value = runRsp.runs || [];
    factors.value = factorRsp.factors || [];
    Object.assign(engineStatus, statusRsp);
    applyPageResult(pagination, runRsp.page_result || { total: rows.value.length });
  } finally {
    loading.value = false;
  }
}

function reloadFirstPage() {
  pagination.current = 1;
  load();
}

function openRecalc() {
  Object.assign(recalcForm, {
    factor_id: '',
    space_id: selectedSpaceId.value || '',
    source_dataset: filters.source_dataset || '',
    subject_id: filters.subject_id || '',
    freq: filters.freq || '1m',
    start_time: '',
    end_time: '',
  });
  recalcVisible.value = true;
}

async function submitRecalc() {
  const spaceId = spaceStore.requireSpaceId();
  if (!recalcForm.source_dataset || !recalcForm.subject_id || !recalcForm.freq) {
    Message.warning('请补全源数据集、标的和频率');
    return;
  }
  const rsp = await recalcFactor({ ...recalcForm, space_id: spaceId });
  lastRecalcId.value = rsp.recalc_id;
  Message.success(`补算已提交：${rsp.recalc_id}`);
  recalcVisible.value = false;
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

function runStatusColor(status?: string) {
  if (status === 'succeeded') return 'green';
  if (status === 'failed') return 'red';
  if (status === 'superseded') return 'orange';
  return 'gray';
}

watch(selectedSpaceId, () => reloadFirstPage());
onMounted(load);
</script>

<style scoped>
.factor-page {
  display: flex;
  flex-direction: column;
  min-height: 0;
  height: 100%;
  padding: 20px;
  overflow: hidden;
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

.status-strip {
  display: flex;
  flex-wrap: wrap;
  gap: 10px;
  margin-bottom: 14px;
}

.status-strip span {
  border: 1px solid var(--color-border-2);
  border-radius: 6px;
  color: var(--color-text-2);
  font-size: 13px;
  line-height: 30px;
  padding: 0 12px;
}

.runs-table-shell {
  flex: 1;
  min-height: 0;
  overflow: hidden;
}

.runs-table-shell :deep(.arco-table-container) {
  min-height: 0;
}

.recalc-form {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  column-gap: 18px;
}

@media (max-width: 768px) {
  .page-head {
    align-items: flex-start;
    flex-direction: column;
    gap: 12px;
  }

  .recalc-form {
    grid-template-columns: 1fr;
  }
}
</style>
