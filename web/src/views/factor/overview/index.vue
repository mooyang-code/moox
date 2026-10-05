<template>
  <div class="moox-page factor-overview-page">
    <div class="moox-inner">
      <div class="page-head">
        <div class="page-head__title">
          <h2>因子总览</h2>
          <InfoTip text="只读总览：看计算任务是否健康、有哪些要处理的事项；所有操作都跳转到对应页面完成。" />
        </div>
        <a-button :loading="refreshing" @click="refresh">
          <template #icon><icon-refresh /></template>
          刷新
        </a-button>
      </div>

      <a-alert v-if="!spaceId" type="warning" show-icon>请先在顶部选择空间</a-alert>
      <template v-else>
        <a-alert v-if="store.loadError" type="error" show-icon class="overview-alert">
          {{ store.loadError }}
          <template #action><a-button size="mini" @click="reload">重试</a-button></template>
        </a-alert>

        <div class="stats-strip" role="list">
          <div v-for="item in stats" :key="item.key" class="stat" role="listitem">
            <span class="stat__label">{{ item.label }}</span>
            <span class="stat__value" :class="`is-${item.tone}`">{{ item.value }}</span>
            <span class="stat__hint">{{ item.hint }}</span>
          </div>
        </div>

        <div class="overview-columns">
          <section class="overview-col" aria-label="计算任务">
            <h3>计算任务</h3>
            <a-empty v-if="!cards.length && !store.loading" description="当前空间没有计算任务">
              <template #extra><a-button @click="go({ path: '/factor/tasks', query: {} })">前往计算任务</a-button></template>
            </a-empty>
            <article v-for="card in cards" :key="card.setId" class="task-card">
              <header class="task-card__head">
                <a-link class="task-card__title" @click="go(card.links.detail)">{{ card.title }}</a-link>
                <a-tag size="small" :color="card.health.color">{{ card.health.label }}</a-tag>
              </header>
              <div class="task-card__chips">
                <span v-if="!card.chips.length" class="task-card__empty">还没有因子</span>
                <a-tag v-for="chip in card.chips" :key="chip.factorId" size="small" :color="chip.color">
                  {{ chip.factorId }} · {{ chip.label }}
                </a-tag>
              </div>
              <footer class="task-card__links">
                <a-link @click="go(card.links.results)">结果</a-link>
                <a-link @click="go(card.links.recalc)">补算</a-link>
                <a-link @click="go(card.links.factors)">因子</a-link>
              </footer>
            </article>
          </section>

          <div class="overview-col">
            <section aria-label="待处理">
              <h3>待处理</h3>
              <a-empty v-if="!pending.length" description="没有待处理事项" />
              <ul v-else class="side-list">
                <li v-for="item in pending" :key="`${item.kind}-${item.setId}-${item.target.query.job || ''}`">
                  <a-link @click="go(item.target)">{{ item.title }}</a-link>
                  <span class="side-list__text">{{ item.text }}</span>
                </li>
              </ul>
            </section>

            <section aria-label="进行中的补算">
              <h3>进行中的补算</h3>
              <a-empty v-if="!running.length" description="没有进行中的补算" />
              <ul v-else class="side-list">
                <li v-for="job in running" :key="job.job_id">
                  <a-link @click="go(recalcTarget(job))">{{ job.factor_ids.join(", ") || "全部已启用因子" }}</a-link>
                  <a-progress :percent="progressPercent(job) / 100" size="small" />
                </li>
              </ul>
            </section>
          </div>
        </div>
      </template>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, ref, watch } from "vue";
import { useRouter } from "vue-router";
import { listRecalcJobs } from "@/api/factor";
import type { FactorSet, FactorSetInfo, RecalcJob } from "@/api/factor/types";
import { usePolling } from "@/hooks/usePolling";
import { RequestGate } from "@/utils/request-gate";
import InfoTip from "@/views/factor/shared/info-tip.vue";
import { useFactorScope } from "@/views/factor/shared/use-factor-scope";
import { progressPercent } from "@/views/factor/recalc/recalc-model";
import { pendingItems, recalcQuerySets, splitRecalcJobs, statsStrip, taskCards, type RouteTarget } from "./overview-model";

defineOptions({ name: "FactorOverview" });

