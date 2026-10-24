<template>
  <div class="moox-page">
    <div class="moox-inner">
      <div class="page-head">
        <div class="title-with-info">
          <h2>数据节点</h2>
          <a-tooltip v-model:popup-visible="nodeInfoVisible" position="right">
            <template #content>
              <div class="node-concepts">
                <div>数据节点由部署流程登记，管理台只读；增删和启停请通过 moox skill（moox-cli）操作。</div>
                <div>Dataset 直接绑定 DataNode，首次激活后锁定绑定，不能再迁移。</div>
              </div>
            </template>
            <button
              class="info-button"
              type="button"
              aria-label="数据节点说明"
              @focus="nodeInfoVisible = true"
              @blur="nodeInfoVisible = false"
            >
              <icon-info-circle />
            </button>
          </a-tooltip>
        </div>
      </div>

      <a-table
        row-key="node.node_id"
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
          <a-table-column title="节点ID" :width="190">
            <template #cell="{ record }">
              <span class="readonly-value">{{ record.node.node_id }}</span>
            </template>
          </a-table-column>
          <a-table-column title="名称" :width="180">
            <template #cell="{ record }">{{ record.node.name || "-" }}</template>
          </a-table-column>
          <a-table-column title="服务目标" :width="280">
            <template #cell="{ record }">
              <span class="readonly-value">{{ record.node.service_target || "-" }}</span>
            </template>
          </a-table-column>
          <a-table-column title="Dataset" :width="280">
            <template #cell="{ record }">
              <div v-if="record.datasets?.length" class="dataset-tags">
                <a-tooltip v-for="summary in record.datasets" :key="`${summary.space_id}:${summary.dataset_id}`">
                  <template #content>Space：{{ summary.space_id }} · Dataset ID：{{ summary.dataset_id }}</template>
                  <a-tag
                    class="dataset-tag"
                    size="small"
                    color="arcoblue"
                    role="link"
                    tabindex="0"
                    @click="openDataset(summary)"
                    @keydown.enter.prevent="openDataset(summary)"
                    @keydown.space.prevent="openDataset(summary)"
                  >
                    {{ summary.name || summary.dataset_id }}
                  </a-tag>
                </a-tooltip>
              </div>
              <span v-else class="empty-value">-</span>
            </template>
          </a-table-column>
          <a-table-column title="状态" :width="90">
            <template #cell="{ record }">
              <a-tag size="small" :color="statusColor(record.node.status)">{{ statusLabel(record.node.status) }}</a-tag>
            </template>
          </a-table-column>
          <a-table-column title="更新时间" :width="180">
            <template #cell="{ record }">{{ formatTime(record.node.updated_at) }}</template>
          </a-table-column>
          <a-table-column title="操作" :width="90" align="center" :fixed="'right'">
            <template #cell="{ record }">
              <a-space class="row-actions" :size="4" wrap>
                <a-button size="mini" type="text" @click="openDetail(record)">
                  <template #icon><icon-eye /></template>
                  查看
                </a-button>
              </a-space>
            </template>
          </a-table-column>
        </template>
      </a-table>
    </div>

    <a-modal
      v-model:visible="detailVisible"
      data-testid="data-node-detail-modal"
      width="1000px"
      title="数据节点详情"
      :footer="false"
    >
      <a-descriptions v-if="detailNode" :column="{ xs: 1, sm: 2 }" bordered>
        <a-descriptions-item label="节点ID">{{ detailNode.node.node_id }}</a-descriptions-item>
        <a-descriptions-item label="名称">{{ detailNode.node.name || "-" }}</a-descriptions-item>
        <a-descriptions-item label="服务目标" :span="2">{{ detailNode.node.service_target || "-" }}</a-descriptions-item>
        <a-descriptions-item label="状态">
          <a-tag size="small" :color="statusColor(detailNode.node.status)">{{ statusLabel(detailNode.node.status) }}</a-tag>
        </a-descriptions-item>
        <a-descriptions-item label="更新时间">{{ formatTime(detailNode.node.updated_at) }}</a-descriptions-item>
      </a-descriptions>

      <div v-if="detailNode" class="detail-datasets">
        <h3>Dataset</h3>
        <div v-if="detailNode.datasets.length" class="detail-datasets-list">
          <a-table
            row-key="dataset_id"
            size="small"
            :bordered="{ cell: true }"
            :data="detailDatasetRows"
            :pagination="false"
            :scroll="{ x: 'max-content', y: '380px' }"
          >
            <template #columns>
              <a-table-column title="Space" data-index="space_id" :width="120" />
              <a-table-column title="Dataset ID" data-index="dataset_id" :width="150" />
              <a-table-column title="名称" :width="150">
                <template #cell="{ record }">{{ record.name || record.dataset_id }}</template>
              </a-table-column>
              <a-table-column title="数据类型" :width="150">
                <template #cell="{ record }">{{ optionLabel(dataKindOptions, record.data_kind) }}</template>
              </a-table-column>
              <a-table-column title="保留期" :width="150">
                <template #cell="{ record }">
                  {{ formatRetention(record.retention) }}
                  <span class="retention-source">{{ retentionSourceLabel(record.retention_source) }}</span>
                </template>
              </a-table-column>
              <a-table-column title="状态" :width="90">
                <template #cell="{ record }">
                  <a-tag size="small" :color="statusColor(record.status)">{{ statusLabel(record.status) }}</a-tag>
                </template>
              </a-table-column>
              <a-table-column title="操作" :width="100" align="center">
                <template #cell="{ record }">
                  <a-button size="mini" type="text" @click="openDataset(record)">打开</a-button>
                </template>
              </a-table-column>
            </template>
          </a-table>
          <a-pagination
            class="detail-datasets-pagination"
            :current="detailPagination.current"
            :total="detailPagination.total"
            :page-size="detailPagination.pageSize"
            :show-total="detailPagination.showTotal"
            :show-page-size="detailPagination.showPageSize"
            :hide-on-single-page="detailPagination.hideOnSinglePage"
            @change="onDetailPageChange"
          />
        </div>
        <a-empty v-else description="暂无 Dataset" />
      </div>
    </a-modal>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref } from "vue";
