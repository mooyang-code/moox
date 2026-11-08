<template>
  <a-drawer
    :visible="visible"
    :title="`历史合成 · ${taskName || taskId}`"
    :footer="false"
    :width="760"
    unmount-on-close
    @cancel="emit('update:visible', false)"
  >
    <a-alert type="info" show-icon class="jobs-hint">
      把时间范围内的 {{ targetFrequency }} K 线按源数据重新合成。范围按当前空间的时区选择，
      并向外对齐到目标桶；源数据不完整的标的会跳过并计数， 补采这些源数据后会自动再触发重算，也可以对同一范围再提交一次。
    </a-alert>

    <section class="jobs-section">
      <h3>提交作业</h3>
      <a-form :model="form" layout="vertical">
        <a-form-item label="时间范围（起始含、结束不含）" required>
          <ZonedRangePicker v-model="form.range" :zone="displayZone" style="width: 100%" />
        </a-form-item>
        <a-form-item label="标的（可选）">
          <a-textarea
            v-model="form.subjects"
            placeholder="只合成这些标的，逗号或换行分隔；留空表示源任务的全部标的"
            :auto-size="{ minRows: 1, maxRows: 3 }"
          />
        </a-form-item>
        <a-space>
          <a-button type="primary" :loading="submitting" @click="submit">提交作业</a-button>
          <span class="jobs-muted">最多 10080 个目标桶；重复点击不会重复建作业。</span>
        </a-space>
      </a-form>
    </section>

    <section class="jobs-section">
      <header class="jobs-section__header">
        <h3>作业列表</h3>
        <a-button size="small" :loading="loading" @click="reload">刷新</a-button>
      </header>
      <a-table
        :data="jobs"
        row-key="job_id"
        size="small"
        :pagination="false"
        :loading="loading"
        :bordered="{ cell: true }"
        :expandable="{ width: 40 }"
      >
        <template #columns>
          <a-table-column title="来源" :width="90">
            <template #cell="{ record }">{{ resampleJobOriginLabel(record.origin) }}</template>
          </a-table-column>
          <a-table-column title="范围" :width="230">
            <template #cell="{ record }">
              <div><TimeText :value="record.start" :zone="displayZone" precision="minute" /></div>
              <div class="jobs-muted">至 <TimeText :value="record.end" :zone="displayZone" precision="minute" /></div>
            </template>
          </a-table-column>
          <a-table-column title="状态" :width="100" align="center">
            <template #cell="{ record }">
              <a-tag size="small" :color="resampleJobStatusColor(record.status)">{{
                resampleJobStatusLabel(record.status)
              }}</a-tag>
            </template>
          </a-table-column>
          <a-table-column title="进度" :width="150">
            <template #cell="{ record }">
              <a-progress size="small" :percent="resampleJobProgress(record) / 100" :show-text="false" />
              <span class="jobs-muted">{{ record.done_buckets }} / {{ record.total_buckets }} 桶</span>
            </template>
          </a-table-column>
          <a-table-column title="写入 / 跳过" :width="110" align="center">
            <template #cell="{ record }">{{ record.written }} / {{ record.skipped }}</template>
          </a-table-column>
          <a-table-column title="操作" :width="90" align="center">
            <template #cell="{ record }">
              <a-button v-if="isActiveResampleJob(record.status)" size="mini" status="danger" type="text" @click="cancel(record)"
                >取消</a-button
              >
              <span v-else class="jobs-muted">-</span>
            </template>
          </a-table-column>
        </template>
        <template #expand-row="{ record }">
          <div class="jobs-detail">
            <div v-if="record.not_before && record.status === 'queued'">
              最早开始时间：<TimeText :value="record.not_before" :zone="displayZone" precision="minute" />
            </div>
            <div v-if="record.subjects && record.subjects.length">标的：{{ record.subjects.join("、") }}</div>
            <div v-if="record.last_error" class="jobs-error">{{ record.last_error }}</div>
            <div v-if="record.skipped_samples && record.skipped_samples.length">
              跳过样例（标的@桶：原因）：
              <ul>
                <li v-for="sample in record.skipped_samples" :key="sample">{{ sample }}</li>
              </ul>
            </div>
            <div v-if="!record.last_error && !(record.skipped_samples && record.skipped_samples.length)" class="jobs-muted">
              没有更多信息
            </div>
          </div>
        </template>
      </a-table>
    </section>
  </a-drawer>
