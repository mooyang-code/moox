<template>
  <div class="moox-page">
    <div class="moox-inner">
      <div class="page-head">
        <h2>构造配置</h2>
        <a-space wrap>
          <a-button :loading="loading" @click="load">查询</a-button>
          <a-button type="primary" :loading="retrying" @click="retryRecover">重试恢复</a-button>
        </a-space>
      </div>

      <a-alert v-if="!selectedSpaceId" type="warning" show-icon>请先在顶部选择空间</a-alert>
      <a-alert v-else-if="error" type="error" show-icon>
        {{ error }}
        <a-link @click="load">重试</a-link>
      </a-alert>
      <a-spin v-else :loading="loading">
        <a-empty v-if="!rows.length" description="暂无复合因子数据集" />
        <a-table
          v-else
          row-key="dataset_id"
          size="small"
          :bordered="{ cell: true }"
          :data="rows"
          :pagination="false"
          :scroll="{ x: 'max-content' }"
        >
          <template #columns>
            <a-table-column title="数据集ID" data-index="dataset_id" :width="220" />
            <a-table-column title="中文名" data-index="name" :width="140" />
            <a-table-column title="merge_mode" :width="120">
              <template #cell="{ record }">{{ record.attributes?.merge_mode || "-" }}</template>
            </a-table-column>
            <a-table-column title="资源状态" :width="120">
              <template #cell="{ record }">{{ record.attributes?.storage_resource_state || "-" }}</template>
            </a-table-column>
            <a-table-column title="字段归属" :width="280">
              <template #cell="{ record }">{{ ownershipText(record.dataset_id) }}</template>
            </a-table-column>
          </template>
        </a-table>
      </a-spin>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref, watch } from "vue";
import { Message } from "@arco-design/web-vue";
import { listDatasetColumns, listDatasets } from "@/api/storage/metadata";
import type { Dataset, DatasetColumn } from "@/api/storage/types";
import { useSpaceStore } from "@/store/modules/space";
import { ensureDefaultView } from "@/views/data/datasets/default-view";
import { fieldOwnershipLabel, isMergedFactorDataset } from "@/views/data/shared/module-attribution";

defineOptions({ name: "FactorConstruct" });

const spaceStore = useSpaceStore();
const selectedSpaceId = computed(() => spaceStore.selectedSpaceId);
const rows = ref<Dataset[]>([]);
const columnsByDataset = ref<Record<string, DatasetColumn[]>>({});
const loading = ref(false);
const retrying = ref(false);
const error = ref("");

function ownershipText(datasetId: string) {
  const columns = columnsByDataset.value[datasetId] || [];
  if (!columns.length) return "-";
  const base = columns.filter(item => fieldOwnershipLabel(item) === "基础字段").map(item => item.column_name);
  const output = columns.filter(item => fieldOwnershipLabel(item) === "因子输出").map(item => item.column_name);
  return `基础字段 ${base.join(", ") || "-"}；因子输出 ${output.join(", ") || "-"}`;
}

async function load() {
  const spaceId = selectedSpaceId.value;
  if (!spaceId) {
    rows.value = [];
    columnsByDataset.value = {};
    return;
  }
  loading.value = true;
  error.value = "";
  try {
    const datasets: Dataset[] = [];
    for (let pageNo = 1; ; pageNo += 1) {
      const rsp = await listDatasets({ space_id: spaceId, page: { page: pageNo, size: 500 } });
      datasets.push(...(rsp.datasets || []));
      if (!rsp.page_result?.has_more || (rsp.datasets || []).length === 0) break;
    }
    rows.value = datasets.filter(isMergedFactorDataset);
    const nextColumns: Record<string, DatasetColumn[]> = {};
    await Promise.all(
      rows.value.map(async item => {
        const rsp = await listDatasetColumns({
          space_id: spaceId,
          dataset_id: item.dataset_id,
          page: { page: 1, size: 500 }
        });
        nextColumns[item.dataset_id] = rsp.columns || [];
      })
    );
    columnsByDataset.value = nextColumns;
  } catch (err) {
    error.value = err instanceof Error ? err.message : "构造配置加载失败";
  } finally {
    loading.value = false;
  }
}

async function retryRecover() {
  retrying.value = true;
  try {
    for (const dataset of rows.value) {
      await ensureDefaultView(dataset, {
        ownerModule: "factor",
        viewRole: "analysis",
        managedBy: "factor"
      });
    }
    await load();
    Message.success("已重试跨接口恢复");
  } catch (err) {
    Message.error(err instanceof Error ? err.message : "重试恢复失败");
  } finally {
    retrying.value = false;
  }
}

watch(selectedSpaceId, load);
onMounted(load);
</script>

<style scoped>
.page-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: var(--moox-space-3);
}

.page-head h2 {
  margin: 0;
  font-size: 20px;
  font-weight: 600;
}
</style>
