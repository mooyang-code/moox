<template>
  <a-drawer
    :visible="visible"
    :width="'min(880px, 100vw)'"
    :title="mode === 'create' ? '新建归档任务' : '编辑归档任务'"
    :ok-text="mode === 'create' ? '创建' : '保存'"
    :ok-loading="submitting"
    unmount-on-close
    @ok="submit"
    @cancel="emit('update:visible', false)"
  >
    <div class="editor">
      <a-form layout="vertical" :model="form" class="form">
        <a-form-item label="数据集" required>
          <a-select
            v-if="mode === 'create'"
            v-model="form.datasetId"
            placeholder="选择时序数据集"
            allow-search
            @change="onDatasetChange"
          >
            <a-option
              v-for="item in datasets"
              :key="item.dataset_id"
              :value="item.dataset_id"
              :disabled="taskIds.has(item.dataset_id)"
              >{{ item.name || item.dataset_id }} · {{ item.freq || "—" }} · {{ formatRetention(item.retention)
              }}{{ taskIds.has(item.dataset_id) ? "（已有任务）" : "" }}</a-option
            >
          </a-select>
          <span v-else>{{ task?.dataset_id }}</span>
          <template v-if="prepareError" #extra
            ><span class="is-bad">读取数据集信息失败：{{ prepareError }}</span></template
          >
        </a-form-item>
        <a-alert v-if="mode === 'create' && prepared?.exists" type="warning" class="exists">
          已有归档任务 · <a-link @click="openExisting">打开它</a-link>
        </a-alert>

        <a-form-item
          label="拉取间隔（每收盘多少根拉取一次）"
          :validate-status="pullError ? 'error' : undefined"
          :help="pullError || undefined"
        >
          <a-input-number
            v-model="form.pullEveryBars"
            :min="1"
            :max="1440"
            :precision="0"
            :disabled="daily"
            style="width: 160px"
          />
          <template v-if="daily" #extra>每天收盘后拉取一次</template>
        </a-form-item>

        <template v-if="mode === 'create' || (task && showBackfillField(task))">
          <a-form-item label="回补起点" :validate-status="backfillError ? 'error' : undefined" :help="backfillError || undefined">
            <ZonedDatePicker v-model="form.backfillFrom" :zone="zone" allow-clear style="width: 100%" />
            <template v-if="!backfillError" #extra>{{
              mode === "create" ? "从线上还保留的最早数据开始归档" : "只有改了才重新校验；留空表示不修改，不能用来清除"
            }}</template>
          </a-form-item>
        </template>
        <div v-else class="hint">
          首次复核已完成，回补起点不再使用；要补更早的数据请用<a-link @click="task && emit('openBackfill', task)"
            >补拉区间</a-link
          >
        </div>

        <a-form-item v-if="mode === 'create'" label="创建后立即启用">
          <a-switch v-model="form.enabled" />
        </a-form-item>
      </a-form>

      <aside class="preview" aria-label="拉取预览">
        <h4>接下来</h4>
        <div v-if="!currentDatasetId" class="sub">选好数据集后显示接下来的拉取与复核</div>
        <div v-else-if="previewError" class="is-bad">{{ previewError }}</div>
        <div v-else-if="!preview" class="sub">读取中…</div>
        <template v-else>
          <div class="block">
            <div class="label">接下来 5 次拉取</div>
            <div v-for="at in preview.next_runs || []" :key="at">{{ formatInstant(at, zone) }}</div>
          </div>
          <div class="block">
            <div class="label">接下来 3 次复核</div>
            <div v-for="at in preview.next_reviews || []" :key="at">{{ formatInstant(at, zone) }}</div>
          </div>
          <div class="block">
            复核间隔 {{ intervalText(preview.interval_seconds) }}，每次复核读取约
            {{ durationText(preview.read_span_seconds || 0) }}
          </div>
          <div v-if="preview.partition_export_estimates?.length" class="block">
            <div class="label">分区预计导出</div>
            <div v-for="text in preview.partition_export_estimates" :key="text">{{ exportEstimateText(text, zone) }}</div>
          </div>
        </template>
        <div class="sub fixed">每分钟第 25 秒检查；每小时 :58～:04 不拉取，这段时间的边界顺延到 :04:25。</div>
        <div class="sub">到点拉取只读新收盘的几根，补采由复核兜底。</div>
      </aside>
    </div>
  </a-drawer>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, reactive, ref, watch } from "vue";
