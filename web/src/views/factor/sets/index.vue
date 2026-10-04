<template>
  <div class="moox-page">
    <div class="moox-inner">
      <div class="page-head">
        <h2>因子集</h2>
        <a-space>
          <a-button type="primary" status="success" :disabled="!selectedSpaceId" @click="openCreate">
            <template #icon><icon-plus /></template>
            新建因子集
          </a-button>
        </a-space>
      </div>

      <a-alert v-if="!selectedSpaceId" type="warning" show-icon>请先在顶部选择空间</a-alert>
      <a-table
        v-else
        row-key="factor_set.set_id"
        size="small"
        :bordered="{ cell: true }"
        :loading="loading"
        :data="pageRows"
        :pagination="pagination"
        :scroll="{ x: 'max-content' }"
        @page-change="onPageChange"
        @page-size-change="onPageSizeChange"
      >
        <template #columns>
          <a-table-column title="因子集 ID" :width="220">
            <template #cell="{ record }">{{ record.factor_set.set_id }}</template>
          </a-table-column>
          <a-table-column title="源数据集" :width="260">
            <template #cell="{ record }">{{ datasetLabel(record.factor_set.source_dataset_id) }}</template>
          </a-table-column>
          <a-table-column title="频率" :width="80">
            <template #cell="{ record }">{{ record.factor_set.freq }}</template>
          </a-table-column>
          <a-table-column title="结果数据集" :width="270">
            <template #cell="{ record }">{{ record.factor_set.result_dataset_id }}</template>
          </a-table-column>
          <a-table-column title="状态" :width="100">
            <template #cell="{ record }">
              <a-tag size="small" :color="setStatusColor(record.factor_set.status)">{{ statusLabel(record.factor_set.status) }}</a-tag>
            </template>
          </a-table-column>
          <a-table-column title="最近周期" :width="180">
            <template #cell="{ record }">{{ periodTime(record.last_run?.last_period_time) }}</template>
          </a-table-column>
          <a-table-column title="延迟" :width="100">
            <template #cell="{ record }">{{ lagLabel(record.last_run?.lag_seconds) }}</template>
          </a-table-column>
          <a-table-column title="操作" :width="120" align="center" :fixed="'right'">
            <template #cell="{ record }">
              <a-button
                v-if="record.factor_set.status !== 'pending'"
                size="mini"
                type="text"
                :status="record.factor_set.status === 'enabled' ? 'danger' : 'success'"
                @click="toggleStatus(record.factor_set)"
              >
                {{ record.factor_set.status === "enabled" ? "停用" : "启用" }}
              </a-button>
            </template>
          </a-table-column>
        </template>
      </a-table>
    </div>

    <a-modal v-model:visible="visible" title="新建因子集" :width="640" @ok="submit">
      <a-form layout="vertical">
        <a-form-item label="源数据集" required>
          <a-select v-model="form.source_dataset_id" allow-search :loading="datasetsLoading" placeholder="选择已激活的时序数据集" @change="sourceChanged">
            <a-option v-for="dataset in sourceDatasets" :key="dataset.dataset_id" :value="dataset.dataset_id">
              {{ dataset.name || dataset.dataset_id }}（{{ dataset.dataset_id }}）
            </a-option>
          </a-select>
        </a-form-item>
        <a-form-item label="频率" required>
          <a-select v-model="form.freq" :disabled="!availableFreqs.length" placeholder="选择源数据集支持的频率">
            <a-option v-for="freq in availableFreqs" :key="freq" :value="freq">{{ freq }}</a-option>
          </a-select>
        </a-form-item>
        <a-form-item label="对象范围" required>
          <a-radio-group v-model="form.subject_mode" type="button">
            <a-radio value="all">全部对象</a-radio>
            <a-radio value="include">指定对象</a-radio>
          </a-radio-group>
        </a-form-item>
        <a-form-item v-if="form.subject_mode === 'include'" label="对象 ID" required>
          <a-input-tag v-model="subjects" allow-clear placeholder="输入对象 ID 后回车" />
        </a-form-item>
      </a-form>
    </a-modal>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref, watch } from "vue";
import { Message } from "@arco-design/web-vue";
import { createFactorSet, listFactorSets, setFactorSetStatus } from "@/api/factor";
import type { FactorSet, FactorSetInfo } from "@/api/factor/types";
import { listDatasets } from "@/api/storage/metadata";
import type { Dataset } from "@/api/storage/types";
import { useSpaceStore } from "@/store/modules/space";
import { defaultPagination, isTimeSeriesDataKind, statusLabel } from "@/views/data/shared/metadata-utils";

defineOptions({ name: "FactorSets" });

const spaceStore = useSpaceStore();
const selectedSpaceId = computed(() => spaceStore.selectedSpaceId);
const allSets = ref<FactorSetInfo[]>([]);
const sourceDatasets = ref<Dataset[]>([]);
const loading = ref(false);
const datasetsLoading = ref(false);
const visible = ref(false);
const pagination = reactive(defaultPagination());
const subjects = ref<string[]>([]);
const form = reactive({ source_dataset_id: "", freq: "", subject_mode: "all" as "all" | "include" });
let loadSequence = 0;

