<template>
  <div class="moox-page factor-results-workbench">
    <div class="moox-inner">
      <PageTitleTabs :model-value="activeTab" :items="tabs" aria-label="因子结果" @change="syncRoute" />
      <section class="factor-results-content">
        <ViewDefinitions
          v-if="activeTab === 'definitions'"
          :embedded="true"
          owner-module="factor"
          view-role="factor_result"
          managed-by="factor"
          :filter-owner-modules="['factor']"
          :filter-dataset-roles="['factor_result']"
          :filter-view-roles="['factor_result']"
          :allowed-primary-dataset-ids="targetDatasetIds"
        />
        <ViewBrowse
          v-else
          :embedded="true"
          empty-description="暂无因子结果视图，请先在“结果视图”中创建一个结果视图"
          :allowed-primary-dataset-ids="targetDatasetIds"
          :view-owner-modules="['factor']"
          :view-roles="['factor_result']"
          :auto-refresh-interval-ms="60000"
        >
          <template #status-extra>
            <span class="engine-status">
              <span>Python Workers {{ engineStatus.python_workers }}</span>
              <span>执行中 {{ engineStatus.active_tasks }}</span>
              <span>等待中 {{ engineStatus.pending_tasks }}</span>
            </span>
          </template>
        </ViewBrowse>
      </section>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from "vue";
import { useRoute, useRouter } from "vue-router";
import PageTitleTabs from "@/components/page-title-tabs/index.vue";
import { getEngineStatus, listFactorBindings } from "@/api/factor";
import type { EngineStatus, FactorBinding } from "@/api/factor/types";
import { useSpaceStore } from "@/store/modules/space";
import { factorBindingTargetDatasetIds } from "@/views/data/shared/factor-result-dataset";
import ViewDefinitions from "@/views/data/views/index.vue";
import ViewBrowse from "@/views/data/view-browse/index.vue";

defineOptions({ name: "FactorResults" });

const route = useRoute();
const router = useRouter();
const spaceStore = useSpaceStore();
const selectedSpaceId = computed(() => spaceStore.selectedSpaceId);
const bindings = ref<FactorBinding[]>([]);
const engineStatus = ref<EngineStatus>({
  ret_info: { code: 0, msg: "" },
  python_workers: 0,
  active_tasks: 0,
  pending_tasks: 0
});
const activeTab = ref(tabFromRoute());
const spaceResolved = ref(false);
let engineStatusTimer: number | undefined;
type FactorResultTab = "definitions" | "browse";

const tabs = [
  { key: "definitions", label: "结果视图" },
  { key: "browse", label: "查看结果" }
] as const;
const normalizedQuery = computed(() => String(route.query.tab || ""));

const targetDatasetIds = computed(() => factorBindingTargetDatasetIds(bindings.value));

function tabFromRoute() {
  return route.query.tab === "definitions" ? "definitions" : "browse";
}

function syncRoute(key: string | number) {
  const tab: FactorResultTab = key === "definitions" ? "definitions" : "browse";
  activeTab.value = tab;
  router.replace({ path: "/factor/results", query: tab === "definitions" ? { tab } : {} });
}

async function loadBindings() {
  if (!selectedSpaceId.value) {
    bindings.value = [];
    return;
  }
  // The layout loads the space list in parallel with this page. Do not mark
  // the lookup resolved until that list is available, otherwise a persisted
  // space ID could suppress the cross-space fallback on the first render.
  if (spaceStore.spaces.length === 0) {
    return;
  }
  const currentSpaceId = selectedSpaceId.value;
  const currentBindings = await listAllBindings(currentSpaceId);
  if (currentBindings.length > 0 || spaceResolved.value) {
    bindings.value = currentBindings;
    spaceResolved.value = true;
    return;
  }

  // The global space selector defaults to the first business space (usually
  // stockcn). Factor result views are scoped by space, so a default factor
  // binding in another space would otherwise look like "no result view".
  // Resolve that once on entry, while still allowing the user to switch back
  // manually after the page has loaded.
  for (const space of spaceStore.spaces) {
    if (space.space_id === currentSpaceId) {
      continue;
    }
    const candidateBindings = await listAllBindings(space.space_id);
    if (candidateBindings.length === 0) {
      continue;
    }
    spaceResolved.value = true;
    spaceStore.setSelectedSpace(space.space_id);
    return;
  }
  bindings.value = currentBindings;
  spaceResolved.value = true;
}

async function loadEngineStatus() {
  engineStatus.value = await getEngineStatus();
}

async function listAllBindings(spaceId: string) {
  const items: FactorBinding[] = [];
  const size = 500;
  for (let pageNo = 1; ; pageNo += 1) {
    const rsp = await listFactorBindings({
      space_id: spaceId,
      status: "enabled",
      page: { page: pageNo, size }
    });
    items.push(...(rsp.bindings || []));
    if (!rsp.page_result?.has_more || (rsp.bindings || []).length === 0) {
      return items;
    }
  }
}

watch(selectedSpaceId, loadBindings);
watch(
  () => spaceStore.spaces.length,
  () => {
    if (!spaceResolved.value) {
      loadBindings();
    }
  }
);
watch(normalizedQuery, () => {
  activeTab.value = tabFromRoute();
});
onMounted(() => {
  loadBindings();
  loadEngineStatus();
  engineStatusTimer = window.setInterval(loadEngineStatus, 5000);
});
onBeforeUnmount(() => {
  if (engineStatusTimer !== undefined) {
    window.clearInterval(engineStatusTimer);
  }
});
</script>

<style scoped>
.factor-results-workbench {
  display: flex;
  flex-direction: column;
  height: 100%;
  min-height: 0;
  overflow: hidden;
}

.factor-results-workbench > .moox-inner {
  display: flex;
  height: 100%;
  min-height: 0;
  flex-direction: column;
}
.factor-results-content {
  min-height: 0;
  flex: 1;
  margin-top: var(--moox-space-3);
  overflow: hidden;
}
.engine-status {
  display: inline-flex;
  gap: var(--moox-space-5);
  color: var(--color-text-2);
  font-size: 13px;
  line-height: 20px;
}
.factor-results-content :deep(.moox-page) {
  height: 100%;
  min-height: 0;
  padding: 0;
  overflow: auto;
  background: transparent;
}
.factor-results-content :deep(.moox-page > .moox-inner) {
  min-height: 0;
  padding: 0;
  border: 0;
  border-radius: 0;
  box-shadow: none;
}
</style>
