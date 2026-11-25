<template>
  <a-drawer v-model:visible="visible" title="任务详情" width="640px" :footer="false" unmount-on-close @cancel="emit('cancel')">
    <template v-if="task">
      <section class="detail-section">
        <h4 class="detail-section__title">基本信息</h4>
        <a-descriptions :column="2" bordered>
          <a-descriptions-item label="任务名称">{{ task.task_name || "-" }}</a-descriptions-item>
          <a-descriptions-item label="任务 ID">{{ task.task_id }}</a-descriptions-item>
          <a-descriptions-item label="数据类型">{{ catalog.dataTypeLabel(task.data_type) }}</a-descriptions-item>
          <a-descriptions-item label="启用状态">
            <a-tag size="small" :color="task.enabled ? 'green' : 'orange'">
              {{ task.enabled ? "启用" : "禁用" }}
            </a-tag>
          </a-descriptions-item>
          <a-descriptions-item label="创建人">{{ task.creator || "-" }}</a-descriptions-item>
          <a-descriptions-item label="创建时间">{{ formatDateTime(task.create_time) }}</a-descriptions-item>
        </a-descriptions>
      </section>
      <section class="detail-section">
        <h4 class="detail-section__title">执行方式</h4>
        <a-descriptions :column="2" bordered>
          <a-descriptions-item label="频率">{{ taskFrequency(task) }}</a-descriptions-item>
          <a-descriptions-item label="来源 / 市场">{{ catalog.taskRouteSummary(task) }}</a-descriptions-item>
          <a-descriptions-item v-if="task.data_type === 'kline'" label="采集方式" :span="2">{{
            ingestLabel(task.ingest)
          }}</a-descriptions-item>
          <a-descriptions-item label="数据范围标签" :span="2">{{ catalog.taskTagSummary(task) }}</a-descriptions-item>
        </a-descriptions>
        <a-descriptions v-if="task.data_type === 'kline_resample'" :column="2" bordered class="detail-resample-info">
          <a-descriptions-item label="源任务">{{ catalog.resampleSourceSummary(task) }}</a-descriptions-item>
          <a-descriptions-item label="源周期 / 序列标签">
            {{ catalog.stringParam(task, "source_frequency") || "-" }} /
            {{ catalog.stringParam(task, "source_series_tag") || "-" }}
          </a-descriptions-item>
        </a-descriptions>
        <div v-if="dependents.length" class="detail-dependents">
          <span class="field-hint">依赖它的合成任务：</span>
          <a-link v-for="item in dependents" :key="item.task_id" @click="emit('locate', item)">
            {{ item.task_name || item.task_id }}（{{ taskFrequency(item) }}）
          </a-link>
        </div>
      </section>
      <section class="detail-section">
        <h4 class="detail-section__title">输出字段</h4>
        <div v-if="outputFields.length" class="detail-output-tags">
          <a-tag v-for="field in outputFields" :key="field.id" color="arcoblue" :title="field.id">
            {{ field.name }}
          </a-tag>
        </div>
        <span v-else class="field-hint">未选择时使用采集方法的默认输出字段。</span>
      </section>
      <section class="detail-section">
        <h4 class="detail-section__title">运行信息</h4>
        <a-descriptions :column="2" bordered>
          <a-descriptions-item label="结果状态">
            <a-tag size="small" :color="resultStatusColor">{{ resultStatusLabel }}</a-tag>
          </a-descriptions-item>
          <a-descriptions-item label="最近数据时间">{{ formatDateTime(task.result?.last_data_time) }}</a-descriptions-item>
          <a-descriptions-item label="归档">
            <ArchiveCoverageText :dataset-id="task.result?.dataset_id" />
          </a-descriptions-item>
          <a-descriptions-item label="任务描述" :span="2">{{ task.description || "-" }}</a-descriptions-item>
          <a-descriptions-item v-if="task.last_error" label="准备信息" :span="2">
            {{ task.last_error }}
          </a-descriptions-item>
        </a-descriptions>
      </section>
      <a-space class="detail-actions">
        <a-button type="primary" @click="emit('results', task)">采集结果</a-button>
        <a-button @click="emit('edit', task)">修改</a-button>
        <a-button v-if="task.data_type === 'kline_resample'" @click="emit('jobs', task)">历史合成</a-button>
      </a-space>
    </template>
  </a-drawer>
</template>

<script setup lang="ts">
import { computed, watch } from "vue";
import ArchiveCoverageText from "@/views/archive/shared/archive-coverage-text.vue";
import { useDisplayZone } from "@/hooks/useDisplayZone";
import { formatInstant } from "@/utils/time-zone";
import {
  collectionTaskResultLabel,
  ingestLabel,
  outputFieldLabels,
  parseCollectionTaskInput,
  taskFrequency,
  type CollectionTaskRecord
} from "./collection-task-params";
import { useCollectionCatalog } from "./use-collection-catalog";

defineOptions({ name: "CollectionTaskDetailDrawer" });

const props = defineProps<{ task?: CollectionTaskRecord }>();
const visible = defineModel<boolean>("visible", { required: true });
const emit = defineEmits<{
  (event: "cancel"): void;
  (event: "results" | "edit" | "jobs" | "locate", task: CollectionTaskRecord): void;
}>();

const catalog = useCollectionCatalog();
// 页面时间按当前空间登记的时区显示
const displayZone = useDisplayZone();

const dependents = computed(() => catalog.dependentsOf(props.task));
const outputFields = computed(() => {
  if (!props.task) return [];
  const ids = parseCollectionTaskInput(props.task).outputFields || [];
  return outputFieldLabels(ids, catalog.outputFieldCatalogs.value[props.task.data_type || ""] || []);
});
const resultStatusLabel = computed(() => (props.task ? collectionTaskResultLabel(props.task) : ""));
const resultStatusColor = computed(() =>
  resultStatusLabel.value === "结果可用" ? "green" : resultStatusLabel.value === "结果异常" ? "red" : "orange"
);

// 输出字段的中文名来自该数据类型的字段规格：打开详情时读一次。
watch(
  () => (visible.value ? props.task?.data_type : ""),
  dataType => {
    if (dataType) void catalog.ensureOutputFieldCatalog(dataType);
  },
  { immediate: true }
);

/** 采集任务属于空间：时间按空间时区带后缀。 */
function formatDateTime(value?: string) {
  return formatInstant(value, displayZone.value);
}
</script>

<style scoped>
.field-hint {
  display: block;
  margin-top: 4px;
  color: var(--color-text-3);
  font-size: 12px;
}
.detail-section {
  margin-bottom: var(--moox-space-5);
}
.detail-section__title {
  margin: 0 0 var(--moox-space-3);
  color: var(--color-text-1);
  font-size: 14px;
  font-weight: 600;
}
.detail-output-tags {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 6px;
}
.detail-resample-info {
  margin-top: 8px;
}
.detail-dependents {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 6px;
  margin-top: 8px;
}
.detail-dependents .field-hint {
  margin-top: 0;
}
.detail-actions {
  margin-top: 16px;
}
</style>
