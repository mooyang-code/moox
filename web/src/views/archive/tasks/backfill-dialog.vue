<template>
  <a-modal
    :visible="visible"
    :title="`补拉区间：${task?.dataset_id || ''}`"
    :width="'min(560px, 100vw)'"
    ok-text="补拉"
    :ok-loading="submitting"
    unmount-on-close
    :on-before-ok="submit"
    @cancel="emit('update:visible', false)"
  >
    <ZonedRangePicker v-model="range" :zone="zone" :quick-ranges="quickRanges" style="width: 100%" />
    <div v-if="rangeError && range" class="is-bad">{{ rangeError }}</div>
    <div v-if="prepareError" class="is-bad">读取线上保留期失败：{{ prepareError }}，下界由后端校验</div>
    <div class="sub">只重读这段时间涉及的分区，不推进水位；排队中的到点拉取会被取消、让位；有运行中的运行时请稍后再试。</div>
  </a-modal>
</template>

<script setup lang="ts">
import { computed, ref, watch } from "vue";
import { Message } from "@arco-design/web-vue";
import { backfillArchiveRange, prepareArchiveTask, type ArchiveTask } from "@/api/archive";
import ZonedRangePicker from "@/components/zoned-date-picker/zoned-range-picker.vue";
import { createLatestRequestGuard } from "@/utils/latest-request";
import { toInstantMs } from "@/utils/time-zone";
import { backfillRangeError } from "../shared/archive-model";

/** 补拉区间：按空间时区选起止，下界是今天 − 线上保留期 + 1 天（Prepare 给出，和后端补拉校验同口径），上界是现在。 */
const props = defineProps<{ visible: boolean; task: ArchiveTask | null; zone: string }>();
const emit = defineEmits<{ "update:visible": [visible: boolean]; started: [datasetId: string] }>();

const range = ref<[number, number] | null>(null);
const earliestMs = ref<number | null>(null);
const prepareError = ref("");
const submitting = ref(false);
const guard = createLatestRequestGuard();

const HOUR = 3_600_000;
const quickRanges = [
  { label: "最近 1 小时", range: (): [number, number] => [Date.now() - HOUR, Date.now()] },
  { label: "最近 1 天", range: (): [number, number] => [Date.now() - 24 * HOUR, Date.now()] }
];
const rangeError = computed(() => backfillRangeError(range.value, earliestMs.value, Date.now(), props.zone));

watch(
  () => props.visible,
  async open => {
    if (!open || !props.task?.dataset_id) return;
    range.value = null;
    earliestMs.value = null;
    prepareError.value = "";
    const request = guard.begin();
    try {
      const rsp = await prepareArchiveTask(props.task.dataset_id, "", { silentError: true });
      if (request.isLatest()) earliestMs.value = toInstantMs(rsp.earliest_backfill_from);
    } catch (cause) {
      if (request.isLatest()) prepareError.value = cause instanceof Error && cause.message ? cause.message : "请稍后重试";
    }
  },
  { immediate: true }
);

const iso = (ms: number) => new Date(ms).toISOString().replace(/\.000Z$/, "Z");

async function submit(): Promise<boolean> {
  const id = props.task?.dataset_id || "";
  const error = backfillRangeError(range.value, earliestMs.value, Date.now(), props.zone);
  if (error || !range.value) {
    Message.warning(error);
    return false;
  }
  submitting.value = true;
  try {
    await backfillArchiveRange({ dataset_id: id, range_start: iso(range.value[0]), range_end: iso(range.value[1]) });
  } catch {
    return false;
  } finally {
    submitting.value = false;
  }
  Message.success("已排队补拉");
  emit("update:visible", false);
  emit("started", id);
  return true;
}
</script>

<style scoped>
.sub {
  margin-top: 8px;
  color: var(--color-text-3);
  font-size: 12px;
  line-height: 20px;
}

.is-bad {
  margin-top: 6px;
  color: rgb(var(--red-6));
}
</style>
