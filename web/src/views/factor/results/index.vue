<template>
  <div class="factor-results-workbench">
    <a-tabs v-model:active-key="activeTab" type="rounded" size="medium" @change="syncRoute">
      <a-tab-pane key="definitions" title="结果视图">
        <ViewDefinitions
          owner-module="factor"
          view-role="factor_result"
          managed-by="factor"
          :filter-owner-modules="['factor']"
          :filter-dataset-roles="['factor_result']"
          :filter-view-roles="['factor_result']"
          :allowed-primary-dataset-ids="targetDatasetIds"
        />
      </a-tab-pane>
      <a-tab-pane key="browse" title="查看结果">
        <ViewBrowse
          page-title="因子结果"
          empty-description="暂无因子结果视图，请先在“结果视图”中创建一个结果视图"
          :allowed-primary-dataset-ids="targetDatasetIds"
          :view-owner-modules="['factor']"
          :view-roles="['factor_result']"
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

defineOptions({ name: 'FactorResults' });

const route = useRoute();
const router = useRouter();
const spaceStore = useSpaceStore();
const selectedSpaceId = computed(() => spaceStore.selectedSpaceId);
const bindings = ref<FactorBinding[]>([]);
const activeTab = ref(tabFromRoute());
const normalizedQuery = computed(() => String(route.query.tab || ''));

const targetDatasetIds = computed(() => factorBindingTargetDatasetIds(bindings.value));

function tabFromRoute() {
  return route.query.tab === 'definitions' ? 'definitions' : 'browse';
}

function syncRoute(key: string | number) {
  const tab = key === 'definitions' ? 'definitions' : 'browse';
  router.replace({ path: '/factor/results', query: tab === 'definitions' ? { tab } : {} });
}

async function loadBindings() {
  if (!selectedSpaceId.value) {
    bindings.value = [];
    return;
  }
  bindings.value = await listAllBindings(selectedSpaceId.value);
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

watch(selectedSpaceId, loadBindings);
watch(normalizedQuery, () => {
  activeTab.value = tabFromRoute();
});
onMounted(loadBindings);
</script>

<style scoped>
.factor-results-workbench {
  display: flex;
  flex-direction: column;
  height: 100%;
  min-height: 0;
  overflow: hidden;
}

.factor-results-workbench :deep(.arco-tabs) {
  display: flex;
  flex: 1;
  flex-direction: column;
  min-height: 0;
}

.factor-results-workbench :deep(.arco-tabs-content) {
  flex: 1;
  min-height: 0;
  padding-top: 0;
  overflow: hidden;
}

.factor-results-workbench :deep(.arco-tabs-content-list) {
  height: 100%;
  min-height: 0;
  overflow: hidden;
}

.factor-results-workbench :deep(.arco-tabs-pane) {
  height: 100%;
  min-height: 0;
  overflow-x: hidden;
  overflow-y: auto;
}
</style>
