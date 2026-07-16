<template>
  <div class="moox-page storage-config-page">
    <div class="moox-inner">
      <PageTitleTabs :model-value="activeTab" :items="tabs" aria-label="存储配置" @change="onTabChange" />

      <section class="storage-config-content">
        <keep-alive>
          <component :is="activeComponent" />
        </keep-alive>
      </section>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue';
import { useRoute, useRouter } from 'vue-router';
import PageTitleTabs from '@/components/page-title-tabs/index.vue';
import ArchiveFiles from './archive.vue';
import PrimaryStoreNodes from './nodes.vue';
import PrimaryStoreRoutes from './routes.vue';

type StorageConfigTab = 'nodes' | 'routes' | 'archive';

const tabs = [
  { key: 'nodes', label: '主存节点' },
  { key: 'routes', label: '主存路由' },
  { key: 'archive', label: '归档文件' },
] as const;

const route = useRoute();
const router = useRouter();
const activeTab = ref<StorageConfigTab>(normalizeTab(route.query.tab));

const activeComponent = computed(() => ({
  nodes: PrimaryStoreNodes,
  routes: PrimaryStoreRoutes,
  archive: ArchiveFiles,
}[activeTab.value]));

function normalizeTab(value: unknown): StorageConfigTab {
  return value === 'routes' || value === 'archive' ? value : 'nodes';
}

function onTabChange(value: string | number) {
  const tab = normalizeTab(value);
  activeTab.value = tab;
  void router.replace({ query: { ...route.query, tab } });
}

watch(() => route.query.tab, (value) => {
  const tab = normalizeTab(value);
  if (tab !== activeTab.value) activeTab.value = tab;
});
</script>

<style scoped lang="scss">
.storage-config-page {
  height: 100%;
  min-height: 0;
}

.storage-config-page > .moox-inner {
  display: flex;
  min-height: 100%;
  flex-direction: column;
}

.storage-config-content {
  min-height: 0;
  flex: 1;
  margin-top: 12px;
  overflow: hidden;
}

.storage-config-content :deep(.moox-page) {
  height: 100%;
  min-height: 0;
  padding: 0;
  overflow: auto;
  background: transparent;
}

.storage-config-content :deep(.moox-page > .moox-inner) {
  min-height: 0;
  padding: 0;
  border: 0;
  border-radius: 0;
  box-shadow: none;
}
</style>
