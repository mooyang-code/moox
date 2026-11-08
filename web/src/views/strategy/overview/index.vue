<template>
  <div class="moox-page strategy-overview-page">
    <div class="moox-inner">
      <div class="page-head">
        <div class="page-head__title">
          <h2>策略总览</h2>
          <InfoTip text="只读总览：看各实例最新目标是否有效、有哪些需要处理的事项；操作都跳转到对应页面完成。" />
          <span v-if="readAt" class="cell-sub">状态读取：{{ readAt }}</span>
        </div>
        <a-button :loading="loading" @click="loadAll()">
          <template #icon><icon-refresh /></template>
          刷新
        </a-button>
      </div>

      <a-alert v-if="!spaceId" type="warning" show-icon>请先在顶部选择空间</a-alert>
      <template v-else>
        <a-alert v-if="loadError" type="error" show-icon class="page-alert">
          {{ loadError }}
          <template #action><a-button size="mini" @click="loadAll()">重试</a-button></template>
        </a-alert>
        <a-alert v-if="targetFailures" type="warning" class="page-alert">
          {{ targetFailures }} 个实例的最新目标读取失败，这些实例暂时显示为没有目标。
        </a-alert>

        <div class="stats-strip" role="list">
          <div v-for="item in stats" :key="item.key" class="stat" role="listitem">
            <span class="stat__label">{{ item.label }}</span>
            <span class="stat__value" :class="loaded ? `is-${item.tone}` : ''">{{ loaded ? item.value : "—" }}</span>
            <span class="stat__hint">{{ item.hint }}</span>
          </div>
        </div>

        <div class="overview-columns">
          <section class="overview-col" aria-label="运行实例">
            <h3>运行实例与当前目标</h3>
            <a-empty v-if="loaded && !cards.length" description="当前空间还没有策略实例">
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
                <a-tooltip :content="POOL_REPLAY_DISABLED_TEXT" :disabled="!card.pool">
                  <a-link :disabled="card.pool" @click="go(card.links.replay)">回放</a-link>
                </a-tooltip>
              </footer>
            </article>
            <a-link v-if="instances.length > cards.length" @click="go(resultsLink())"
              >还有 {{ instances.length - cards.length }} 个实例，前往运行结果查看</a-link
            >
          </section>

          <div class="overview-col">
            <section aria-label="待处理">
              <h3>待处理</h3>
              <div v-if="!loaded" class="side-list__text">—</div>
              <a-empty v-else-if="!pending.length" description="没有待处理事项" />
              <ul v-else class="side-list">
                <li v-for="item in pending" :key="`${item.kind}-${item.instanceId}`">
                  <a-link @click="go(item.target)">{{ item.title }}</a-link>
                  <span class="side-list__text">{{ item.text }}</span>
                </li>
              </ul>
            </section>

            <section aria-label="进行中的回放">
              <h3>进行中的回放</h3>
              <a-alert v-if="replayError" type="warning" class="page-alert">回放读取失败：{{ replayError }}</a-alert>
              <div v-else-if="!loaded" class="side-list__text">—</div>
              <a-empty v-else-if="!running.length" description="没有进行中的回放" />
              <ul v-else class="side-list">
                <li v-for="replay in running" :key="replay.replay_id">
                  <a-link @click="go(replayLink({ replayId: replay.replay_id }))">{{
                    replaySourceLabel(replay, store.strategyCatalog)
                  }}</a-link>
                  <span class="side-list__text"
                    >{{ replayStatusLabel(replay.status) }} · {{ replayRangeLabel(replay, displayZone)
                    }}<template v-if="replay.progress_time">
                      · 进度 {{ formatInstant(replay.progress_time, displayZone) }}</template
                    ></span
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
import { listLogicalAccounts } from "@/api/trade";
import { POOL_REPLAY_DISABLED_TEXT } from "@/views/strategy/dsl";
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
import { replayRangeLabel, replaySourceLabel, replayStatusLabel } from "@/views/strategy/replay/model";
import { useNowClock } from "@/views/strategy/use-now-clock";
import { formatInstant } from "@/utils/time-zone";
import { useDisplayZone } from "@/hooks/useDisplayZone";

