<template>
  <div class="observability-overview">
    <header class="overview-toolbar">
      <div class="overview-summary" aria-label="可观测性摘要">
        <span
          ><b>{{ abnormalCount }}</b> 异常</span
        >
        <span
          ><b>{{ overview.services?.length || 0 }}</b> 服务</span
        >
        <span
          ><b>{{ overview.hosts?.length || 0 }}</b> 主机</span
        >
        <span
          ><b>{{ overview.datasets?.length || 0 }}</b> Dataset + Frequency</span
        >
      </div>
      <a-tooltip content="刷新总览">
        <a-button shape="circle" :loading="loading" aria-label="刷新总览" @click="loadOverview">
          <template #icon><icon-refresh /></template>
        </a-button>
      </a-tooltip>
    </header>

    <section class="scf-band" aria-label="SCF 运行状态">
      <strong>SCF</strong>
      <span class="status-item status-healthy">在线 {{ overview.scf?.online_count || 0 }}</span>
      <span class="status-item status-stale">超时 {{ overview.scf?.timeout_count || 0 }}</span>
      <span class="status-item status-unknown">未知 {{ overview.scf?.unknown_count || 0 }}</span>
      <span class="muted">最旧心跳 {{ formatTime(overview.scf?.oldest_heartbeat_at) }}</span>
    </section>

    <section class="overview-section">
      <div class="section-head"><strong>服务</strong><span>健康检查与 Reporter freshness</span></div>
      <a-table
        row-key="service_name"
        size="small"
        :loading="loading"
        :data="overview.services || []"
        :pagination="false"
        :scroll="{ x: 'max-content', y: 230 }"
      >
        <template #columns>
          <a-table-column title="状态" :width="100">
            <template #cell="{ record }"
              ><a-tag :color="statusColor(record.status)">{{ statusLabel(record.status) }}</a-tag></template
            >
          </a-table-column>
          <a-table-column title="服务" data-index="service_name" :width="220" />
          <a-table-column title="节点 / 实例" :width="260">
            <template #cell="{ record }">{{ record.node_id || "-" }} / {{ record.instance_id || "-" }}</template>
          </a-table-column>
          <a-table-column title="原因" data-index="reason" :width="180" />
          <a-table-column title="最后上报" :width="190">
            <template #cell="{ record }">{{ formatTime(record.last_seen_at) }}</template>
          </a-table-column>
        </template>
      </a-table>
      <div v-if="!loading && !overview.services?.length" class="inline-empty">尚未上报</div>
    </section>

    <section class="overview-section">
      <div class="section-head"><strong>主机</strong><span>agent reachable、CPU、Memory、Filesystem</span></div>
      <a-table
        row-key="agent_id"
        size="small"
        :loading="loading"
        :data="overview.hosts || []"
        :pagination="false"
        :scroll="{ x: 'max-content', y: 230 }"
      >
        <template #columns>
          <a-table-column title="状态" :width="100">
            <template #cell="{ record }"
              ><a-tag :color="statusColor(record.status)">{{ statusLabel(record.status) }}</a-tag></template
            >
          </a-table-column>
          <a-table-column title="主机" data-index="hostname" :width="200" />
          <a-table-column title="CPU" :width="100"
            ><template #cell="{ record }">{{ formatPercent(record.cpu_percent) }}</template></a-table-column
          >
          <a-table-column title="内存" :width="100"
            ><template #cell="{ record }">{{ formatPercent(record.memory_percent) }}</template></a-table-column
          >
          <a-table-column title="文件系统峰值" :width="130">
            <template #cell="{ record }">{{ formatPercent(record.filesystem_max_percent) }}</template>
          </a-table-column>
          <a-table-column title="原因" data-index="reason" :width="180" />
          <a-table-column title="最后上报" :width="190">
            <template #cell="{ record }">{{ formatTime(record.last_seen_at) }}</template>
          </a-table-column>
        </template>
      </a-table>
      <div v-if="!loading && !overview.hosts?.length" class="inline-empty">尚未上报</div>
    </section>

    <section class="overview-section dataset-section">
      <div class="section-head"><strong>实时 Dataset + Frequency</strong><span>异常优先，最多 1000 项</span></div>
      <div class="dataset-filters">
        <a-select v-model="filters.status" allow-clear placeholder="状态">
          <a-option v-for="value in statusOptions" :key="value" :value="value">{{ statusLabel(value) }}</a-option>
        </a-select>
        <a-select v-model="filters.producer" allow-clear allow-search placeholder="模块">
          <a-option v-for="value in producerOptions" :key="value" :value="value">{{ value }}</a-option>
        </a-select>
        <a-input v-model="filters.dataset" allow-clear placeholder="Dataset" />
        <a-select v-model="filters.freq" allow-clear allow-search placeholder="Frequency">
          <a-option v-for="value in freqOptions" :key="value" :value="value">{{ value }}</a-option>
        </a-select>
      </div>
      <a-table
        row-key="dataset_id"
        size="small"
        :loading="loading"
        :data="filteredDatasets"
        :pagination="{ pageSize: 50, hideOnSinglePage: true }"
        :scroll="{ x: 'max-content', y: 420 }"
      >
        <template #columns>
          <a-table-column title="状态" :width="100">
            <template #cell="{ record }"
              ><a-tag :color="statusColor(record.status)">{{ statusLabel(record.status) }}</a-tag></template
            >
          </a-table-column>
          <a-table-column title="模块" data-index="producer" :width="150" />
          <a-table-column title="Dataset" :width="260" :ellipsis="true" :tooltip="true">
            <template #cell="{ record }"
              ><span class="dataset-id">{{ record.dataset_id }}</span></template
            >
          </a-table-column>
          <a-table-column title="Frequency" data-index="freq" :width="110" />
          <a-table-column title="原因" data-index="reason" :width="170" />
          <a-table-column title="最后运行" :width="190">
            <template #cell="{ record }">{{ formatTime(record.last_run_at) }}</template>
          </a-table-column>
          <a-table-column title="最后成功" :width="190">
            <template #cell="{ record }">{{ formatTime(record.last_success_at) }}</template>
          </a-table-column>
          <a-table-column title="Input Watermark" :width="190">
            <template #cell="{ record }">{{ formatTime(record.input_watermark_at) }}</template>
          </a-table-column>
          <a-table-column title="Output Watermark" :width="190">
            <template #cell="{ record }">{{ formatTime(record.output_watermark_at) }}</template>
          </a-table-column>
          <a-table-column title="Lag" :width="110">
            <template #cell="{ record }">{{ formatLag(record.lag_seconds) }}</template>
          </a-table-column>
        </template>
      </a-table>
      <div v-if="!loading && !filteredDatasets.length" class="inline-empty">{{ datasetEmptyDescription }}</div>
    </section>

    <section class="overview-section">
      <div class="section-head"><strong>Canary / Balance</strong><span>业务逻辑最后检查结果</span></div>
      <a-table
        row-key="kind"
        size="small"
        :loading="loading"
        :data="overview.business_checks || []"
        :pagination="false"
        :scroll="{ x: 'max-content' }"
      >
        <template #columns>
          <a-table-column title="状态" :width="100">
            <template #cell="{ record }"
              ><a-tag :color="statusColor(record.status)">{{ statusLabel(record.status) }}</a-tag></template
            >
          </a-table-column>
          <a-table-column title="类型" data-index="kind" :width="140" />
          <a-table-column title="模块" data-index="module" :width="160" />
          <a-table-column title="原因" data-index="reason" :width="260" />
          <a-table-column title="最后检查" :width="190">
            <template #cell="{ record }">{{ formatTime(record.last_checked_at) }}</template>
          </a-table-column>
        </template>
      </a-table>
      <div v-if="!loading && !overview.business_checks?.length" class="inline-empty">尚未上报</div>
    </section>
  </div>
