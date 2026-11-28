<template>
  <div class="moox-page title-tabs-workbench">
    <div class="moox-inner">
      <PageTitleTabs :model-value="activeTab" :items="tabItems" :aria-label="label" @change="setTab" />

      <section class="workbench-content">
        <!-- 不用 keep-alive：各页签按 URL 读写自己的状态并有轮询，切走即卸载，避免后台响应其他页签的 URL。 -->
        <component :is="activeComponent" :key="activeTab" />
      </section>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, type Component } from "vue";
import type { LocationQuery, LocationQueryRaw } from "vue-router";
import PageTitleTabs from "@/components/page-title-tabs/index.vue";
import { useQueryTab } from "@/hooks/useQueryTab";

export interface WorkbenchTab<T extends string = string> {
  key: T;
  label: string;
  component: Component;
}

defineOptions({ name: "TitleTabsWorkbench" });

const props = defineProps<{
  tabs: readonly WorkbenchTab[];
  /** 页签栏的可访问名称（传给 PageTitleTabs 的 aria-label）。 */
  label: string;
  /** URL 参数名，默认 "tab"。 */
  param?: string;
  /** 路由写入目标（按名称或路径）；省略时使用当前 route.path。 */
  routeTarget?: { name: string } | { path: string };
  /** 切换页签时构造完整 query；省略时只写 tab 并清空其他参数。 */
  buildQuery?: (tab: string, currentQuery: LocationQuery) => LocationQueryRaw;
}>();

const tabKeys = props.tabs.map(item => item.key);
const tabItems = computed(() => props.tabs.map(({ key, label }) => ({ key, label })));
const componentMap = computed(() => Object.fromEntries(props.tabs.map(item => [item.key, item.component])));

const { activeTab, setTab } = useQueryTab({
  tabs: tabKeys,
  param: props.param,
  target: props.routeTarget,
  buildQuery: props.buildQuery
});

const activeComponent = computed(() => componentMap.value[activeTab.value] ?? props.tabs[0]?.component);
</script>

<style scoped lang="scss">
.title-tabs-workbench {
  height: 100%;
  min-height: 0;
}

.title-tabs-workbench > .moox-inner {
  display: flex;
  min-height: 100%;
  flex-direction: column;
}

.workbench-content {
  min-width: 0;
  min-height: 0;
  flex: 1;
  margin-top: var(--moox-space-3);
  overflow-x: hidden;
  overflow-y: auto;
}

.workbench-content :deep(.workbench-loading) {
  display: flex;
  justify-content: center;
  padding: 48px 0;
  color: var(--color-text-3);
}

.workbench-content :deep(.moox-page) {
  height: auto;
  min-height: 100%;
  max-width: 100%;
  padding: 0;
  overflow-x: hidden;
  overflow-y: visible;
  background: transparent;
}

.workbench-content :deep(.moox-inner) {
  min-height: 0;
  padding: 0;
  border: 0;
  border-radius: 0;
  box-shadow: none;
}
</style>