import { useRouter } from "vue-router";
import { listDataNodes } from "@/api/storage/metadata";
import type { DataNodeListItem, DatasetSummary } from "@/api/storage/types";
import { formatRetention, retentionSourceLabel } from "@/utils/retention";
import {
  applyPageResult,
  dataKindOptions,
  defaultPagination,
  formatTime,
  optionLabel,
  statusColor,
  statusLabel
} from "@/views/data/shared/metadata-utils";

defineOptions({ name: "OpsStorageDataNodes" });

const router = useRouter();
const rows = ref<DataNodeListItem[]>([]);
const loading = ref(false);
const nodeInfoVisible = ref(false);
const detailVisible = ref(false);
const detailNode = ref<DataNodeListItem>();
const detailPagination = reactive({
  current: 1,
  pageSize: 10,
  total: 0,
  showTotal: true,
  showPageSize: false,
  hideOnSinglePage: false
});
const pagination = reactive(defaultPagination());
const detailDatasetRows = computed(() => {
  if (!detailNode.value) return [];
  const start = (detailPagination.current - 1) * detailPagination.pageSize;
  return detailNode.value.datasets.slice(start, start + detailPagination.pageSize);
});

async function load() {
  loading.value = true;
  try {
    const rsp = await listDataNodes({ page: { page: pagination.current, size: pagination.pageSize } });
    rows.value = rsp.items || [];
    applyPageResult(pagination, rsp.page_result);
  } finally {
    loading.value = false;
  }
}

function openDetail(record: DataNodeListItem) {
  detailNode.value = record;
  detailPagination.current = 1;
  detailPagination.total = record.datasets.length;
  detailVisible.value = true;
}

function onDetailPageChange(page: number) {
  detailPagination.current = page;
}

function openDataset(summary: DatasetSummary) {
  void router.push({
    path: "/collector/tasks",
    query: { tab: "results", space_id: summary.space_id }
  });
}

function onPageChange(page: number) {
  pagination.current = page;
  void load();
}

function onPageSizeChange(pageSize: number) {
  pagination.current = 1;
  pagination.pageSize = pageSize;
  void load();
}

onMounted(load);
</script>

<style scoped>
.retention-source {
  margin-left: 6px;
  color: var(--color-text-3);
  font-size: 12px;
}

.page-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: var(--moox-space-2);
}

.title-with-info {
  display: inline-flex;
  align-items: center;
  gap: var(--moox-space-1);
}

.title-with-info h2 {
  margin: 0;
  font-size: 20px;
  font-weight: 600;
}

.info-button {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 24px;
  height: 24px;
  padding: 0;
  color: var(--color-text-3);
  cursor: help;
  border: 0;
  background: transparent;
}

.node-concepts {
  max-width: 360px;
  line-height: 1.7;
}

.dataset-tags {
  display: flex;
  flex-wrap: wrap;
  gap: 4px;
  max-width: 260px;
}

.dataset-tag {
  max-width: 250px;
  cursor: pointer;
}

.dataset-tag :deep(.arco-tag-content) {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.readonly-value {
  color: var(--color-text-2);
  overflow-wrap: anywhere;
}

.empty-value {
  color: var(--color-text-3);
}

.row-actions {
  width: 90px;
  justify-content: center;
}

.detail-datasets {
  margin-top: 24px;
}

.detail-datasets h3 {
  margin: 0 0 12px;
  font-size: 16px;
}

.detail-datasets-list {
  min-height: 452px;
}

.detail-datasets-pagination {
  display: flex;
  justify-content: flex-end;
  margin-top: 16px;
}
</style>