</template>

<script setup lang="ts">
import { computed, onActivated, reactive, ref } from "vue";
import { reportControlError } from "@/api/admin/http";
import { metricMonitorApi } from "@/api/metric-monitor";
import type { ObservabilityOverview } from "@/api/metric-monitor/types";

const loading = ref(false);
const overview = ref<ObservabilityOverview>({});
const filters = reactive({ status: "", producer: "", dataset: "", freq: "" });
const statusOptions = ["down", "stale", "degraded", "unknown", "healthy"];

const producerOptions = computed(() => unique((overview.value.datasets || []).map(item => item.producer)));
const freqOptions = computed(() => unique((overview.value.datasets || []).map(item => item.freq)));
const filteredDatasets = computed(() =>
  (overview.value.datasets || []).filter(
    item =>
      (!filters.status || item.status === filters.status) &&
      (!filters.producer || item.producer === filters.producer) &&
      (!filters.freq || item.freq === filters.freq) &&
      (!filters.dataset || (item.dataset_id || "").toLowerCase().includes(filters.dataset.toLowerCase()))
  )
);
const abnormalCount = computed(
  () =>
    [
      ...(overview.value.services || []),
      ...(overview.value.hosts || []),
      ...(overview.value.datasets || []),
      ...(overview.value.business_checks || [])
    ].filter(item => item.status !== "healthy").length
);
const datasetEmptyDescription = computed(() => {
  if ((overview.value.services || []).some(item => item.status === "stale")) return "producer stale";
  if (!(overview.value.datasets || []).length) return "尚未上报";
  return "正常但空结果";
});

