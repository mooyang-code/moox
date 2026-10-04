<template>
  <div class="moox-page factor-workbench">
    <div class="moox-inner">
      <a-alert v-if="!spaceId" type="warning" show-icon>请先在顶部选择空间</a-alert>
      <div v-else class="workbench">
        <aside class="workbench__sets">
          <SetList @create="createVisible = true" @select="selectSet" />
        </aside>
        <section class="workbench__detail">
          <a-alert v-if="store.loadError" type="error" show-icon class="workbench__error">
            {{ store.loadError }}
            <template #action><a-button size="mini" @click="reload">重试</a-button></template>
          </a-alert>
          <a-empty v-if="!store.current" :description="store.loading ? '加载中…' : '请选择或新建一个因子集'" />
          <template v-else>
            <SetHeader />
            <a-tabs :active-key="tab" lazy-load @change="changeTab">
              <a-tab-pane key="overview" title="概览" />
              <a-tab-pane key="factors" title="因子" />
              <a-tab-pane key="recalc" title="补算" />
              <a-tab-pane key="results" title="结果" />
            </a-tabs>
            <div class="workbench__body">
              <OverviewTab v-if="tab === 'overview'" />
              <FactorsTab v-else-if="tab === 'factors'" />
              <RecalcTab v-else-if="tab === 'recalc'" :key="store.currentSetId" />
              <ResultsTab v-else :key="store.currentSetId" />
            </div>
          </template>
        </section>
      </div>
    </div>
    <SetCreateModal v-model:visible="createVisible" @created="onCreated" />
  </div>
</template>

<script setup lang="ts">
import { computed, ref, watch } from "vue";
import { useRoute, useRouter } from "vue-router";
import { usePolling } from "@/hooks/usePolling";
import { useFactorStore } from "@/store/modules/factor";
import { useSpaceStore } from "@/store/modules/space";
import SetCreateModal from "./set-create-modal.vue";
import SetHeader from "./set-header.vue";
import SetList from "./set-list.vue";
import FactorsTab from "./tabs/factors.vue";
import OverviewTab from "./tabs/overview.vue";
import RecalcTab from "./tabs/recalc.vue";
import ResultsTab from "./tabs/results.vue";

defineOptions({ name: "FactorWorkbench" });

const ROUTE_NAME = "factor-workbench";
const TABS = ["overview", "factors", "recalc", "results"] as const;
type TabKey = (typeof TABS)[number];

const route = useRoute();
const router = useRouter();
const store = useFactorStore();
const spaceStore = useSpaceStore();
const createVisible = ref(false);

const spaceId = computed(() => spaceStore.selectedSpaceId);
const tab = computed<TabKey>(() => {
  const value = String(route.query.tab || "");
  return (TABS as readonly string[]).includes(value) ? (value as TabKey) : "overview";
});

function navigate(query: Record<string, string | undefined>) {
  const next: Record<string, string> = {};
  for (const [key, value] of Object.entries({ ...route.query, ...query })) {
    if (value !== undefined && value !== "") next[key] = String(value);
  }
  return router.replace({ path: route.path, query: next });
}

function selectSet(setId: string) {
  store.select(setId);
  void navigate({ set: setId, job: undefined });
}

function changeTab(key: string | number) {
  void navigate({ tab: String(key), job: undefined });
}

async function onCreated(setId: string) {
  await store.reload({ silent: false });
  selectSet(setId);
}

async function reload() {
  await store.load(spaceId.value);
  syncSelection();
}

function syncSelection() {
  if (route.name !== ROUTE_NAME) return;
  store.select(String(route.query.set || ""));
  if (store.currentSetId !== String(route.query.set || "")) void navigate({ set: store.currentSetId || undefined });
}

watch(
  spaceId,
  async () => {
    store.reset();
    await store.load(spaceId.value);
    void store.refreshEngine();
    syncSelection();
  },
  { immediate: true }
);

watch(
  () => route.query.set,
  () => {
    if (route.name === ROUTE_NAME) store.select(String(route.query.set || ""));
  }
);

watch(
  () => store.currentSetId,
  () => {
    if (route.name === ROUTE_NAME && store.currentSetId !== String(route.query.set || ""))
      void navigate({ set: store.currentSetId || undefined });
  }
);

usePolling(
  () => Promise.all([store.reload(), store.refreshEngine()]),
  10_000,
  () => Boolean(spaceId.value)
);
</script>

<style scoped>
.factor-workbench,
.factor-workbench > .moox-inner {
  min-height: 0;
}

.workbench {
  display: grid;
  grid-template-columns: 280px minmax(0, 1fr);
  gap: var(--moox-space-4);
  min-height: calc(100vh - 160px);
}

.workbench__sets {
  min-height: 0;
  padding-right: var(--moox-space-4);
  border-right: 1px solid var(--color-border-2);
}

.workbench__detail {
  min-width: 0;
}

.workbench__error {
  margin-bottom: var(--moox-space-3);
}

.workbench__body {
  padding-top: var(--moox-space-3);
}

@media (max-width: 900px) {
  .workbench {
    grid-template-columns: minmax(0, 1fr);
  }

  .workbench__sets {
    padding-right: 0;
    padding-bottom: var(--moox-space-3);
    border-right: 0;
    border-bottom: 1px solid var(--color-border-2);
  }
}
</style>