</template>

<script setup lang="ts">
import TimeText from "@/components/time-text/index.vue";
import { onBeforeUnmount, reactive, ref, watch } from "vue";
import { Message } from "@arco-design/web-vue";
import { cancelKlineResampleJob, listKlineResampleJobs, submitKlineResampleJob, type ResampleJob } from "@/api/collector";
import {
  defaultResampleRange,
  isActiveResampleJob,
  newResampleRequestId,
  parseSubjectInput,
  resampleJobOriginLabel,
  resampleJobProgress,
  resampleJobStatusColor,
  resampleJobStatusLabel,
  validateResampleRange
} from "./resample-jobs";
import { useDisplayZone } from "@/hooks/useDisplayZone";
import ZonedRangePicker from "@/components/zoned-date-picker/zoned-range-picker.vue";

const props = defineProps<{ visible: boolean; spaceId: string; taskId: string; taskName: string; targetFrequency: string }>();
const emit = defineEmits<{ (event: "update:visible", value: boolean): void }>();

const jobs = ref<ResampleJob[]>([]);
const loading = ref(false);
const submitting = ref(false);
// 合成范围按 K 线的时刻：显示和选择都按当前空间登记的时区，提交时换成 UTC
const displayZone = useDisplayZone();
const form = reactive({ range: null as [number, number] | null, subjects: "" });
let timer: ReturnType<typeof setInterval> | undefined;
let requestToken = 0;

async function reload() {
  if (!props.spaceId || !props.taskId) return;
  const token = ++requestToken;
  loading.value = true;
  try {
    const list = await listKlineResampleJobs(props.spaceId, props.taskId, "", 50);
    if (token === requestToken) jobs.value = list;
  } catch (error) {
    if (token === requestToken) Message.error(error instanceof Error ? error.message : "获取历史合成作业失败");
  } finally {
    if (token === requestToken) loading.value = false;
  }
}

async function submit() {
  const problem = validateResampleRange(form.range);
  if (problem) {
    Message.error(problem);
    return;
  }
  submitting.value = true;
  try {
    const response = await submitKlineResampleJob({
      space_id: props.spaceId,
      task_id: props.taskId,
      request_id: newResampleRequestId(),
      start: new Date(form.range![0]).toISOString(),
      end: new Date(form.range![1]).toISOString(),
      subjects: parseSubjectInput(form.subjects)
    });
    Message.success(response.created === false ? "已存在相同请求的作业" : "历史合成作业已提交");
    await reload();
  } catch (error) {
    Message.error(error instanceof Error ? error.message : "提交历史合成作业失败");
  } finally {
    submitting.value = false;
  }
}

async function cancel(job: ResampleJob) {
  try {
    await cancelKlineResampleJob(props.spaceId, job.job_id);
    Message.success("作业已取消，已写入的保留");
    await reload();
  } catch (error) {
    Message.error(error instanceof Error ? error.message : "取消作业失败");
  }
}

function stopPolling() {
  if (timer) clearInterval(timer);
  timer = undefined;
}

// 抽屉打开时每 5 秒刷新一次作业进度；关闭时停止。
watch(
  () => props.visible,
  visible => {
    stopPolling();
    if (!visible) return;
    form.range = defaultResampleRange(displayZone.value);
    form.subjects = "";
    jobs.value = [];
    void reload();
    timer = setInterval(() => void reload(), 5000);
  },
  { immediate: true }
);

onBeforeUnmount(stopPolling);
</script>

<style scoped lang="scss">
.jobs-hint {
  margin-bottom: 16px;
}

.jobs-section {
  margin-bottom: 20px;

  h3 {
    margin: 0 0 12px;
    font-size: 15px;
  }
}

.jobs-section__header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 12px;

  h3 {
    margin: 0;
  }
}

.jobs-muted {
  color: var(--color-text-3);
  font-size: 12px;
}

.jobs-detail {
  display: flex;
  flex-direction: column;
  gap: 4px;
  padding: 4px 8px;
  font-size: 12px;

  ul {
    margin: 4px 0 0;
    padding-left: 18px;
  }
}

.jobs-error {
  color: rgb(var(--danger-6));
}
</style>