async function loadOverview() {
  loading.value = true;
  try {
    const response = await metricMonitorApi.getObservabilityOverview();
    overview.value = response.overview || {};
  } catch (error) {
    reportControlError(error);
  } finally {
    loading.value = false;
  }
}

function unique(values: Array<string | undefined>) {
  return [...new Set(values.filter((value): value is string => Boolean(value)))].sort();
}

function statusColor(status?: string) {
  return (
    ({ healthy: "green", stale: "orange", degraded: "orange", down: "red", unknown: "gray" } as Record<string, string>)[
      status || ""
    ] || "gray"
  );
}

function statusLabel(status?: string) {
  return (
    ({ healthy: "正常", stale: "陈旧", degraded: "降级", down: "异常", unknown: "未知" } as Record<string, string>)[
      status || ""
    ] ||
    status ||
    "未知"
  );
}

function formatTime(value?: string) {
  if (!value) return "-";
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? value : date.toLocaleString();
}

function formatPercent(value?: number) {
  return typeof value === "number" ? `${value.toFixed(1)}%` : "-";
}

function formatLag(value?: number) {
  if (typeof value !== "number") return "-";
  if (value < 60) return `${value}s`;
  if (value < 3600) return `${Math.floor(value / 60)}m`;
  return `${Math.floor(value / 3600)}h`;
}

onActivated(loadOverview);
</script>

<style scoped lang="scss">
.observability-overview {
  min-width: 0;
  height: 100%;
  overflow: auto;
}

.overview-toolbar,
.scf-band,
.section-head,
.dataset-filters {
  display: flex;
  align-items: center;
}

.overview-toolbar {
  justify-content: space-between;
  padding: 0 0 var(--moox-space-3);
}

.overview-summary,
.scf-band {
  display: flex;
  flex-wrap: wrap;
  gap: var(--moox-space-4);
}

.overview-summary span,
.scf-band span {
  color: var(--color-text-2);
}

.overview-summary b {
  margin-right: 4px;
  color: var(--color-text-1);
}

.scf-band {
  padding: 10px 0;
  border-top: 1px solid var(--color-border-2);
  border-bottom: 1px solid var(--color-border-2);
}

.overview-section {
  padding: var(--moox-space-4) 0;
  border-bottom: 1px solid var(--color-border-2);
}

.section-head {
  gap: var(--moox-space-3);
  margin-bottom: var(--moox-space-3);
}

.section-head span,
.muted {
  color: var(--color-text-3);
  font-size: 12px;
}

.dataset-filters {
  flex-wrap: wrap;
  gap: var(--moox-space-2);
  margin-bottom: var(--moox-space-3);
}

.dataset-filters > * {
  width: 180px;
}

.dataset-id {
  overflow-wrap: anywhere;
}

.inline-empty {
  padding: 24px;
  color: var(--color-text-3);
  text-align: center;
}

@media (max-width: 640px) {
  .overview-toolbar {
    align-items: flex-start;
    gap: var(--moox-space-2);
  }

  .overview-summary {
    gap: var(--moox-space-2) var(--moox-space-3);
  }

  .scf-band {
    gap: var(--moox-space-2);
  }

  .dataset-filters {
    display: grid;
    grid-template-columns: minmax(0, 1fr) minmax(0, 1fr);
  }

  .dataset-filters > * {
    width: 100%;
    min-width: 0;
  }

  .section-head {
    align-items: flex-start;
    flex-direction: column;
    gap: 2px;
  }
}
</style>
