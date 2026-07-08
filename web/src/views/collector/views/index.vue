<template>
  <div class="collector-workbench">
    <a-tabs v-model:active-key="activeTab" type="rounded" size="medium" @change="syncRoute">
      <a-tab-pane key="definitions" title="视图定义">
        <ViewDefinitions
          owner-module="collector"
          view-role="collection_browse"
          managed-by="manual"
          :filter-owner-modules="['collector']"
          :filter-dataset-roles="['raw_collection', 'import']"
          :filter-view-roles="['collection_browse', 'analysis']"
          :include-unowned="true"
          :excluded-primary-dataset-ids="excludedFactorDatasetIds"
          :exclude-likely-factor-datasets="true"
        />
      </a-tab-pane>
      <a-tab-pane key="browse" title="查看数据">
        <ViewBrowse
          page-title="数据视图"
          empty-description="暂无数据视图"
          :view-owner-modules="['collector']"
          :view-roles="['collection_browse', 'analysis']"
          :include-unowned="true"
          :excluded-primary-dataset-ids="excludedFactorDatasetIds"
          :exclude-likely-factor-datasets="true"
        />
      </a-tab-pane>
    </a-tabs>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue';
import { useRoute, useRouter } from 'vue-router';
import { listFactorBindings } from '@/api/factor';
import type { FactorBinding } from '@/api/factor/types';
import { useSpaceStore } from '@/store/modules/space';
import { factorBindingTargetDatasetIds } from '@/views/data/shared/factor-result-dataset';
import ViewDefinitions from '@/views/data/views/index.vue';
import ViewBrowse from '@/views/data/view-browse/index.vue';

defineOptions({ name: 'CollectorViews' });

const route = useRoute();
const router = useRouter();
const spaceStore = useSpaceStore();
const selectedSpaceId = computed(() => spaceStore.selectedSpaceId);
const activeTab = ref(tabFromRoute());
const bindings = ref<FactorBinding[]>([]);

const normalizedQuery = computed(() => String(route.query.tab || ''));
const excludedFactorDatasetIds = computed(() => factorBindingTargetDatasetIds(bindings.value));

function tabFromRoute() {
  return route.query.tab === 'browse' ? 'browse' : 'definitions';
}

function syncRoute(key: string | number) {
  const tab = key === 'browse' ? 'browse' : 'definitions';
  router.replace({ path: '/collector/views', query: tab === 'browse' ? { tab } : {} });
}

async function loadBindings() {
  if (!selectedSpaceId.value) {
    bindings.value = [];
    return;
  }
  try {
    bindings.value = await listAllBindings(selectedSpaceId.value);
  } catch {
    bindings.value = [];
  }
}

async function listAllBindings(spaceId: string) {
  const items: FactorBinding[] = [];
  const size = 500;
  for (let pageNo = 1; ; pageNo += 1) {
    const rsp = await listFactorBindings({
      space_id: spaceId,
      status: 'enabled',
      page: { page: pageNo, size },
    });
    items.push(...(rsp.bindings || []));
    if (!rsp.page_result?.has_more || (rsp.bindings || []).length === 0) {
      return items;
    }
  }
}

watch(normalizedQuery, () => {
  activeTab.value = tabFromRoute();
});
watch(selectedSpaceId, loadBindings);
onMounted(loadBindings);
</script>

<style scoped>
.collector-workbench {
  display: flex;
  flex-direction: column;
  height: 100%;
  min-height: 0;
  overflow: hidden;
}

.collector-workbench :deep(.arco-tabs) {
  display: flex;
  flex: 1;
  flex-direction: column;
  min-height: 0;
}

.collector-workbench :deep(.arco-tabs-content) {
  flex: 1;
  min-height: 0;
  padding-top: 0;
  overflow: hidden;
}

.collector-workbench :deep(.arco-tabs-content-list) {
  height: 100%;
  min-height: 0;
  overflow: hidden;
}

.collector-workbench :deep(.arco-tabs-pane) {
  height: 100%;
  min-height: 0;
  overflow-x: hidden;
  overflow-y: auto;
}
</style>
