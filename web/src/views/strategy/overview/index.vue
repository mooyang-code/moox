<template>
  <div class="moox-page strategy-overview-page">
    <div class="moox-inner">
      <div class="page-head">
        <div class="page-head__title">
          <h2>策略总览</h2>
          <InfoTip text="只读总览：看各实例最新目标是否有效、有哪些需要处理的事项；操作都跳转到对应页面完成。" />
          <span v-if="readAt" class="cell-sub">状态读取：{{ readAt }}</span>
        </div>
        <a-button :loading="loading" @click="loadAll">
          <template #icon><icon-refresh /></template>
          刷新
        </a-button>
      </div>

      <a-alert v-if="!spaceId" type="warning" show-icon>请先在顶部选择空间</a-alert>
      <template v-else>
        <a-alert v-if="loadError" type="error" show-icon class="page-alert">
          {{ loadError }}
          <template #action><a-button size="mini" @click="loadAll">重试</a-button></template>
        </a-alert>

        <div class="stats-strip" role="list">
          <div v-for="item in stats" :key="item.key" class="stat" role="listitem">
            <span class="stat__label">{{ item.label }}</span>
            <span class="stat__value" :class="`is-${item.tone}`">{{ item.value }}</span>
            <span class="stat__hint">{{ item.hint }}</span>
          </div>
        </div>

        <div class="overview-columns">
          <section class="overview-col" aria-label="运行实例">
            <h3>运行实例与当前目标</h3>
            <a-empty v-if="!cards.length && !loading" description="当前空间还没有策略实例">
              <template #extra>
                <a-button type="primary" status="success" @click="go(instancesLink({ create: true }))">新建实例</a-button>
              </template>
            </a-empty>
            <article v-for="card in cards" :key="card.instanceId" class="task-card" :class="{ 'is-disabled': !card.enabled }">
              <header class="task-card__head">
                <a-link class="task-card__title" @click="go(card.links.results)">{{ card.title }}</a-link>
                <StatusBadge :enabled="card.enabled" :health="card.health" />
                <a-tag v-if="card.enabled" size="small" :color="card.stateTag.color">{{ card.stateTag.label }}</a-tag>
                <a-tag v-if="card.trading" size="small" color="orange">交易模式</a-tag>
              </header>
              <div class="cell-sub mono">{{ card.subtitle }}</div>
              <div class="task-card__chips">
                <span v-if="!card.enabled" class="task-card__empty">已停用，不再产出目标</span>
                <span v-else-if="!card.chips.length" class="task-card__empty">{{
                  card.state === "zero" ? "零仓位目标" : card.state === "empty" ? "当前会话尚无结果" : "暂无可展示的目标"
                }}</span>
                <a-tag v-for="chip in card.chips" :key="chip.instrumentId" size="small" :color="chip.short ? 'red' : 'green'">
                  {{ chip.text }}
                </a-tag>
                <a-tag v-if="card.more" size="small">+{{ card.more }}</a-tag>
              </div>
              <footer class="task-card__links">
                <span v-if="card.period" class="cell-sub">{{ card.period }}</span>
                <a-link @click="go(card.links.results)">结果</a-link>
                <a-link @click="go(detailLink(card.instanceId))">详情</a-link>
                <a-link @click="go(card.links.replay)">回放</a-link>
              </footer>
            </article>
            <a-link v-if="instances.length > cards.length" @click="go(resultsLink())"
              >还有 {{ instances.length - cards.length }} 个实例，前往运行结果查看</a-link
            >
          </section>

          <div class="overview-col">
            <section aria-label="待处理">
              <h3>待处理</h3>
              <a-empty v-if="!pending.length" description="没有待处理事项" />
              <ul v-else class="side-list">
                <li v-for="item in pending" :key="`${item.kind}-${item.instanceId}`">
                  <a-link @click="go(item.target)">{{ item.title }}</a-link>
                  <span class="side-list__text">{{ item.text }}</span>
                </li>
              </ul>
            </section>

            <section aria-label="进行中的回放">
              <h3>进行中的回放</h3>
              <a-empty v-if="!running.length" description="没有进行中的回放" />
              <ul v-else class="side-list">
                <li v-for="replay in running" :key="replay.replay_id">
                  <a-link @click="go(replayLink({ replayId: replay.replay_id }))">{{
                    replaySourceLabel(replay, store.strategyCatalog)
                  }}</a-link>
                  <span class="side-list__text"
                    >{{ replayStatusLabel(replay.status) }} · {{ replayRangeLabel(replay)
                    }}<template v-if="replay.progress_time"> · 进度 {{ formatUtcTime(replay.progress_time) }}</template></span
                  >
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
import { StaleSpaceRequestError } from "@/api/admin/stale-space";
import { listInstances, listReplays, listStrategyTargets } from "@/api/strategy";
import type { Replay, StrategyInstance, StrategyTargetSnapshot } from "@/api/strategy-types";
import { usePolling } from "@/hooks/usePolling";
import { useSpaceStore } from "@/store/modules/space";
import { useStrategyStore } from "@/store/modules/strategy";
import { RequestGate } from "@/utils/request-gate";
import InfoTip from "@/views/factor/shared/info-tip.vue";
import StatusBadge from "@/views/strategy/components/strategy-status-badge.vue";
import { detailLink, instancesLink, replayLink, resultsLink, type StrategyLink } from "@/views/strategy/links";
import { activeReplays, instanceCards, overviewStats, pendingItems } from "@/views/strategy/overview/overview-model";
import { formatUtcTime, replayRangeLabel, replaySourceLabel, replayStatusLabel } from "@/views/strategy/replay/model";
import { useNowClock } from "@/views/strategy/use-now-clock";

