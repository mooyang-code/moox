<template>
  <div class="overview-page">
    <div class="page-head">
      <div>
        <h2>数据概览</h2>
        <span>当前空间：{{ spaceStore.selectedSpace?.name || '未选择' }}</span>
      </div>
      <a-button :disabled="!selectedSpaceId" @click="load">
        <template #icon><icon-refresh /></template>
        刷新
      </a-button>
    </div>

    <a-alert v-if="!selectedSpaceId" type="warning" show-icon>请先在顶部选择空间</a-alert>

    <a-spin v-else :loading="loading">
      <a-grid :cols="{ xs: 1, sm: 2, md: 3, lg: 4 }" :col-gap="16" :row-gap="16">
        <a-grid-item v-for="item in stats" :key="item.key">
          <a-card :bordered="false" class="stat-card" hoverable>
            <a-statistic :title="item.label" :value="item.value" :suffix="item.hasMore ? '+' : ''" />
            <template #extra>
              <a-link @click="router.push(item.path)">查看</a-link>
            </template>
          </a-card>
        </a-grid-item>
      </a-grid>
    </a-spin>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref, watch } from 'vue';
import { useRouter } from 'vue-router';
import {
  listArchiveFiles,
  listDatasets,
  listDataSources,
  listFactors,
  listFields,
  listPrimaryStoreRoutes,
  listSubjects,
  listViews,
} from '@/api/storage/metadata';
import type { PageResult } from '@/api/storage/types';
import { useSpaceStore } from '@/store/modules/space';

defineOptions({ name: 'DataOverview' });

interface CountResult {
  value: number;
  hasMore: boolean;
}

interface StatItem extends CountResult {
  key: string;
  label: string;
  path: string;
}

const router = useRouter();
const spaceStore = useSpaceStore();
const selectedSpaceId = computed(() => spaceStore.selectedSpaceId);
const loading = ref(false);
const stats = reactive<StatItem[]>([
  { key: 'sources', label: '数据来源', path: '/data/sources', value: 0, hasMore: false },
  { key: 'subjects', label: '数据对象', path: '/data/subjects', value: 0, hasMore: false },
  { key: 'datasets', label: '数据集', path: '/data/datasets', value: 0, hasMore: false },
  { key: 'fields', label: '字段', path: '/data/fields', value: 0, hasMore: false },
  { key: 'factors', label: '因子', path: '/data/factors', value: 0, hasMore: false },
  { key: 'views', label: '查询视图', path: '/data/views', value: 0, hasMore: false },
  { key: 'routes', label: '主存路由', path: '/ops/storage/routes', value: 0, hasMore: false },
  { key: 'archives', label: '归档文件', path: '/ops/storage/archive', value: 0, hasMore: false },
]);

function countFrom(page?: PageResult, fallbackLength = 0): CountResult {
  const total = Number(page?.total);
  if (Number.isFinite(total) && total >= 0) {
    return { value: total, hasMore: !!page?.has_more };
  }
  return { value: fallbackLength, hasMore: fallbackLength >= 200 };
}

function setStat(key: string, value: CountResult) {
  const item = stats.find((row) => row.key === key);
  if (!item) return;
  item.value = value.value;
  item.hasMore = value.hasMore;
}

async function load() {
  if (!selectedSpaceId.value) return;
  loading.value = true;
  try {
    const space_id = selectedSpaceId.value;
    const page = { page: 1, size: 1 };
    const [sources, subjects, datasets, fields, factors, views, routes, archives] = await Promise.all([
      listDataSources({ space_id, page }),
      listSubjects({ space_id, page }),
      listDatasets({ space_id, page }),
      listFields({ space_id, page }),
      listFactors({ space_id, page }),
      listViews({ space_id, page }),
      listPrimaryStoreRoutes({ space_id, page }),
      listArchiveFiles({ space_id, page }),
    ]);

    setStat('sources', countFrom(sources.page_result, sources.data_sources?.length));
    setStat('subjects', countFrom(subjects.page_result, subjects.subjects?.length));
    setStat('datasets', countFrom(datasets.page_result, datasets.datasets?.length));
    setStat('fields', countFrom(fields.page_result, fields.fields?.length));
    setStat('factors', countFrom(factors.page_result, factors.factors?.length));
    setStat('views', countFrom(views.page_result, views.views?.length));
    setStat('routes', countFrom(routes.page_result, routes.primary_store_routes?.length));
    setStat('archives', countFrom(archives.page_result, archives.archive_files?.length));
  } finally {
    loading.value = false;
  }
}

watch(selectedSpaceId, load);
onMounted(load);
</script>

<style scoped>
.overview-page {
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

.stat-card {
  min-height: 118px;
  border-radius: 8px;
}
</style>
