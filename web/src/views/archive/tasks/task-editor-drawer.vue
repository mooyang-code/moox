<template>
  <a-drawer
    :visible="visible"
    :width="'min(640px, 100vw)'"
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

        <template v-if="mode === 'create'">
          <a-form-item
            label="回补起点"
            :required="foreverRetention"
            :validate-status="backfillError ? 'error' : undefined"
            :help="backfillError || undefined"
          >
            <ZonedDatePicker v-model="form.backfillFrom" :zone="zone" allow-clear style="width: 100%" />
            <template v-if="!backfillError" #extra>{{
              foreverRetention
                ? "线上永久保留：必须填写从哪里开始归档；建好后不能改"
                : "默认从线上保留期起点开始归档；留空同默认；建好后不能改"
            }}</template>
          </a-form-item>
        </template>
        <template v-else>
          <a-form-item label="回补起点">
            <span>{{ formatInstant(task?.backfill_from, zone) }}</span>
            <template #extra
              >建好后不能改；要补更早的数据，保留期内用<a-link @click="task && emit('openBackfill', task)">补拉区间</a-link
              >，保留期外用 moox-cli import --target archive</template
            >
          </a-form-item>
          <a-form-item label="启用">
            <a-switch v-model="form.enabled" />
            <template #extra>停用后不再跟随；重新启用后从原位点继续</template>
          </a-form-item>
        </template>

        <a-form-item v-if="mode === 'create'" label="创建后立即启用">
          <a-switch v-model="form.enabled" />
        </a-form-item>
      </a-form>

      <div class="sub note">{{ ARCHIVE_SCHEDULE_NOTE }}</div>
    </div>
  </a-drawer>
</template>

<script setup lang="ts">
import { reportError } from "@/api/http";
import { computed, onBeforeUnmount, reactive, ref, watch } from "vue";
import { Message } from "@arco-design/web-vue";
import {
  createArchiveTask,
  prepareArchiveTask,
  updateArchiveTask,
  type ArchiveTask,
  type PrepareArchiveTaskRsp
} from "@/api/archive";
import type { Dataset } from "@/api/storage/types";
import ZonedDatePicker from "@/components/zoned-date-picker/zoned-date-picker.vue";
import { createLatestRequestGuard } from "@/utils/latest-request";
import { formatRetention } from "@/utils/retention";
import { formatInstant, toInstantMs } from "@/utils/time-zone";
import {
  ARCHIVE_SCHEDULE_NOTE,
  backfillFromError,
  dirNameError,
  dirNameOwner,
  partitionPathPreview
} from "../shared/archive-model";

/**
 * 新建 / 编辑归档任务。新建：选数据集后 Prepare 给出最早回补起点（线上保留期起点，默认就填它；永久保留时必填）；
 * 编辑：只能启用、停用，回补起点建好后不改。
 * 目录名：新建时默认填建议值，可改；改过的值防抖后用 Prepare 带 dir_name 检查是否被占用，冲突时标红；建好后不能改。
 */
