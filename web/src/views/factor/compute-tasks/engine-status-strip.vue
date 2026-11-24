<template>
  <section class="engine-status" aria-label="引擎与补算">
    <div class="stats-strip" role="list">
      <div v-for="item in stats" :key="item.key" class="stat" role="listitem">
        <span class="stat__label">{{ item.label }}</span>
        <span class="stat__value" :class="`is-${item.tone}`">{{ item.value }}</span>
        <span class="stat__hint">{{ item.hint }}</span>
      </div>
    </div>
    <p v-if="jobsError" class="recalc-line is-error">补算作业读取失败：{{ jobsError }}</p>
    <p v-if="split.running.length" class="recalc-line">
      <span class="recalc-line__label">进行中的补算</span>
      <span v-for="job in split.running" :key="job.job_id" class="recalc-item">
        <a-link @click="go(job)">{{ labelOf(job.task_id) }}</a-link>
        <span class="recalc-item__text">{{ progressPercent(job) }}%</span>
      </span>
    </p>
    <p v-if="split.recentFailed.length" class="recalc-line">
      <span class="recalc-line__label is-danger">近 24 小时失败的补算</span>
      <span v-for="job in split.recentFailed" :key="job.job_id" class="recalc-item">
        <a-link @click="go(job)">{{ labelOf(job.task_id) }}</a-link>
        <span class="recalc-item__text">{{ job.error || "补算失败" }}</span>
      </span>
    </p>
  </section>
</template>

<script setup lang="ts">
import { computed, ref, watch } from "vue";
import { useRouter } from "vue-router";
import { listRecalcJobs } from "@/api/factor";
import type { RecalcJob } from "@/api/factor/types";
import { useFactorStore } from "@/store/modules/factor";
import { createLatestRequestGuard } from "@/utils/latest-request";
import { progressPercent } from "@/views/factor/recalc/recalc-model";
import { recalcJobTarget, recalcQueryTasks, splitRecalcJobs, statsStrip } from "./engine-status";

// 计算任务列表顶部：引擎状态、进行中的补算、近 24 小时失败的补算。补算作业随页面轮询（见 loadJobs）刷新。
const router = useRouter();
const store = useFactorStore();
const jobs = ref<RecalcJob[]>([]);
const jobsLoaded = ref(false);
const jobsError = ref("");
const gate = createLatestRequestGuard();

const split = computed(() => splitRecalcJobs(jobs.value));
const stats = computed(() => statsStrip(store.engine, split.value.running.length));

/** ListRecalcJobs 必须带 task_id：只对已启用的计算任务扇出。失败时保留上一份快照，没读到过才写明失败。 */
async function loadJobs() {
  const token = gate.begin();
  const ids = recalcQueryTasks(store.tasks);
  if (!ids.length) {
    jobs.value = [];
    jobsLoaded.value = true;
    jobsError.value = "";
    return;
  }
  try {
    const results = await Promise.all(
      ids.map(taskId =>
        listRecalcJobs({ task_id: taskId, statuses: ["accepted", "running", "failed"], page: { page: 1, size: 20 } })
      )
    );
    if (!token.isLatest()) return;
    jobs.value = results.flatMap(rsp => rsp.jobs || []);
    jobsLoaded.value = true;
    jobsError.value = "";
  } catch (error) {
    if (token.isLatest() && !jobsLoaded.value) jobsError.value = error instanceof Error ? error.message : String(error);
  }
}

/** 补算所属计算任务的展示名；任务不在列表里时用任务 ID。 */
function labelOf(taskId: string) {
  const info = store.tasks.find(item => item.factor_task.task_id === taskId);
  return info ? store.taskLabel(info.factor_task) : taskId;
}

function go(job: RecalcJob) {
  void router.push(recalcJobTarget(job));
}

watch(
  () => recalcQueryTasks(store.tasks).join(","),
  () => {
    jobsLoaded.value = false;
    jobsError.value = "";
    void loadJobs();
  },
  { immediate: true }
);

defineExpose({ loadJobs });
</script>

<style scoped lang="scss">
.engine-status {
  margin-bottom: var(--moox-space-3);
}

.stats-strip {
  display: grid;
  grid-template-columns: repeat(6, minmax(0, 1fr));
  gap: var(--moox-space-3);
}

.stat {
  display: flex;
  flex-direction: column;
  gap: 2px;
  padding: var(--moox-space-2) var(--moox-space-3);
  border: 1px solid var(--color-border-2);
  border-radius: 6px;
}

.stat__label,
.stat__hint {
  color: var(--color-text-3);
  font-size: 12px;
}

.stat__value {
  font-size: 18px;
  font-weight: 600;
}

.stat__value.is-ok {
  color: rgb(var(--success-6));
}

.stat__value.is-danger {
  color: rgb(var(--danger-6));
}

.stat__value.is-muted {
  color: var(--color-text-3);
}

.recalc-line {
  display: flex;
  flex-wrap: wrap;
  align-items: baseline;
  gap: var(--moox-space-2) var(--moox-space-4);
  margin: var(--moox-space-2) 0 0;
  font-size: 13px;
}

.recalc-line.is-error,
.recalc-line__label.is-danger {
  color: rgb(var(--danger-6));
}

.recalc-line__label {
  color: var(--color-text-2);
  font-weight: 600;
}

.recalc-item {
  display: inline-flex;
  gap: var(--moox-space-1);
}

.recalc-item__text {
  color: var(--color-text-3);
}

@media (max-width: 900px) {
  .stats-strip {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }
}
</style>