import { Message } from "@arco-design/web-vue";
import {
  createArchiveTask,
  prepareArchiveTask,
  previewArchiveSchedule,
  updateArchiveTask,
  type ArchiveTask,
  type PrepareArchiveTaskRsp,
  type PreviewArchiveScheduleRsp
} from "@/api/archive";
import type { Dataset } from "@/api/storage/types";
import ZonedDatePicker from "@/components/zoned-date-picker/zoned-date-picker.vue";
import { createLatestRequestGuard } from "@/utils/latest-request";
import { formatRetention } from "@/utils/retention";
import { formatInstant, toInstantMs } from "@/utils/time-zone";
import {
  backfillFromError,
  durationText,
  exportEstimateText,
  intervalText,
  isDailyFreq,
  pullEveryBarsError,
  showBackfillField
} from "./archive-model";

/**
 * 新建 / 编辑归档任务。新建：选数据集后 Prepare 给出推荐的拉取间隔与最早回补起点（默认就填它）；
 * 编辑：数据集不可改，回补起点只在还没有成功的复核时可改。右侧按数据集与间隔防抖预览接下来的拉取与复核。
 */
const props = defineProps<{
  visible: boolean;
  mode: "create" | "edit";
  task: ArchiveTask | null;
  presetDatasetId: string;
  /** 应该归档的数据集（时序、active、有限保留期） */
  datasets: Dataset[];
  tasks: ArchiveTask[];
  zone: string;
}>();
const emit = defineEmits<{
  "update:visible": [visible: boolean];
  created: [datasetId: string];
  updated: [];
  openTask: [datasetId: string];
  openBackfill: [task: ArchiveTask];
}>();

const form = reactive({
  datasetId: "",
  pullEveryBars: 1 as number | undefined,
  backfillFrom: null as number | null,
  enabled: true
});
const prepared = ref<PrepareArchiveTaskRsp | null>(null);
const prepareError = ref("");
const preview = ref<PreviewArchiveScheduleRsp | null>(null);
const previewError = ref("");
const submitting = ref(false);
const prepareGuard = createLatestRequestGuard();
const previewGuard = createLatestRequestGuard();
let previewTimer: ReturnType<typeof setTimeout> | undefined;
// 编辑时打开抽屉那一刻的回补起点：没改就不提交，也不校验（后端对没改的值保留原值，即使已超出保留期）
const originalBackfill = ref<number | null>(null);

const taskIds = computed(() => new Set(props.tasks.map(item => item.dataset_id)));
const currentDatasetId = computed(() => (props.mode === "edit" ? props.task?.dataset_id || "" : form.datasetId));
const freq = computed(() => (props.mode === "edit" ? props.task?.freq : prepared.value?.freq) || "");
const daily = computed(() => isDailyFreq(freq.value));
const earliestMs = computed(() => toInstantMs(prepared.value?.earliest_backfill_from));
const pullError = computed(() => (freq.value ? pullEveryBarsError(freq.value, Number(form.pullEveryBars)) : ""));
const backfillChanged = computed(() => props.mode === "create" || form.backfillFrom !== originalBackfill.value);
const backfillError = computed(() =>
  backfillFromError(backfillChanged.value ? form.backfillFrom : null, earliestMs.value, Date.now(), props.zone)
);

watch(
  () => props.visible,
  open => {
    if (!open) return;
    prepared.value = null;
    prepareError.value = "";
    preview.value = null;
    previewError.value = "";
    if (props.mode === "edit" && props.task) {
      originalBackfill.value = toInstantMs(props.task.backfill_from);
      Object.assign(form, {
        datasetId: props.task.dataset_id || "",
        pullEveryBars: props.task.pull_every_bars || 1,
        backfillFrom: originalBackfill.value,
        enabled: true
      });
      void prepare(props.task.dataset_id || "", false);
    } else {
      Object.assign(form, { datasetId: props.presetDatasetId, pullEveryBars: 1, backfillFrom: null, enabled: true });
      if (props.presetDatasetId) void prepare(props.presetDatasetId, true);
    }
  },
  { immediate: true }
);

function onDatasetChange(value: unknown) {
  void prepare(String(value || ""), true);
}

