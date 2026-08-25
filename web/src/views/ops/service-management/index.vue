<template>
  <div class="moox-page service-management-page">
    <div class="moox-inner">
      <PageTitleTabs :model-value="activeTab" :items="tabs" aria-label="服务管理" @change="onTabChange" />

      <section class="management-content">
        <keep-alive>
          <component :is="activeComponent" :embedded="true" />
        </keep-alive>
      </section>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, ref, watch } from "vue";
import { useRoute, useRouter } from "vue-router";
import PageTitleTabs from "@/components/page-title-tabs/index.vue";
import GatewayNodes from "./gateway-nodes.vue";
import ServiceDeployments from "@/views/settings/service-deployments/index.vue";
import HealthMonitor from "@/views/ops/health-monitor/index.vue";

type ServiceManagementTab = "health" | "nodes" | "instances";
const tabs = [
  { key: "nodes", label: "网关节点" },
  { key: "instances", label: "服务实例" },
  { key: "health", label: "健康监控" }
] as const;

const route = useRoute();
const router = useRouter();
const activeTab = ref<ServiceManagementTab>(normalizeTab(route.query.tab));
if (route.query.tab !== undefined && normalizeTab(route.query.tab) !== route.query.tab) {
  void router.replace({ query: { ...route.query, tab: activeTab.value } });
}

const activeComponent = computed(
  () =>
    ({
      health: HealthMonitor,
      nodes: GatewayNodes,
      instances: ServiceDeployments
    })[activeTab.value]
);

function normalizeTab(value: unknown): ServiceManagementTab {
  return value === "nodes" || value === "instances" || value === "health" ? value : "health";
}

function onTabChange(value: string | number) {
  const tab = normalizeTab(value);
  activeTab.value = tab;
  void router.replace({ query: { ...route.query, tab } });
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
.service-management-page {
  height: 100%;
  min-height: 0;
}

.service-management-page > .moox-inner {
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

.management-content :deep(.monitor-page) {
  padding: 0;
  background: transparent;
}

.management-content :deep(.moox-page),
.management-content :deep(.monitor-page) {
  height: 100%;
  min-height: 0;
}
</style>
