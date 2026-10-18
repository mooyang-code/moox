<template>
  <div class="moox-page strategy-workbench-page">
    <div class="moox-inner">
      <PageTitleTabs :model-value="activeTab" :items="tabs" aria-label="策略运行" @change="onTabChange" />

      <section class="workbench-content">
        <!-- 不用 keep-alive：各 Tab 都按 URL 读写自己的状态并有轮询，切走即卸载，避免后台响应其它 Tab 的 URL。 -->
        <component :is="activeComponent" :key="activeTab" />
      </section>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, defineAsyncComponent, h, type Component } from "vue";
import { useRoute, useRouter } from "vue-router";
import PageTitleTabs from "@/components/page-title-tabs/index.vue";
import { STRATEGY_TASKS_ROUTE, normalizeTaskTab, type StrategyTaskTab } from "@/views/strategy/links";

defineOptions({ name: "StrategyWorkbench" });

// 各 Tab 按需加载：回放依赖体积很大的图表库，只有打开「策略回放」时才下载，其余 Tab 不被它拖慢。
const tabLoading = { render: () => h("div", { class: "workbench-loading" }, [h("span", "正在加载…")]) };
const lazyTab = (loader: () => Promise<{ default: Component }>) =>
  defineAsyncComponent({ loader: () => loader().then(module => module.default), loadingComponent: tabLoading, delay: 200 });
const StrategyRunning = lazyTab(() => import("@/views/strategy/running/index.vue"));
const StrategyResults = lazyTab(() => import("@/views/strategy/results/index.vue"));
const StrategyReplay = lazyTab(() => import("@/views/strategy/replay/index.vue"));

const tabs = [
  { key: "instances", label: "运行实例" },
  { key: "results", label: "运行结果" },
  { key: "replay", label: "策略回放" }
] as const;

const route = useRoute();
const router = useRouter();
// 当前 Tab 只由 URL 决定：点击时只改 URL；切换 Tab 时清掉上一个 Tab 的 query，各 Tab 的参数互不串用。
const activeTab = computed<StrategyTaskTab>(() => normalizeTaskTab(route.query.tab));

const activeComponent = computed(
  () =>
    ({
      instances: StrategyRunning,
      results: StrategyResults,
      replay: StrategyReplay
    })[activeTab.value]
);

function onTabChange(value: string | number) {
  const tab = normalizeTaskTab(value);
  void router.replace({ name: STRATEGY_TASKS_ROUTE, query: tab === "instances" ? {} : { tab } });
}
</script>

<style scoped lang="scss">
.strategy-workbench-page {
  height: 100%;
  min-height: 0;
}

.strategy-workbench-page > .moox-inner {
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
