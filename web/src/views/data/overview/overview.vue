<template>
  <div class="overview-page">
    <div class="page-head">
      <div>
        <h2>数据概览</h2>
        <span>当前空间：{{ spaceStore.selectedSpace?.name || '未选择' }}</span>
      </div>
      <a-button :disabled="!selectedSpaceId" :loading="loading" @click="load">
        <template #icon><icon-refresh /></template>
        刷新
      </a-button>
    </div>

    <a-alert v-if="!selectedSpaceId" type="warning" show-icon>请先在顶部选择空间</a-alert>

    <a-spin v-else :loading="loading">
      <section class="overview-hero">
        <div>
          <strong>{{ spaceStore.selectedSpace?.name || selectedSpaceId }}</strong>
          <span>数据资产、查询视图与归档状态</span>
        </div>
        <a-button type="primary" @click="router.push('/data/browse')">浏览数据</a-button>
      </section>

      <section class="stat-grid">
        <button v-for="item in stats" :key="item.key" class="stat-card" @click="router.push(item.path)">
          <span class="accent" :style="{ background: item.color }" />
          <span class="stat-group">{{ item.group }}</span>
          <strong>{{ item.value }}{{ item.hasMore ? '+' : '' }}</strong>
          <span class="stat-label">{{ item.label }}</span>
          <small>{{ item.description }}</small>
        </button>
      </section>
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
import { pageResultTotal } from '@/views/data/shared/metadata-utils';

defineOptions({ name: 'DataOverview' });

interface CountResult {
  value: number;
  hasMore: boolean;
}

interface StatItem extends CountResult {
  key: string;
  group: string;
  label: string;
  description: string;
  path: string;
  color: string;
}

const router = useRouter();
const spaceStore = useSpaceStore();
const selectedSpaceId = computed(() => spaceStore.selectedSpaceId);
const loading = ref(false);
const stats = reactive<StatItem[]>([
  {
    key: 'sources',
    group: '接入',
    label: '数据源',
    description: '交易所、财经接口、文件导入来源',
    path: '/data/sources',
    value: 0,
    hasMore: false,
    color: '#2563eb',
  },
  {
    key: 'subjects',
    group: '对象',
    label: '数据对象',
    description: '标的、股票、合约等隔离实体',
    path: '/data/subjects',
    value: 0,
    hasMore: false,
    color: '#0891b2',
  },
  {
    key: 'datasets',
    group: '建模',
    label: '数据集',
    description: '时序与记录数据写入契约',
    path: '/data/datasets',
    value: 0,
    hasMore: false,
    color: '#16a34a',
  },
  {
    key: 'fields',
    group: '建模',
    label: '字段',
    description: '可复用字段定义与类型约束',
    path: '/data/fields',
    value: 0,
    hasMore: false,
    color: '#7c3aed',
  },
  {
    key: 'factors',
    group: '计算',
    label: '因子',
    description: '衍生计算字段与算法定义',
    path: '/data/factors',
    value: 0,
    hasMore: false,
    color: '#db2777',
  },
  {
    key: 'views',
    group: '查询',
    label: '查询视图',
    description: '面向分析与检索的读取模型',
    path: '/data/views',
    value: 0,
    hasMore: false,
    color: '#ea580c',
  },
  {
    key: 'routes',
    group: '主存',
    label: '主存路由',
    description: '数据集到 PrimaryStore 的路由配置',
    path: '/ops/storage/routes',
    value: 0,
    hasMore: false,
    color: '#475569',
  },
  {
    key: 'archives',
    group: '归档',
    label: '归档文件',
    description: '离线文件登记与归档结果',
    path: '/ops/storage/archive',
    value: 0,
    hasMore: false,
    color: '#ca8a04',
  },
]);

function countFrom(page?: PageResult, fallbackLength = 0): CountResult {
  if (page) {
    return { value: pageResultTotal(page), hasMore: !!page.has_more };
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

.page-head span,
.overview-hero span,
.stat-card small {
  color: var(--color-text-3);
}

.overview-page :deep(.arco-spin),
.overview-page :deep(.arco-spin-children) {
  display: block;
  width: 100%;
}

.overview-hero {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 16px;
  padding: 18px 20px;
  margin-bottom: 16px;
  border: 1px solid var(--color-border-2);
  border-radius: 8px;
  background: linear-gradient(135deg, var(--color-bg-2), var(--color-fill-1));
}

.overview-hero strong,
.overview-hero span {
  display: block;
}

.overview-hero strong {
  margin-bottom: 4px;
  font-size: 18px;
}

.stat-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(240px, 1fr));
  gap: 14px;
}

.stat-card {
  position: relative;
  min-height: 150px;
  padding: 16px;
  overflow: hidden;
  text-align: left;
  cursor: pointer;
  background: var(--color-bg-2);
  border: 1px solid var(--color-border-2);
  border-radius: 8px;
  transition: border-color 0.18s ease, box-shadow 0.18s ease, transform 0.18s ease;
}

.stat-card:hover {
  border-color: rgb(var(--primary-5));
  box-shadow: 0 8px 24px rgba(15, 23, 42, 0.08);
  transform: translateY(-1px);
}

.accent {
  position: absolute;
  top: 0;
  left: 0;
  width: 100%;
  height: 3px;
}

.stat-group {
  display: inline-flex;
  padding: 2px 7px;
  color: var(--color-text-2);
  font-size: 12px;
  background: var(--color-fill-2);
  border-radius: 4px;
}

.stat-card strong {
  display: block;
  margin-top: 14px;
  font-size: 30px;
  line-height: 1;
}

.stat-label {
  display: block;
  margin-top: 8px;
  color: var(--color-text-1);
  font-weight: 600;
}

.stat-card small {
  display: block;
  margin-top: 8px;
  line-height: 1.5;
}

@media (max-width: 720px) {
  .overview-hero {
    align-items: stretch;
    flex-direction: column;
  }
}
</style>
