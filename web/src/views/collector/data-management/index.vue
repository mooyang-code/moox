<template>
  <div class="moox-page data-management-page">
    <div class="moox-inner">
      <PageTitleTabs :model-value="activeTab" :items="tabs" aria-label="数据管理" @change="onTabChange" />

      <section class="management-content">
        <keep-alive>
          <CollectorViews v-if="activeTab === 'views'" />
          <CollectorDatasets v-else />
        </keep-alive>
      </section>
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, watch } from "vue";
import { useRoute, useRouter } from "vue-router";
import PageTitleTabs from "@/components/page-title-tabs/index.vue";
import CollectorDatasets from "@/views/collector/datasets/index.vue";
import CollectorViews from "@/views/collector/views/index.vue";

defineOptions({ name: "CollectorDataManagement" });

type DataManagementTab = "views" | "datasets";

const tabs = [
  { key: "views", label: "数据视图" },
  { key: "datasets", label: "数据集合" }
] as const;

const route = useRoute();
const router = useRouter();
const activeTab = ref<DataManagementTab>(normalizeTab(route.query.tab));

function normalizeTab(value: unknown): DataManagementTab {
  return value === "datasets" ? "datasets" : "views";
}

function onTabChange(value: string | number) {
  const tab = normalizeTab(value);
  activeTab.value = tab;
  const query = { ...route.query, tab };
  if (tab === "views") delete query.datasetTab;
  else delete query.viewTab;
  void router.replace({ path: "/collector/data-management", query });
}

watch(
  () => route.query.tab,
  value => {
    const tab = normalizeTab(value);
    if (tab !== activeTab.value) activeTab.value = tab;
  }
);
</script>

<style scoped lang="scss">
.data-management-page {
  height: 100%;
  min-height: 0;
}

.data-management-page > .moox-inner {
  display: flex;
  min-height: 100%;
  flex-direction: column;
}

.management-content {
  min-height: 0;
  flex: 1;
  margin-top: var(--moox-space-3);
  overflow: hidden;
}

.management-content :deep(.page-head) {
  margin-bottom: var(--moox-space-2);
}

.management-content :deep(.collector-subtabs .arco-tabs-content) {
  display: none;
}

.management-content :deep(.collector-subtabs .arco-tabs-tab:first-child) {
  margin-left: 0;
}

.management-content :deep(.collector-subtabs .arco-tabs-tab) {
  border-radius: 4px;
}

.management-content :deep(.collector-subtabs .arco-tabs-tab-active) {
  color: rgb(var(--primary-6));
  background-color: var(--color-fill-2);
}

.management-content :deep(.collector-workbench),
.management-content :deep(.moox-page),
.management-content :deep(.data-browse-page),
.management-content :deep(.view-browse-page) {
  height: 100%;
  min-height: 0;
}

.management-content :deep(.moox-page) {
  padding: 0;
  background: transparent;
}

.management-content :deep(.moox-page > .moox-inner) {
  padding: 0;
  border: 0;
  border-radius: 0;
  box-shadow: none;
}
</style>
