<template>
  <div class="moox-page">
    <div class="moox-inner">
      <div class="page-actions">
        <a-space>
          <a-input v-model="datasetFilter" allow-clear placeholder="请输入数据集ID" @press-enter="search" />
          <a-button type="primary" :disabled="!selectedSpaceId" @click="search">
            <template #icon><icon-search /></template>
            <span>查询</span>
          </a-button>
        </a-space>
      </div>

      <a-alert v-if="!selectedSpaceId" type="warning" show-icon>请先在顶部选择空间</a-alert>

      <a-table
        v-else
        row-key="archive_file_id"
        size="small"
        :bordered="{ cell: true }"
        :loading="loading"
        :data="rows"
        :pagination="pagination"
        :scroll="{ x: 'max-content' }"
        @page-change="onPageChange"
        @page-size-change="onPageSizeChange"
        @sorter-change="onSorterChange"
      >
        <template #columns>
          <a-table-column title="归档ID" data-index="archive_file_id" :width="180" />
          <a-table-column title="数据集" data-index="dataset_id" :width="150" />
          <a-table-column title="分区" data-index="partition_key" :width="160" />
          <a-table-column title="文件URI" data-index="file_uri" :width="360" />
          <a-table-column title="格式" data-index="file_format" :width="100" />
          <a-table-column
            title="最小时间"
            data-index="min_time"
            :width="180"
            :sortable="{ sortDirections: ['ascend', 'descend'], sorter: true }"
          >
            <template #cell="{ record }">{{ formatTime(record.min_time) }}</template>
          </a-table-column>
          <a-table-column
            title="最大时间"
            data-index="max_time"
            :width="180"
            :sortable="{ sortDirections: ['ascend', 'descend'], sorter: true }"
          >
            <template #cell="{ record }">{{ formatTime(record.max_time) }}</template>
          </a-table-column>
          <a-table-column title="行数" data-index="row_count" :width="100" />
          <a-table-column title="内容Hash" data-index="content_hash" :width="220" />
          <a-table-column title="列" :width="220">
            <template #cell="{ record }">{{ joinList(record.columns) || "-" }}</template>
          </a-table-column>
          <a-table-column title="状态" :width="90">
            <template #cell="{ record }">
              <a-tag size="small" :color="statusColor(record.status)">{{ record.status }}</a-tag>
            </template>
          </a-table-column>
          <a-table-column
            title="创建时间"
            data-index="created_at"
            :width="180"
            :sortable="{ sortDirections: ['ascend', 'descend'], sorter: true }"
          >
            <template #cell="{ record }">{{ formatTime(record.created_at) }}</template>
          </a-table-column>
          <a-table-column
            title="更新时间"
            data-index="updated_at"
            :width="180"
            :sortable="{ sortDirections: ['ascend', 'descend'], sorter: true }"
          >
            <template #cell="{ record }">{{ formatTime(record.updated_at) }}</template>
          </a-table-column>
        </template>
      </a-table>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref, watch } from "vue";
import { listArchiveFiles } from "@/api/storage/metadata";
import type { ArchiveFile } from "@/api/storage/types";
import { useSpaceStore } from "@/store/modules/space";
import { applyPageResult, defaultPagination, formatTime, joinList, statusColor } from "@/views/data/shared/metadata-utils";

defineOptions({ name: "OpsStorageArchive" });

const spaceStore = useSpaceStore();
const selectedSpaceId = computed(() => spaceStore.selectedSpaceId);
const rows = ref<ArchiveFile[]>([]);
const loading = ref(false);
const datasetFilter = ref("");
type ArchiveSortField = "min_time" | "max_time" | "created_at" | "updated_at";
const sortBy = ref<ArchiveSortField | "">("");
const sortOrder = ref<"asc" | "desc">("asc");
const pagination = reactive(defaultPagination());

async function load() {
  if (!selectedSpaceId.value) {
    rows.value = [];
    return;
  }
  loading.value = true;
  try {
    const rsp = await listArchiveFiles({
      space_id: selectedSpaceId.value,
      dataset_id: datasetFilter.value,
      sort_by: sortBy.value || undefined,
      sort_order: sortBy.value ? sortOrder.value : undefined,
      page: { page: pagination.current, size: pagination.pageSize }
    });
    rows.value = rsp.archive_files || [];
    applyPageResult(pagination, rsp.page_result);
  } finally {
    loading.value = false;
  }
}

function search() {
  pagination.current = 1;
  load();
}

function onSorterChange(field: string, direction: string) {
  sortBy.value = direction ? (field as ArchiveSortField) : "";
  sortOrder.value = direction === "descend" ? "desc" : "asc";
  pagination.current = 1;
  load();
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
.page-actions {
  display: flex;
  justify-content: flex-start;
  margin-bottom: var(--moox-space-2);
}
</style>
