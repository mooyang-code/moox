<template>
  <div v-if="info" class="overview">
    <a-descriptions :column="2" bordered size="small" title="基本信息">
      <a-descriptions-item label="空间">{{ set.space_id }}</a-descriptions-item>
      <a-descriptions-item label="频率">{{ set.freq }}</a-descriptions-item>
      <a-descriptions-item label="源数据集">{{ set.source_dataset_id }}</a-descriptions-item>
      <a-descriptions-item label="结果数据集">{{ set.result_dataset_id }}</a-descriptions-item>
      <a-descriptions-item label="对象范围">
        {{ set.subject_mode === "all" ? "全部对象" : `指定 ${set.subjects.length} 个：${set.subjects.join(", ")}` }}
      </a-descriptions-item>
      <a-descriptions-item label="因子数"> {{ info.factors?.length || 0 }}（已启用 {{ enabledCount }}） </a-descriptions-item>
      <a-descriptions-item label="创建时间">{{ formatTime(set.created_at) }}</a-descriptions-item>
      <a-descriptions-item label="更新时间">{{ formatTime(set.updated_at) }}</a-descriptions-item>
    </a-descriptions>

    <section class="overview__section">
      <h3>运行状态</h3>
      <a-space wrap size="large" class="runtime">
        <span>
          实时消费
          <a-tag size="small" :color="engine?.consumer_running ? 'green' : 'gray'">{{
            engine?.consumer_running ? "运行中" : "停止"
          }}</a-tag>
        </span>
        <span>Python Worker {{ engine?.python_workers ?? "-" }}，忙碌 {{ engine?.python_busy ?? "-" }}</span>
        <span>本因子集队列 {{ lane?.queued ?? 0 }}，{{ lane?.active ? "计算中" : "空闲" }}</span>
      </a-space>
    </section>

    <section class="overview__section">
      <h3>最近周期</h3>
      <a-empty
        v-if="!lastRun?.last_period_time"
        description="服务启动后尚未完成周期；状态为进程内记录，重启后在下一个周期完成前为空"
      />
      <template v-else>
        <a-space wrap size="large" class="runtime">
          <span>周期时间 {{ formatPeriod(lastRun.last_period_time) }}</span>
          <span>
            整体状态
            <a-tag size="small" :color="health.color">{{ health.label }}</a-tag>
          </span>
          <span>延迟 {{ formatLag(lastRun.lag_seconds) }}</span>
          <span v-if="lastRun.failed_subjects?.length">失败对象 {{ lastRun.failed_subjects.length }}</span>
        </a-space>
        <a-table
          class="overview__table"
          row-key="factor_id"
          size="small"
          :bordered="{ cell: true }"
          :pagination="false"
          :data="lastRun.factors || []"
        >
          <template #columns>
            <a-table-column title="因子" data-index="factor_id" :width="200" />
            <a-table-column title="本周期状态" :width="120">
              <template #cell="{ record }">
                <a-tag size="small" :color="periodStatusTag(record.status).color">{{
                  periodStatusTag(record.status).label
                }}</a-tag>
              </template>
            </a-table-column>
            <a-table-column title="失败对象" :width="320" :ellipsis="true" :tooltip="true">
              <template #cell="{ record }">
                {{
                  record.failed_subjects?.length
                    ? `${record.failed_subjects.length} 个：${record.failed_subjects.join(", ")}`
                    : "-"
                }}
              </template>
            </a-table-column>
            <a-table-column title="源码 Hash" :width="140">
              <template #cell="{ record }"
                ><code>{{ (record.source_hash || "").slice(0, 8) || "-" }}</code></template
              >
            </a-table-column>
          </template>
        </a-table>
        <p class="overview__hint">
          “降级”表示部分对象计算失败，对应列在这些对象上为 NULL；“已跳过”表示截面因子因上游缺失对象且不允许部分参与而跳过。
        </p>
      </template>
    </section>
  </div>
</template>

<script setup lang="ts">
import { computed } from "vue";
import type { EngineStatus, FactorSet, FactorSetInfo, SetRunSummary } from "@/api/factor/types";
import { useFactorStore } from "@/store/modules/factor";
import { formatTime } from "@/views/data/shared/metadata-utils";
import { formatLag, formatPeriod, setHealth } from "../health";
import { periodStatusTag } from "../status";

defineOptions({ name: "FactorOverviewTab" });

const store = useFactorStore();
const info = computed<FactorSetInfo | undefined>(() => store.current);
const set = computed<FactorSet>(() => info.value!.factor_set);
const lastRun = computed<SetRunSummary | undefined>(() => info.value?.last_run);
const engine = computed<EngineStatus | null>(() => store.engine);
const lane = computed(() => engine.value?.lanes?.find(item => item.set_id === set.value.set_id));
const enabledCount = computed(() => (info.value?.factors || []).filter(factor => factor.status === "enabled").length);
const health = computed(() => setHealth(set.value, lastRun.value));
</script>

<style scoped>
.overview__section {
  margin-top: var(--moox-space-5);
}

.overview__section h3 {
  margin: 0 0 var(--moox-space-2);
  font-size: 15px;
  font-weight: 600;
}

.runtime {
  color: var(--color-text-2);
  font-size: 13px;
}

.overview__table {
  margin-top: var(--moox-space-3);
}

.overview__hint {
  margin: var(--moox-space-2) 0 0;
  color: var(--color-text-3);
  font-size: 12px;
}
</style>
