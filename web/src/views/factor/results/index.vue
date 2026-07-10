<template>
  <div class="factor-results-workbench">
    <ViewDefinitions
      v-if="activeTab === 'definitions'"
      owner-module="factor"
      view-role="factor_result"
      managed-by="factor"
      :filter-owner-modules="['factor']"
      :filter-dataset-roles="['factor_result']"
      :filter-view-roles="['factor_result']"
      :allowed-primary-dataset-ids="targetDatasetIds"
    >
      <template #page-title>
        <PageTitleTabs :model-value="activeTab" :items="tabs" aria-label="因子结果" @change="syncRoute" />
      </template>
    </ViewDefinitions>
    <ViewBrowse
      v-else
      empty-description="暂无因子结果视图，请先在“结果视图”中创建一个结果视图"
      :allowed-primary-dataset-ids="targetDatasetIds"
      :view-owner-modules="['factor']"
      :view-roles="['factor_result']"
    >
      <template #page-title>
        <PageTitleTabs :model-value="activeTab" :items="tabs" aria-label="因子结果" @change="syncRoute" />
      </template>
    </ViewBrowse>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue';
import { useRoute, useRouter } from 'vue-router';
import PageTitleTabs from '@/components/page-title-tabs/index.vue';
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
type FactorResultTab = 'definitions' | 'browse';

const tabs = [
  { key: 'definitions', label: '结果视图' },
  { key: 'browse', label: '查看结果' },
] as const;
const normalizedQuery = computed(() => String(route.query.tab || ''));

const targetDatasetIds = computed(() => factorBindingTargetDatasetIds(bindings.value));

function tabFromRoute() {
  return route.query.tab === 'definitions' ? 'definitions' : 'browse';
}

function syncRoute(key: string | number) {
  const tab: FactorResultTab = key === 'definitions' ? 'definitions' : 'browse';
  activeTab.value = tab;
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
</style>
