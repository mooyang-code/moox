<template>
  <a-modal
    :visible="visible"
    :title="mode === 'check' ? '检查缺口' : '新建补采'"
    :width="result ? 860 : 520"
    :ok-text="mode === 'check' ? '检查' : '提交'"
    :ok-loading="busy"
    :footer="!result"
    unmount-on-close
    @before-ok="onSubmit"
    @cancel="close"
  >
    <template v-if="!result">
      <a-form :model="form" layout="vertical">
        <a-form-item label="数据集">
          <a-select v-model="form.datasetId" placeholder="全部数据集" allow-clear allow-search>
            <a-option v-for="item in datasetOptions" :key="item" :value="item">{{ item }}</a-option>
          </a-select>
        </a-form-item>
        <a-form-item label="标的（留空表示全部）">
          <a-input v-model="form.subjectId" placeholder="BTCUSDT" allow-clear />
        </a-form-item>
        <a-form-item
          label="时间范围（UTC，起始含、结束不含）"
          :validate-status="rangeError ? 'error' : undefined"
          :help="rangeError"
        >
          <a-range-picker
            v-model="form.range"
            show-time
            format="YYYY-MM-DD HH:mm"
            style="width: 100%"
            @change="rangeError = ''"
          />
        </a-form-item>
        <a-space class="quick-ranges" wrap>
          <a-button v-for="item in quickRanges" :key="item.hours" size="mini" @click="applyQuick(item.hours)">{{
            item.label
          }}</a-button>
        </a-space>
        <a-form-item v-if="mode === 'backfill'">
          <a-checkbox v-model="form.force">整段重拉（不检查缺口，范围内应有的 K 线全部重拉）</a-checkbox>
        </a-form-item>
        <a-form-item v-else>
          <a-checkbox v-model="form.deep">深度检查（校验字段齐全）</a-checkbox>
        </a-form-item>
      </a-form>
    </template>

    <template v-else>
      <div class="gap-summary">
        <span
          >应有 {{ result.expected || 0 }} 根，缺失
          <b :class="{ danger: (result.missing || 0) > 0 }">{{ result.missing || 0 }}</b> 根</span
        >
        <span v-if="result.absent" class="muted">（另有 {{ result.absent }} 根已确认行情源没有，不计入）</span>
        <span v-if="result.truncated" class="muted">（结果过多，已截断）</span>
      </div>
      <a-table
        v-if="gapRows.length"
        row-key="key"
        size="small"
        :data="gapRows"
        :pagination="{ pageSize: 10, size: 'small' }"
        :bordered="{ cell: true }"
      >
        <template #columns>
          <a-table-column title="数据集" data-index="dataset_id" />
          <a-table-column title="频率" data-index="frequency" :width="70" />
          <a-table-column title="标的" data-index="subject_id" :width="130" />
          <a-table-column title="范围（UTC）" :width="260">
            <template #cell="{ record }">{{ utcText(record.from) }} ~ {{ utcText(record.to) }}</template>
          </a-table-column>
          <a-table-column title="缺失根数" data-index="missing" :width="90" align="right" />
        </template>
      </a-table>
      <div class="dialog-footer">
        <a-button @click="close">关闭</a-button>
        <a-button v-if="(result.missing || 0) > 0" type="primary" :loading="busy" @click="backfillGaps">补采这些缺口</a-button>
      </div>
    </template>
  </a-modal>
</template>

<script setup lang="ts">
import { computed, ref, watch } from "vue";
import { Message } from "@arco-design/web-vue";
import { checkKline, submitKlineBackfill, type CheckKlineResponse } from "@/api/collector/runtime-status";
import { quickRange, toRFC3339, utcText } from "./runtime-status-model";

const props = defineProps<{
  visible: boolean;
  mode: "check" | "backfill";
  spaceId: string;
  datasetOptions: string[];
  datasetId?: string;
}>();

const emit = defineEmits<{
  (event: "update:visible", value: boolean): void;
  (event: "submitted"): void;
}>();

const quickRanges = [
  { hours: 1, label: "最近 1 小时" },
  { hours: 24, label: "最近 1 天" },
  { hours: 24 * 7, label: "最近 7 天" }
];

const form = ref({ datasetId: "", subjectId: "", range: [] as string[], force: false, deep: false });
const rangeError = ref("");
const busy = ref(false);
const result = ref<CheckKlineResponse | null>(null);
// 检查时的范围：“补采这些缺口”按同一范围按缺口补采。
const checkedRequest = ref<{ dataset_id?: string; subject_id?: string; from: string; to: string } | null>(null);

const gapRows = computed(() =>
  (result.value?.gaps || []).map((item, index) => ({
    ...item,
    key: `${item.dataset_id}|${item.subject_id}|${item.from}|${index}`
  }))
);

watch(
  () => props.visible,
  visible => {
    if (!visible) return;
    form.value = { datasetId: props.datasetId || "", subjectId: "", range: [], force: false, deep: false };
    rangeError.value = "";
    result.value = null;
    checkedRequest.value = null;
  }
);

function applyQuick(hours: number) {
  form.value.range = quickRange(hours);
  rangeError.value = "";
}

function close() {
  emit("update:visible", false);
}

function rangeRequest() {
  const [rawFrom = "", rawTo = ""] = form.value.range || [];
  const from = toRFC3339(rawFrom);
  const to = toRFC3339(rawTo);
  if (!from || !to) {
    rangeError.value = "请选择起止时间";
    return null;
  }
  if (from >= to) {
    rangeError.value = "起始时间要早于结束时间";
    return null;
  }
  return { dataset_id: form.value.datasetId || undefined, subject_id: form.value.subjectId.trim() || undefined, from, to };
}

async function submitBackfill(request: { dataset_id?: string; subject_id?: string; from: string; to: string }, force: boolean) {
  const created = await submitKlineBackfill({ space_id: props.spaceId, ...request, force });
  Message.success(created.job_id ? `已创建补采作业，共 ${created.chunks || 0} 个分块` : "范围内没有缺口");
  if (created.job_id) emit("submitted");
}

async function onSubmit(): Promise<boolean> {
  const request = rangeRequest();
  if (!request) return false;
  busy.value = true;
  try {
    if (props.mode === "check") {
      result.value = await checkKline({ space_id: props.spaceId, ...request, deep: form.value.deep });
      checkedRequest.value = request;
      // 留在弹窗里展示结果。
      return false;
    }
    await submitBackfill(request, form.value.force);
    close();
    return true;
  } catch (error) {
    console.error("检查或补采失败:", error);
    Message.error(error instanceof Error ? error.message : "检查或补采失败");
    return false;
  } finally {
    busy.value = false;
  }
}

async function backfillGaps() {
  if (!checkedRequest.value) return;
  busy.value = true;
  try {
    await submitBackfill(checkedRequest.value, false);
    close();
  } catch (error) {
    console.error("补采失败:", error);
    Message.error(error instanceof Error ? error.message : "补采失败");
  } finally {
    busy.value = false;
  }
}
</script>

<style scoped>
.quick-ranges {
  margin: calc(var(--moox-space-2) * -1) 0 var(--moox-space-3);
}
.gap-summary {
  display: flex;
  flex-wrap: wrap;
  gap: var(--moox-space-1);
  align-items: baseline;
  margin-bottom: var(--moox-space-3);
}
.gap-summary .danger {
  color: rgb(var(--danger-6));
}
.muted {
  color: var(--color-text-3);
  font-size: 12px;
}
.dialog-footer {
  display: flex;
  justify-content: flex-end;
  gap: var(--moox-space-2);
  margin-top: var(--moox-space-3);
}
</style>
