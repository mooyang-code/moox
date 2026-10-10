<template>
  <div class="moox-page deployments-page">
    <div class="moox-inner">
      <PageTitleTabs :model-value="activeTab" :items="tabs" aria-label="服务部署" @change="onTabChange" />

      <section class="deployments-content">
        <keep-alive>
          <ServicesTab v-if="activeTab === 'services'" :focus="focusKey" @open="openComponent" @loaded="onServicesLoaded" />
          <RoutesTab v-else :initial-host="queryText(route.query.host)" />
        </keep-alive>
      </section>
    </div>

    <ComponentDrawer v-model:visible="drawerVisible" :row="selectedRow" />
  </div>
</template>

<script setup lang="ts">
import { computed, ref, watch } from "vue";
import { useRoute, useRouter } from "vue-router";
import PageTitleTabs from "@/components/page-title-tabs/index.vue";
import ComponentDrawer from "./component-drawer.vue";
import RoutesTab from "./routes-tab.vue";
import ServicesTab from "./services-tab.vue";
import type { DeploymentRow } from "./deployment-view";

type DeploymentsTab = "services" | "routes";
const tabs = [
  { key: "services", label: "服务" },
  { key: "routes", label: "网关路由" }
] as const;

const route = useRoute();
const router = useRouter();
const activeTab = ref<DeploymentsTab>(normalizeTab(route.query.tab));
const drawerVisible = ref(false);
const selectedRow = ref<DeploymentRow | null>(null);
// 从监控告警「定位」过来时带 host 和 component：高亮这一行并打开详情。
const focusKey = computed(() => {
  const host = queryText(route.query.host);
  const component = queryText(route.query.component);
  return host && component ? `${host}:${component}` : "";
});
let focusedOnce = "";

function queryText(value: unknown) {
  return typeof value === "string" ? value : "";
}

function normalizeTab(value: unknown): DeploymentsTab {
  return value === "routes" ? value : "services";
}

function onTabChange(value: string | number) {
  const tab = normalizeTab(value);
  activeTab.value = tab;
  void router.replace({ query: { ...route.query, tab } });
}

function openComponent(row: DeploymentRow) {
  selectedRow.value = row;
  drawerVisible.value = true;
}

function onServicesLoaded(rows: DeploymentRow[]) {
  if (selectedRow.value) selectedRow.value = rows.find(row => row.key === selectedRow.value?.key) || selectedRow.value;
  if (!focusKey.value || focusedOnce === focusKey.value) return;
  const row = rows.find(item => item.key === focusKey.value);
  if (row) {
    focusedOnce = focusKey.value;
    openComponent(row);
  }
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
.deployments-page {
  height: 100%;
  overflow: auto;
}

.deployments-content {
  margin-top: var(--moox-space-3);
}
</style>
