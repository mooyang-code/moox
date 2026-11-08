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
          v-if="mode === 'create'"
          label="目录名"
          required
          :validate-status="dirError ? 'error' : undefined"
          :help="dirError || undefined"
        >
          <a-input v-model="form.dirName" :max-length="64" :disabled="!prepared" placeholder="选好数据集后给出建议值" />
          <template v-if="!dirError" #extra>
            <div>建好后不能改。{{ dirCheck.checking ? "检查中…" : "" }}</div>
            <div class="mono path">{{ pathPreview }}</div>
          </template>
        </a-form-item>
        <a-form-item v-else label="目录名">
          <span class="mono">{{ task?.dir_name || "—" }}</span>
          <template #extra>
            <div>建好后不能改</div>
            <div class="mono path">{{ pathPreview }}</div>
          </template>
        </a-form-item>

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
        <div class="sub fixed">{{ avoidanceText || "避让时段读取中" }}</div>
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
  type AvoidanceRules,
  type PrepareArchiveTaskRsp,
  type PreviewArchiveScheduleRsp
} from "@/api/archive";
import type { Dataset } from "@/api/storage/types";
import ZonedDatePicker from "@/components/zoned-date-picker/zoned-date-picker.vue";
import { createLatestRequestGuard } from "@/utils/latest-request";
import { formatRetention } from "@/utils/retention";
import { formatInstant, toInstantMs } from "@/utils/time-zone";
import {
  avoidanceScheduleText,
  backfillFromError,
  dirNameError,
  durationText,
  exportEstimateText,
  intervalText,
  isDailyFreq,
  partitionPathPreview,
  pullEveryBarsError,
  showBackfillField
} from "../shared/archive-model";

/**
 * 新建 / 编辑归档任务。新建：选数据集后 Prepare 给出推荐的拉取间隔与最早回补起点（默认就填它）；
 * 编辑：数据集不可改，回补起点只在还没有成功的复核时可改。右侧按数据集与间隔防抖预览接下来的拉取与复核。
 * 目录名：新建时默认填建议值，可改；改过的值防抖后用 Prepare 带 dir_name 检查是否被占用，冲突时标红；建好后不能改。
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
  spaceId: string;
  /** 当前的避让规则（概览接口），右侧说明按它写 */
  avoidance?: AvoidanceRules;
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
  enabled: true,
  dirName: ""
});
/** 目录名的占用检查：name 是检查过的目录名，conflict 是它的结果 */
const dirCheck = reactive({ name: "", conflict: false, checking: false, error: "" });
const dirGuard = createLatestRequestGuard();
let dirTimer: ReturnType<typeof setTimeout> | undefined;
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
const dirError = computed(() => {
  if (props.mode !== "create" || !prepared.value) return "";
  const local = dirNameError(form.dirName);
  if (local) return local;
  if (dirCheck.name === form.dirName && dirCheck.conflict) return "目录名已被这个空间的其他任务使用，请改一个";
  if (dirCheck.name === form.dirName && dirCheck.error) return `检查目录名失败：${dirCheck.error}`;
  return "";
});
const pathPreview = computed(() =>
  props.mode === "edit"
    ? partitionPathPreview(
        props.task?.space_id || props.spaceId,
        props.task?.purpose || "",
        props.task?.dir_name || "",
        props.task?.partition_unit
      )
    : partitionPathPreview(props.spaceId, prepared.value?.purpose || "", form.dirName, prepared.value?.partition_unit)
);
const avoidanceText = computed(() => avoidanceScheduleText(props.avoidance));

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
        enabled: true,
        dirName: props.task.dir_name || ""
      });
      void prepare(props.task.dataset_id || "", false);
    } else {
      Object.assign(form, { datasetId: props.presetDatasetId, pullEveryBars: 1, backfillFrom: null, enabled: true, dirName: "" });
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
  resetDirCheck();
  if (!datasetId) return;
  try {
    const rsp = await prepareArchiveTask(datasetId, "", { silentError: true });
    if (!request.isLatest()) return;
    prepared.value = rsp;
    if (fill) {
      form.pullEveryBars = rsp.recommended_pull_every_bars || 1;
      form.backfillFrom = toInstantMs(rsp.earliest_backfill_from);
      // 建议值的占用情况已经在这次 Prepare 里
      form.dirName = rsp.suggested_dir_name || "";
      Object.assign(dirCheck, { name: form.dirName, conflict: Boolean(rsp.dir_name_conflict), error: "" });
    }
  } catch (cause) {
    if (request.isLatest()) prepareError.value = cause instanceof Error && cause.message ? cause.message : "请稍后重试";
  }
}

function resetDirCheck() {
  if (dirTimer) clearTimeout(dirTimer);
  dirGuard.invalidate();
  Object.assign(dirCheck, { name: "", conflict: false, checking: false, error: "" });
}

/** 改目录名后防抖 400ms 检查占用；不合规的不发请求（后端会当参数错误）；只更新检查结果，不动表单其他字段。 */
watch(() => form.dirName, onDirNameChange);
function onDirNameChange() {
  if (props.mode !== "create") return;
  if (dirTimer) clearTimeout(dirTimer);
  dirGuard.invalidate();
  dirCheck.checking = false;
  const name = form.dirName;
  if (!form.datasetId || dirNameError(name) || name === dirCheck.name) return;
  dirCheck.checking = true;
  dirTimer = setTimeout(() => void checkDirName(form.datasetId, name), 400);
}

async function checkDirName(datasetId: string, name: string) {
  const request = dirGuard.begin();
  try {
    const rsp = await prepareArchiveTask(datasetId, name, { silentError: true });
    if (request.isLatest()) Object.assign(dirCheck, { name, conflict: Boolean(rsp.dir_name_conflict), error: "" });
  } catch (cause) {
    if (request.isLatest())
      Object.assign(dirCheck, {
        name,
        conflict: false,
        error: cause instanceof Error && cause.message ? cause.message : "请稍后重试"
      });
  } finally {
    if (request.isLatest()) dirCheck.checking = false;
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
onBeforeUnmount(() => {
  if (previewTimer) clearTimeout(previewTimer);
  if (dirTimer) clearTimeout(dirTimer);
});

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
  if (props.mode === "create") {
    // 改过的目录名还没检查完：不等防抖，立即检查一次再判断
    if (prepared.value && !dirNameError(form.dirName) && dirCheck.name !== form.dirName) {
      if (dirTimer) clearTimeout(dirTimer);
      await checkDirName(form.datasetId, form.dirName);
    }
    if (dirError.value || !prepared.value) {
      Message.warning(dirError.value || "数据集信息还没读到");
      return;
    }
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
        paused: !form.enabled,
        dir_name: form.dirName
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

.mono {
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
}

.path {
  word-break: break-all;
}
</style>
