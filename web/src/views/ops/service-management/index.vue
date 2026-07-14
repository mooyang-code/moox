<template>
  <div class="moox-page service-management-page">
    <div class="moox-inner">
      <header class="page-head">
        <div>
          <h2>服务管理</h2>
          <p>统一维护服务部署、可用性探测和应用指标。</p>
        </div>
      </header>

      <a-tabs v-model:active-key="activeTab" class="management-tabs" type="rounded" @change="onTabChange">
        <a-tab-pane key="deployments" title="服务部署" />
        <a-tab-pane key="availability" title="可用性监控" />
        <a-tab-pane key="metrics" title="应用指标" />
      </a-tabs>

      <section class="management-content">
        <keep-alive>
          <component :is="activeComponent" :embedded="true" />
        </keep-alive>
      </section>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue';
import { useRoute, useRouter } from 'vue-router';
import ServiceDeployments from '@/views/settings/service-deployments/index.vue';
import ServiceMonitor from '@/views/ops/service-monitor/index.vue';
import MetricMonitor from '@/views/ops/metric-monitor/index.vue';

type ServiceManagementTab = 'deployments' | 'availability' | 'metrics';

const route = useRoute();
const router = useRouter();
const activeTab = ref<ServiceManagementTab>(normalizeTab(route.query.tab));
if (route.query.tab !== undefined && normalizeTab(route.query.tab) !== route.query.tab) {
  void router.replace({ query: { ...route.query, tab: activeTab.value } });
}

const activeComponent = computed(() => ({
  deployments: ServiceDeployments,
  availability: ServiceMonitor,
  metrics: MetricMonitor,
}[activeTab.value]));

function normalizeTab(value: unknown): ServiceManagementTab {
  return value === 'availability' || value === 'metrics' ? value : 'deployments';
}

function onTabChange(value: string | number) {
  const tab = normalizeTab(value);
  activeTab.value = tab;
  void router.replace({ query: { ...route.query, tab } });
}

watch(() => route.query.tab, (value) => {
  const tab = normalizeTab(value);
  if (tab !== activeTab.value) activeTab.value = tab;
});
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

.page-head {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  margin-bottom: 8px;
}

.page-head h2 {
  margin: 0 0 4px;
  font-size: 20px;
  font-weight: 600;
}

.page-head p {
  margin: 0;
  color: var(--color-text-3);
}

.management-tabs {
  flex: none;
}

.management-content {
  min-height: 0;
  flex: 1;
  overflow: hidden;
}

.management-content :deep(.moox-page),
.management-content :deep(.monitor-page),
.management-content :deep(.metric-monitor-page) {
  height: 100%;
  min-height: 0;
}
</style>