const router = useRouter();
const { store, spaceId, reload } = useFactorScope();

const jobs = ref<RecalcJob[]>([]);
const refreshing = ref(false);
const gate = new RequestGate();

const labelOf = (set: FactorSet) => store.setLabel(set);
const sets = computed<FactorSetInfo[]>(() => store.sets);
const cards = computed(() => taskCards(sets.value, labelOf));
const split = computed(() => splitRecalcJobs(jobs.value));
const running = computed(() => split.value.running);
const pending = computed(() => pendingItems(sets.value, split.value.failed, labelOf));
const stats = computed(() => statsStrip(store.engine, running.value.length));

/** ListRecalcJobs 必须带 set_id：只对已启用的计算任务扇出，页面失活时不轮询。 */
async function loadJobs() {
  const token = gate.next();
  const ids = recalcQuerySets(sets.value);
  if (!ids.length) {
    jobs.value = [];
    return;
  }
  try {
    const results = await Promise.all(
      ids.map(setId =>
        listRecalcJobs({ set_id: setId, statuses: ["accepted", "running", "failed"], page: { page: 1, size: 20 } })
      )
    );
    if (gate.isCurrent(token)) jobs.value = results.flatMap(rsp => rsp.jobs || []);
  } catch {
    // 保留上一份快照，下一个轮询周期重试。
  }
}

async function refresh() {
  refreshing.value = true;
  try {
    await Promise.all([store.reload(), store.refreshEngine(), loadJobs()]);
  } finally {
    refreshing.value = false;
  }
}

function go(target: RouteTarget) {
  void router.push(target);
}

function recalcTarget(job: RecalcJob): RouteTarget {
  return { path: "/factor/tasks", query: { tab: "recalc", set: job.set_id, job: job.job_id } };
}

usePolling(loadJobs, 10_000, () => Boolean(spaceId.value));
watch(
  () => recalcQuerySets(sets.value).join(","),
  () => void loadJobs(),
  { immediate: true }
);
</script>

<style scoped lang="scss">
@use "../shared/factor-page.scss";

.overview-alert {
  margin-bottom: var(--moox-space-3);
}

.stats-strip {
  display: grid;
  grid-template-columns: repeat(4, minmax(0, 1fr));
  gap: var(--moox-space-3);
  margin-bottom: var(--moox-space-4);
}

.stat {
  display: flex;
  flex-direction: column;
  gap: 2px;
  padding: var(--moox-space-3);
  border: 1px solid var(--color-border-2);
  border-radius: 6px;
}

.stat__label,
.stat__hint {
  color: var(--color-text-3);
  font-size: 12px;
}

.stat__value {
  font-size: 22px;
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

.overview-columns {
  display: grid;
  grid-template-columns: minmax(0, 3fr) minmax(0, 2fr);
  gap: var(--moox-space-4);
  align-items: start;
}

.overview-col {
  display: flex;
  min-width: 0;
  flex-direction: column;
  gap: var(--moox-space-3);
}

.overview-col h3 {
  margin: 0 0 var(--moox-space-2);
  font-size: 15px;
  font-weight: 600;
}

.task-card {
  padding: var(--moox-space-3);
  border: 1px solid var(--color-border-2);
  border-radius: 6px;
}

.task-card + .task-card {
  margin-top: var(--moox-space-3);
}

.task-card__head {
  display: flex;
  align-items: center;
  gap: var(--moox-space-2);
}

.task-card__title {
  font-weight: 600;
}

.task-card__chips {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
  margin: var(--moox-space-2) 0;
}

.task-card__empty {
  color: var(--color-text-3);
  font-size: 12px;
}

.task-card__links {
  display: flex;
  gap: var(--moox-space-4);
  font-size: 13px;
}

.side-list {
  margin: 0;
  padding: 0;
  list-style: none;
}

.side-list li {
  display: flex;
  flex-direction: column;
  gap: 2px;
  padding: 8px 0;
  border-bottom: 1px solid var(--color-border-2);
}

.side-list__text {
  color: var(--color-text-3);
  font-size: 12px;
}

@media (max-width: 900px) {
  .stats-strip {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }

  .overview-columns {
    grid-template-columns: minmax(0, 1fr);
  }
}
</style>
