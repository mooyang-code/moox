<template>
  <div class="collector-workbench">
    <ViewDefinitions
      v-if="activeTab === 'definitions'"
      owner-module="collector"
      view-role="collection_browse"
      managed-by="manual"
      :filter-owner-modules="['collector']"
      :filter-dataset-roles="['raw_collection', 'import']"
      :filter-view-roles="['collection_browse', 'analysis']"
      :include-unowned="true"
      :excluded-primary-dataset-ids="excludedFactorDatasetIds"
      :exclude-likely-factor-datasets="true"
    >
      <template #page-title>
        <PageTitleTabs :model-value="activeTab" :items="tabs" aria-label="数据视图" @change="syncRoute" />
      </template>
    </ViewDefinitions>
    <ViewBrowse
      v-else
      empty-description="暂无数据视图"
      :view-owner-modules="['collector']"
      :view-roles="['collection_browse', 'analysis']"
      :include-unowned="true"
      :excluded-primary-dataset-ids="excludedFactorDatasetIds"
      :exclude-likely-factor-datasets="true"
    >
      <template #page-title>
        <PageTitleTabs :model-value="activeTab" :items="tabs" aria-label="数据视图" @change="syncRoute" />
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

defineOptions({ name: 'CollectorViews' });

const props = withDefaults(
  defineProps<{
    queryKey?: string;
    routePath?: string;
  }>(),
  {
    queryKey: 'tab',
    routePath: '/collector/views',
  },
);

const route = useRoute();
const router = useRouter();
const spaceStore = useSpaceStore();
const selectedSpaceId = computed(() => spaceStore.selectedSpaceId);
const activeTab = ref(tabFromRoute());
const bindings = ref<FactorBinding[]>([]);
type CollectorViewTab = 'definitions' | 'browse';

const tabs = [
  { key: 'definitions', label: '视图定义' },
  { key: 'browse', label: '查看数据' },
] as const;

const normalizedQuery = computed(() => String(route.query[props.queryKey] || ''));
const excludedFactorDatasetIds = computed(() => factorBindingTargetDatasetIds(bindings.value));

function tabFromRoute() {
  return route.query[props.queryKey] === 'browse' ? 'browse' : 'definitions';
}

function syncRoute(key: string | number) {
  const tab: CollectorViewTab = key === 'browse' ? 'browse' : 'definitions';
  activeTab.value = tab;
  const query = { ...route.query };
  if (tab === 'browse') query[props.queryKey] = tab;
  else delete query[props.queryKey];
  void router.replace({ path: props.routePath, query });
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

</style>