// 页面时间按当前空间登记的时区显示
const displayZone = useDisplayZone();

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
// 当前空间至少读到过一次：没读到时统计条显示“-”，不能显示成“需要处理 0”。
const loaded = ref(false);
const replayError = ref("");
// 读取失败的实例目标数：只提示，不当成“没有目标”。
const targetFailures = ref(0);
const readAt = ref("");
// 已暂停自动执行的组合账户（读不到时为空，不提示暂停）
const pausedAccounts = ref<Set<string>>(new Set());
const gate = new RequestGate();

const input = computed(() => ({
  instances: instances.value,
  targets: targets.value,
  replays: replays.value,
  catalog: store.strategyCatalog,
  nowMs: nowMs.value,
  pausedAccounts: pausedAccounts.value
}));
const stats = computed(() => overviewStats(input.value));
const cards = computed(() => instanceCards(input.value, displayZone.value));
const pending = computed(() => pendingItems(input.value));
const running = computed(() => activeReplays(replays.value));

async function loadInstances(silent: boolean): Promise<StrategyInstance[]> {
  const all: StrategyInstance[] = [];
  for (let page = 1; page <= 10; page += 1) {
    const rsp = await listInstances({ page, page_size: 200 }, { silent });
    all.push(...rsp.items);
    if (!rsp.items.length || all.length >= rsp.page.total) break;
  }
  return all;
}

/**
 * 读取实例、已启用实例的最新目标与最近的回放；单个实例的目标读取失败不影响其它实例。
 * 后台轮询（background）不转刷新按钮，也不先清掉错误条，避免每 30 秒闪一下；结果出来后再更新。
 */
async function loadAll(options: { background?: boolean } = {}) {
  if (!spaceId.value) return;
  const token = gate.next();
  if (!options.background) loading.value = true;
  try {
    const [nextInstances, replayPage] = await Promise.all([
      loadInstances(Boolean(options.background)),
      listReplays({ page: 1, page_size: 20 }, { silent: true }).catch(error => {
        if (gate.isCurrent(token)) replayError.value = error instanceof Error && error.message ? error.message : "请稍后重试";
        return null;
      }),
      // 策略目录只在前台加载时补读：后台刷新不重试，免得策略模块不可用时每 30 秒弹一次
      store.strategiesComplete || options.background ? Promise.resolve() : store.loadAllStrategies(200).catch(() => undefined),
      listLogicalAccounts({ page: 1, size: 500 }, { silent: true })
        .then(rsp => {
          if (gate.isCurrent(token)) {
            const paused = (rsp.logical_accounts || []).filter(item => item.automation_state === "PAUSED");
            pausedAccounts.value = new Set(paused.map(item => item.logical_account_id));
          }
        })
        .catch(() => undefined)
    ]);
    if (!gate.isCurrent(token)) return;
    const enabled = nextInstances.filter(item => item.enabled).slice(0, TARGET_FETCH_LIMIT);
    const settled = await Promise.allSettled(
      enabled.map(item => listStrategyTargets(item.instance_id, { silent: Boolean(options.background) }))
    );
    if (!gate.isCurrent(token)) return;
    const nextTargets: Record<string, StrategyTargetSnapshot> = {};
    settled.forEach((result, index) => {
      if (result.status === "fulfilled") nextTargets[enabled[index].instance_id] = result.value;
    });
    instances.value = nextInstances;
    targets.value = nextTargets;
    targetFailures.value = settled.filter(result => result.status === "rejected").length;
    if (replayPage) {
      replays.value = replayPage.items;
      replayError.value = "";
    }
    loaded.value = true;
    loadError.value = "";
    readAt.value = formatInstant(Date.now(), displayZone.value, "clock");
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

usePolling(
  () => loadAll({ background: true }),
  30_000,
  () => Boolean(spaceId.value)
);
watch(
  spaceId,
  () => {
    instances.value = [];
    targets.value = {};
    replays.value = [];
    readAt.value = "";
    loaded.value = false;
    loadError.value = "";
    replayError.value = "";
    targetFailures.value = 0;
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