const rows = computed(() => allSets.value.filter(item => item.factor_set.space_id === selectedSpaceId.value));
const pageRows = computed(() => {
  const start = (pagination.current - 1) * pagination.pageSize;
  return rows.value.slice(start, start + pagination.pageSize);
});
const selectedDataset = computed(() => sourceDatasets.value.find(item => item.dataset_id === form.source_dataset_id));
const availableFreqs = computed(() => selectedDataset.value?.freqs || []);

watch(rows, () => {
  pagination.total = rows.value.length;
}, { immediate: true });

async function loadAllSets() {
  const items: FactorSetInfo[] = [];
  for (let page = 1; ; page += 1) {
    const rsp = await listFactorSets({ page: { page, size: 500 } });
    items.push(...(rsp.factor_sets || []));
    if (!rsp.page_result?.has_more || !(rsp.factor_sets || []).length) return items;
  }
}

async function loadAllDatasets(spaceId: string) {
  const items: Dataset[] = [];
  for (let page = 1; ; page += 1) {
    const rsp = await listDatasets({ space_id: spaceId, page: { page, size: 500 } });
    items.push(...(rsp.datasets || []));
    if (!rsp.page_result?.has_more || !(rsp.datasets || []).length) return items;
  }
}

async function load() {
  const spaceId = selectedSpaceId.value;
  const sequence = ++loadSequence;
  if (!spaceId) {
    allSets.value = [];
    sourceDatasets.value = [];
    loading.value = false;
    datasetsLoading.value = false;
    return;
  }
  loading.value = true;
  datasetsLoading.value = true;
  try {
    const [sets, datasets] = await Promise.all([loadAllSets(), loadAllDatasets(spaceId)]);
    if (sequence !== loadSequence || spaceId !== selectedSpaceId.value) return;
    allSets.value = sets;
    sourceDatasets.value = datasets.filter(
      item => item.status === "active" && isTimeSeriesDataKind(item.data_kind) && (item.freqs || []).length > 0
    );
  } catch (error) {
    if (sequence === loadSequence) Message.error(error instanceof Error ? error.message : "因子集加载失败");
  } finally {
    if (sequence === loadSequence) {
      loading.value = false;
      datasetsLoading.value = false;
    }
  }
}

function datasetLabel(datasetId: string) {
  const dataset = sourceDatasets.value.find(item => item.dataset_id === datasetId);
  return dataset?.name ? `${dataset.name}（${datasetId}）` : datasetId;
}

function sourceChanged() {
  form.freq = availableFreqs.value.includes(form.freq) ? form.freq : availableFreqs.value[0] || "";
}

function resetForm() {
  form.source_dataset_id = "";
  form.freq = "";
  form.subject_mode = "all";
  subjects.value = [];
}

function openCreate() {
  resetForm();
  visible.value = true;
}

async function submit() {
  const spaceId = selectedSpaceId.value;
  if (!spaceId || !form.source_dataset_id || !form.freq) {
    Message.warning("请选择源数据集和频率");
    return;
  }
  if (form.subject_mode === "include" && !subjects.value.length) {
    Message.warning("指定对象模式至少需要一个对象 ID");
    return;
  }
  try {
    const created = await createFactorSet({
      space_id: spaceId,
      source_dataset_id: form.source_dataset_id,
      freq: form.freq,
      subject_mode: form.subject_mode,
      subjects: form.subject_mode === "include" ? [...subjects.value] : []
    });
    if (spaceId !== selectedSpaceId.value) return;
    Message.success(`因子集已创建：${created.set_id}`);
    visible.value = false;
    pagination.current = 1;
    await load();
  } catch (error) {
    Message.error(error instanceof Error ? error.message : "创建因子集失败");
  }
}

async function toggleStatus(set: FactorSet) {
  const next = set.status === "enabled" ? "disabled" : "enabled";
  try {
    await setFactorSetStatus(set.set_id, next);
    Message.success(next === "disabled" ? "因子集已停用" : "因子集已启用");
    await load();
  } catch (error) {
    Message.error(error instanceof Error ? error.message : "更新因子集状态失败");
  }
}

function setStatusColor(status: string) {
  if (status === "enabled") return "green";
  if (status === "pending") return "blue";
  return "orange";
}

function periodTime(value?: number) {
  return value ? new Date(value * 1000).toLocaleString() : "-";
}

function lagLabel(value?: number) {
  return typeof value === "number" && Number.isFinite(value) ? `${Math.max(0, value)} 秒` : "-";
}

function onPageChange(page: number) {
  pagination.current = page;
}

function onPageSizeChange(pageSize: number) {
  pagination.current = 1;
  pagination.pageSize = pageSize;
}

watch(selectedSpaceId, () => {
  pagination.current = 1;
  load();
});
onMounted(load);
</script>

<style scoped>
.page-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: var(--moox-space-toolbar-table);
}

.page-head h2 {
  margin: 0;
  font-size: 20px;
  font-weight: 600;
}
</style>
