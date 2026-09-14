<template>
  <div class="moox-page data-management-page">
    <div class="moox-inner">
      <PageTitleTabs :model-value="activeTab" :items="tabs" aria-label="基础数据集" />

      <section class="management-content">
        <CollectorDatasets />
      </section>
    </div>
  </div>
</template>

<script setup lang="ts">
import PageTitleTabs from "@/components/page-title-tabs/index.vue";
import CollectorDatasets from "@/views/collector/datasets/index.vue";

defineOptions({ name: "CollectorDataManagement" });

const tabs = [{ key: "datasets", label: "基础数据集" }] as const;
const activeTab = "datasets";
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
