<template>
  <a-modal
    :visible="visible"
    width="620px"
    :ok-loading="loading"
    :ok-button-props="{ disabled: Boolean(activeBackfill) }"
    @ok="start"
    @cancel="close"
    @close="close"
  >
    <template #title>K 线重采样历史回填</template>
    <a-alert v-if="activeBackfill" type="info" :show-icon="true" :closable="false">
      规则正在回填：{{ activeBackfill.requestId }}，已登记 {{ activeBackfill.participants }} 个标的。
      下一桶：{{ activeBackfill.nextBucket || "等待调度" }}
    </a-alert>
    <a-form layout="vertical" :model="form">
      <a-form-item label="规则">
        <a-input :model-value="ruleId" disabled />
      </a-form-item>
      <a-form-item label="目标周期">
        <a-input :model-value="targetFrequency" disabled />
      </a-form-item>
      <a-form-item label="开始时间（UTC，包含）" required>
        <a-input v-model="form.start" placeholder="例如 2026-08-28T00:00:00Z" />
      </a-form-item>
      <a-form-item label="结束时间（UTC，不包含）" required>
        <a-input v-model="form.end" placeholder="例如 2026-08-29T00:00:00Z" />
      </a-form-item>
    </a-form>
    <a-descriptions :column="1" size="small" bordered>
      <a-descriptions-item label="预计桶数">{{ bucketCount || "-" }}</a-descriptions-item>
      <a-descriptions-item label="源数据保留期">{{ sourceKeepDuration || "未提供，由服务端校验" }}</a-descriptions-item>
      <a-descriptions-item label="目标写入空间">内部行情 `crypto`</a-descriptions-item>
    </a-descriptions>
    <a-alert v-if="errorMessage" class="dialog-error" type="error" :show-icon="true" :closable="false">
      {{ errorMessage }}
    </a-alert>
    <template #footer>
      <a-space>
        <a-button v-if="activeBackfill" status="warning" :loading="cancelling" @click="cancel">取消回填</a-button>
        <a-button @click="close">关闭</a-button>
        <a-button type="primary" :disabled="Boolean(activeBackfill)" :loading="loading" @click="start">开始回填</a-button>
      </a-space>
    </template>
  </a-modal>
</template>

<script setup lang="ts">
import { computed, reactive, ref, watch } from "vue";
import { Message } from "@arco-design/web-vue";
import { cancelKlineResampleBackfill, startKlineResampleBackfill } from "@/api/collector";
import {
  countBackfillBuckets,
  defaultClosedEnd,
  formatUtcInput,
  parseFixedFrequencyMinutes,
  type ResampleBackfillSummary
} from "./resample-backfill";

const props = withDefaults(
  defineProps<{
    visible: boolean;
    spaceId: string;
    ruleId: string;
    targetFrequency: string;
    sourceKeepDuration?: string;
    activeBackfill?: ResampleBackfillSummary | null;
  }>(),
  { sourceKeepDuration: "", activeBackfill: null }
);

const emit = defineEmits<{
  (event: "update:visible", value: boolean): void;
  (event: "started"): void;
  (event: "cancelled"): void;
}>();

const form = reactive({ start: "", end: "" });
const loading = ref(false);
const cancelling = ref(false);
const errorMessage = ref("");
const bucketCount = computed(() => countBackfillBuckets(form.start, form.end, props.targetFrequency));

function resetForm() {
  const minutes = parseFixedFrequencyMinutes(props.targetFrequency) || 60;
  const end = defaultClosedEnd(props.targetFrequency) || new Date();
  const start = new Date(end.getTime() - minutes * 60 * 1000 * 24);
  form.start = formatUtcInput(start);
  form.end = formatUtcInput(end);
  errorMessage.value = "";
}

watch(
  () => [props.visible, props.targetFrequency] as const,
  ([visible]) => {
    if (visible) resetForm();
  },
  { immediate: true }
);

function close() {
  emit("update:visible", false);
}

async function start() {
  errorMessage.value = "";
  if (!props.spaceId || !props.ruleId || !form.start || !form.end) {
    errorMessage.value = "空间、规则和 UTC 时间范围不能为空";
    return;
  }
  if (!bucketCount.value) {
    errorMessage.value = "时间范围必须是目标周期的完整桶，且结束时间必须晚于开始时间";
    return;
  }
  loading.value = true;
  try {
    await startKlineResampleBackfill({
      space_id: props.spaceId,
      rule_id: props.ruleId,
      request_id: `resample-${Date.now()}`,
      start: new Date(form.start).toISOString(),
      end: new Date(form.end).toISOString()
    });
    Message.success("回填已提交");
    emit("started");
  } catch (error) {
    errorMessage.value = error instanceof Error ? error.message : "回填提交失败";
  } finally {
    loading.value = false;
  }
}

async function cancel() {
  const requestId = props.activeBackfill?.requestId;
  if (!requestId) return;
  cancelling.value = true;
  errorMessage.value = "";
  try {
    await cancelKlineResampleBackfill({ space_id: props.spaceId, rule_id: props.ruleId, request_id: requestId });
    Message.success("已取消回填");
    emit("cancelled");
  } catch (error) {
    errorMessage.value = error instanceof Error ? error.message : "取消回填失败";
  } finally {
    cancelling.value = false;
  }
}
</script>

<style scoped>
.dialog-error {
  margin-top: 12px;
}
</style>

