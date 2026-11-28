<template>
  <TitleTabsWorkbench :tabs="tabs" label="策略运行" :route-target="{ name: STRATEGY_TASKS_ROUTE }" />
</template>

<script setup lang="ts">
import { defineAsyncComponent, h, type Component } from "vue";
import TitleTabsWorkbench, { type WorkbenchTab } from "@/components/page-title-tabs/workbench.vue";
import { STRATEGY_TASKS_ROUTE, type StrategyTaskTab } from "@/views/strategy/links";

defineOptions({ name: "StrategyWorkbench" });

// 各 Tab 按需加载：回放依赖体积很大的图表库，只有打开「策略回放」时才下载，其余 Tab 不被它拖慢。
const tabLoading = { render: () => h("div", { class: "workbench-loading" }, [h("span", "正在加载…")]) };
const lazyTab = (loader: () => Promise<{ default: Component }>) =>
  defineAsyncComponent({ loader: () => loader().then(module => module.default), loadingComponent: tabLoading, delay: 200 });

const tabs: readonly WorkbenchTab<StrategyTaskTab>[] = [
  { key: "instances", label: "运行实例", component: lazyTab(() => import("@/views/strategy/running/index.vue")) },
  { key: "replay", label: "策略回放", component: lazyTab(() => import("@/views/strategy/replay/index.vue")) }
];
</script>