const props = defineProps<{
  visible: boolean;
  mode: "create" | "edit";
  task: ArchiveTask | null;
  presetDatasetId: string;
  /** 当前空间的时序数据集（归档任务由 ArchiveMgr 自动补齐，手工新建入口保留） */
  datasets: Dataset[];
  tasks: ArchiveTask[];
  zone: string;
  spaceId: string;
  /** 执行器的归档根目录；有就把路径预览写成绝对路径 */
  rootDir?: string;
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
const submitting = ref(false);
const prepareGuard = createLatestRequestGuard();

const taskIds = computed(() => new Set(props.tasks.map(item => item.dataset_id)));
const earliestMs = computed(() => toInstantMs(prepared.value?.earliest_backfill_from));
const foreverRetention = computed(() => prepared.value?.retention === "forever");
const backfillError = computed(() => {
  if (props.mode !== "create") return "";
  if (foreverRetention.value && form.backfillFrom === null) return "线上永久保留的数据集必须填写回补起点";
  return backfillFromError(form.backfillFrom, earliestMs.value, Date.now(), props.zone);
});
const dirError = computed(() => {
  if (props.mode !== "create" || !prepared.value) return "";
  const local = dirNameError(form.dirName);
  if (local) return local;
  if (dirCheck.name === form.dirName && dirCheck.conflict) {
    // 冲突的任务在当前空间的任务表里时写出它的数据集；找不到（例如任务表还没刷新）时泛指
    const owner = dirNameOwner(props.tasks, prepared.value?.purpose || "", form.dirName);
    const ownerId = owner?.dataset_id || "";
    const name = props.datasets.find(item => item.dataset_id === ownerId)?.name || ownerId;
    return name ? `目录名已被「${name}」使用，请改一个` : "目录名已被这个空间的其他任务使用，请改一个";
  }
  if (dirCheck.name === form.dirName && dirCheck.error) return `检查目录名失败：${dirCheck.error}`;
  return "";
});
const pathPreview = computed(() =>
  props.mode === "edit"
    ? partitionPathPreview(
        props.task?.space_id || props.spaceId,
        props.task?.purpose || "",
        props.task?.dir_name || "",
        props.task?.partition_unit,
        props.rootDir
      )
    : partitionPathPreview(
        props.spaceId,
        prepared.value?.purpose || "",
        form.dirName,
        prepared.value?.partition_unit,
        props.rootDir
      )
);

watch(
  () => props.visible,
  open => {
    if (!open) return;
    prepared.value = null;
    prepareError.value = "";
    if (props.mode === "edit" && props.task) {
      Object.assign(form, {
        datasetId: props.task.dataset_id || "",
        backfillFrom: toInstantMs(props.task.backfill_from),
        enabled: props.task.status !== "disabled",
        dirName: props.task.dir_name || ""
      });
    } else {
      Object.assign(form, { datasetId: props.presetDatasetId, backfillFrom: null, enabled: true, dirName: "" });
      if (props.presetDatasetId) void prepare(props.presetDatasetId, true);
    }
  },
  { immediate: true }
);

function onDatasetChange(value: unknown) {
  void prepare(String(value || ""), true);
}

/** 读数据集的归档信息；新建时用最早回补起点（线上保留期起点）与建议目录名填表。 */
async function prepare(datasetId: string, fill: boolean) {
  const request = prepareGuard.begin();
  prepared.value = null;
  prepareError.value = "";
  resetDirCheck();
  if (!datasetId) return;
  try {
    const rsp = await prepareArchiveTask(datasetId, "");
    if (!request.isLatest()) return;
    prepared.value = rsp;
    if (fill) {
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
    const rsp = await prepareArchiveTask(datasetId, name);
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

onBeforeUnmount(() => {
  if (dirTimer) clearTimeout(dirTimer);
});

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
    // 改过的目录名还没检查完、或上次检查失败：不等防抖，立即检查一次再判断
    if (prepared.value && !dirNameError(form.dirName) && (dirCheck.name !== form.dirName || dirCheck.error)) {
      if (dirTimer) clearTimeout(dirTimer);
      await checkDirName(form.datasetId, form.dirName);
    }
    if (dirError.value || !prepared.value) {
      Message.warning(dirError.value || "数据集信息还没读到");
      return;
    }
  }
  if (backfillError.value) {
    Message.warning(backfillError.value);
    return;
  }
  submitting.value = true;
  try {
    if (props.mode === "create") {
      await createArchiveTask({
        dataset_id: form.datasetId,
        backfill_from: iso(form.backfillFrom),
        disabled: !form.enabled,
        dir_name: form.dirName
      });
      Message.success("归档任务已创建");
      emit("update:visible", false);
      emit("created", form.datasetId);
    } else if (props.task) {
      const status = form.enabled ? "enabled" : "disabled";
      if (status !== (props.task.status === "disabled" ? "disabled" : "enabled")) {
        await updateArchiveTask({ dataset_id: props.task.dataset_id || "", status });
      }
      Message.success("已保存");
      emit("update:visible", false);
      emit("updated");
    }
  } catch (error) {
    reportError(error);
  } finally {
    submitting.value = false;
  }
}
</script>

<style scoped>
.note {
  margin-top: var(--moox-space-3);
}

.sub {
  color: var(--color-text-3);
  font-size: 12px;
  line-height: 20px;
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