/** 读数据集的归档信息；新建时用推荐的间隔和最早回补起点填表。 */
async function prepare(datasetId: string, fill: boolean) {
  const request = prepareGuard.begin();
  prepared.value = null;
  prepareError.value = "";
  if (!datasetId) return;
  try {
    const rsp = await prepareArchiveTask(datasetId, { silentError: true });
    if (!request.isLatest()) return;
    prepared.value = rsp;
    if (fill) {
      form.pullEveryBars = rsp.recommended_pull_every_bars || 1;
      form.backfillFrom = toInstantMs(rsp.earliest_backfill_from);
    }
  } catch (cause) {
    if (request.isLatest()) prepareError.value = cause instanceof Error && cause.message ? cause.message : "请稍后重试";
  }
}

// 数据集或间隔变化后防抖 400ms 预览；服务端校验失败显示在预览区，过期的结果丢弃
watch([currentDatasetId, () => form.pullEveryBars, () => props.visible], () => {
  if (previewTimer) clearTimeout(previewTimer);
  previewGuard.invalidate();
  preview.value = null;
  previewError.value = "";
  if (!props.visible || !currentDatasetId.value) return;
  previewTimer = setTimeout(() => void loadPreview(), 400);
});
onBeforeUnmount(() => previewTimer && clearTimeout(previewTimer));

async function loadPreview() {
  const request = previewGuard.begin();
  try {
    const rsp = await previewArchiveSchedule(
      { dataset_id: currentDatasetId.value, pull_every_bars: Number(form.pullEveryBars) || 1 },
      { silentError: true }
    );
    if (request.isLatest()) preview.value = rsp;
  } catch (cause) {
    if (request.isLatest()) previewError.value = cause instanceof Error && cause.message ? cause.message : "预览失败";
  }
}

function openExisting() {
  emit("update:visible", false);
  emit("openTask", form.datasetId);
}

const iso = (ms: number | null) => (ms === null ? "" : new Date(ms).toISOString().replace(/\.000Z$/, "Z"));

async function submit() {
  if (props.mode === "create" && (!form.datasetId || prepared.value?.exists)) {
    Message.warning(form.datasetId ? "这个数据集已有归档任务" : "请选择数据集");
    return;
  }
  if (pullError.value || backfillError.value) {
    Message.warning(pullError.value || backfillError.value);
    return;
  }
  submitting.value = true;
  try {
    if (props.mode === "create") {
      await createArchiveTask({
        dataset_id: form.datasetId,
        pull_every_bars: Number(form.pullEveryBars) || 1,
        backfill_from: iso(form.backfillFrom),
        paused: !form.enabled
      });
      Message.success("归档任务已创建");
      emit("update:visible", false);
      emit("created", form.datasetId);
    } else if (props.task) {
      const changedBackfill = form.backfillFrom !== null && form.backfillFrom !== originalBackfill.value;
      await updateArchiveTask({
        dataset_id: props.task.dataset_id || "",
        pull_every_bars: Number(form.pullEveryBars) === props.task.pull_every_bars ? 0 : Number(form.pullEveryBars) || 0,
        backfill_from: changedBackfill ? iso(form.backfillFrom) : ""
      });
      Message.success("已保存");
      emit("update:visible", false);
      emit("updated");
    }
  } catch {
    // 失败原因由接口封装提示，抽屉保持打开
  } finally {
    submitting.value = false;
  }
}
</script>

<style scoped>
.editor {
  display: grid;
  grid-template-columns: minmax(0, 1fr) 300px;
  gap: var(--moox-space-4);
}

@media (max-width: 720px) {
  .editor {
    grid-template-columns: 1fr;
  }
}

.preview {
  padding-left: var(--moox-space-3);
  border-left: 1px solid var(--color-border-2);
}

@media (max-width: 720px) {
  .preview {
    padding-left: 0;
    border-left: none;
  }
}

h4 {
  margin: 0 0 8px;
  font-size: 14px;
}

.block {
  margin-bottom: 10px;
  line-height: 22px;
}

.label {
  color: var(--color-text-3);
  font-size: 12px;
}

.sub {
  color: var(--color-text-3);
  font-size: 12px;
  line-height: 20px;
}

.fixed {
  margin-top: 12px;
}

.hint {
  margin-bottom: var(--moox-space-3);
  color: var(--color-text-2);
}

.exists {
  margin-bottom: var(--moox-space-3);
}

.is-bad {
  color: rgb(var(--red-6));
}
</style>
