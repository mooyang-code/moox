<template>
  <div class="collector-workbench">
    <a-tabs v-model:active-key="activeTab" type="rounded" size="medium" @change="syncRoute">
      <a-tab-pane key="definitions" title="集合定义">
        <DatasetDefinitions
          owner-module="collector"
          dataset-role="raw_collection"
          managed-by="manual"
          :filter-owner-modules="['collector']"
          :filter-dataset-roles="['raw_collection', 'import']"
          :include-unowned="true"
        />
      </a-tab-pane>
      <a-tab-pane key="browse" title="查看数据">
        <DatasetBrowse
          :dataset-owner-modules="['collector']"
          :dataset-roles="['raw_collection', 'import']"
          :include-unowned="true"
        />
      </a-tab-pane>
    </a-tabs>
  </div>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue';
import { useRoute, useRouter } from 'vue-router';
import DatasetDefinitions from '@/views/data/datasets/index.vue';
import DatasetBrowse from '@/views/data/browse/index.vue';

defineOptions({ name: 'CollectorDatasets' });

const route = useRoute();
const router = useRouter();
const activeTab = ref(tabFromRoute());

const normalizedQuery = computed(() => String(route.query.tab || ''));

function tabFromRoute() {
  return route.query.tab === 'browse' ? 'browse' : 'definitions';
}

function syncRoute(key: string | number) {
  const tab = key === 'browse' ? 'browse' : 'definitions';
  router.replace({ path: '/collector/datasets', query: tab === 'browse' ? { tab } : {} });
}

watch(normalizedQuery, () => {
  activeTab.value = tabFromRoute();
});
</script>

<style scoped>
.collector-workbench {
  min-height: 0;
}

.collector-workbench :deep(.arco-tabs-content) {
  padding-top: 0;
}
</style>
