<template>
  <div class="collector-workbench">
    <DatasetDefinitions
      v-if="activeTab === 'definitions'"
      owner-module="collector"
      dataset-role="raw_collection"
      managed-by="manual"
      :filter-owner-modules="['collector']"
      :filter-dataset-roles="['raw_collection', 'import']"
      :include-unowned="true"
    >
      <template #page-title>
        <PageTitleTabs :model-value="activeTab" :items="tabs" aria-label="数据集合" @change="syncRoute" />
      </template>
    </DatasetDefinitions>
    <DatasetBrowse
      v-else
      :dataset-owner-modules="['collector']"
      :dataset-roles="['raw_collection', 'import']"
      :include-unowned="true"
    >
      <template #page-title>
        <PageTitleTabs :model-value="activeTab" :items="tabs" aria-label="数据集合" @change="syncRoute" />
      </template>
    </DatasetBrowse>
  </div>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue';
import { useRoute, useRouter } from 'vue-router';
import PageTitleTabs from '@/components/page-title-tabs/index.vue';
import DatasetDefinitions from '@/views/data/datasets/index.vue';
import DatasetBrowse from '@/views/data/browse/index.vue';

defineOptions({ name: 'CollectorDatasets' });

const route = useRoute();
const router = useRouter();
const activeTab = ref(tabFromRoute());
type CollectorDatasetTab = 'definitions' | 'browse';

const tabs = [
  { key: 'definitions', label: '集合定义' },
  { key: 'browse', label: '查看数据' },
] as const;

const normalizedQuery = computed(() => String(route.query.tab || ''));

function tabFromRoute() {
  return route.query.tab === 'browse' ? 'browse' : 'definitions';
}

function syncRoute(key: string | number) {
  const tab: CollectorDatasetTab = key === 'browse' ? 'browse' : 'definitions';
  activeTab.value = tab;
  router.replace({ path: '/collector/datasets', query: tab === 'browse' ? { tab } : {} });
}

watch(normalizedQuery, () => {
  activeTab.value = tabFromRoute();
});
</script>

<style scoped>
.collector-workbench {
  display: flex;
  flex-direction: column;
  height: 100%;
  min-height: 0;
  overflow: hidden;
}
</style>