defineOptions({ name: "StrategyOverview" });

/** 最多为多少个已启用实例读取最新目标（逐个请求）；更多的实例在「运行结果」里按需查看。 */
const TARGET_FETCH_LIMIT = 24;

const router = useRouter();
const store = useStrategyStore();
const spaceStore = useSpaceStore();
const spaceId = computed(() => spaceStore.selectedSpaceId);
const nowMs = useNowClock(10_000);

const instances = ref<StrategyInstance[]>([]);
const targets = ref<Record<string, StrategyTargetSnapshot>>({});
const replays = ref<Replay[]>([]);
const loading = ref(false);
const loadError = ref("");
const readAt = ref("");
const gate = new RequestGate();

const input = computed(() => ({
  instances: instances.value,
  targets: targets.value,
  replays: replays.value,
  catalog: store.strategyCatalog,
  nowMs: nowMs.value
}));
const stats = computed(() => overviewStats(input.value));
const cards = computed(() => instanceCards(input.value));
const pending = computed(() => pendingItems(input.value));
const running = computed(() => activeReplays(replays.value));

async function loadInstances(): Promise<StrategyInstance[]> {
  const all: StrategyInstance[] = [];
  for (let page = 1; page <= 10; page += 1) {
    const rsp = await listInstances({ page, page_size: 200 });
    all.push(...rsp.items);
    if (!rsp.items.length || all.length >= rsp.page.total) break;
  }
  return all;
}

/** 读取实例、已启用实例的最新目标与最近的回放；单个实例的目标读取失败不影响其它实例。 */
async function loadAll() {
  if (!spaceId.value) return;
  const token = gate.next();
  loading.value = true;
  loadError.value = "";
  try {
    const [nextInstances, replayPage] = await Promise.all([
      loadInstances(),
      listReplays({ page: 1, page_size: 20 }, { silent: true }).catch(() => null),
      store.strategiesComplete ? Promise.resolve() : store.loadAllStrategies(200).catch(() => undefined)
    ]);
    if (!gate.isCurrent(token)) return;
    const enabled = nextInstances.filter(item => item.enabled).slice(0, TARGET_FETCH_LIMIT);
    const settled = await Promise.allSettled(enabled.map(item => listStrategyTargets(item.instance_id)));
    if (!gate.isCurrent(token)) return;
    const nextTargets: Record<string, StrategyTargetSnapshot> = {};
    settled.forEach((result, index) => {
      if (result.status === "fulfilled") nextTargets[enabled[index].instance_id] = result.value;
    });
    instances.value = nextInstances;
    targets.value = nextTargets;
    if (replayPage) replays.value = replayPage.items;
    readAt.value = new Date().toLocaleTimeString("zh-CN", { hour12: false });
  } catch (error) {
    if (error instanceof StaleSpaceRequestError || !gate.isCurrent(token)) return;
    loadError.value = `总览读取失败：${error instanceof Error ? error.message : "未知错误"}`;
  } finally {
    if (gate.isCurrent(token)) loading.value = false;
  }
}

function go(target: StrategyLink) {
  void router.push(target);
}

usePolling(loadAll, 30_000, () => Boolean(spaceId.value));
watch(
  spaceId,
  () => {
    instances.value = [];
    targets.value = {};
    replays.value = [];
    readAt.value = "";
    void loadAll();
  },
  { immediate: true }
);
</script>

<style scoped lang="scss">
@use "../shared/strategy-page.scss";

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

.task-card.is-disabled {
  background: var(--color-fill-1);
}

.task-card__head {
  display: flex;
  flex-wrap: wrap;
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
  flex-wrap: wrap;
  align-items: center;
  gap: var(--moox-space-4);
  font-size: 13px;
}

.task-card__links .cell-sub {
  margin-right: auto;
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
