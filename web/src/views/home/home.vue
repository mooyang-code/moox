<template>
  <div class="moox-page home-page">
    <div class="moox-inner home">
      <section v-if="!selectedSpaceId" class="home-empty">
        <h2>还没有可用的空间</h2>
        <p>空间是采集、因子、策略与交易的隔离边界。先创建一个空间，再配置数据源和采集任务。</p>
        <a-button type="primary" status="success" @click="go('/settings/spaces')">新建空间</a-button>
      </section>

      <template v-else>
        <section class="banner" :class="`is-${status.tone}`">
          <div class="banner-main">
            <div class="banner-meta">
              {{ greeting }}，{{ userName }} · {{ todayText }}<template v-if="spaceName"> · {{ spaceName }}</template>
            </div>
            <div class="banner-status">
              <span class="status-icon" :class="`is-${status.tone}`"><component :is="statusIcon" /></span>
              <h2>{{ status.text }}</h2>
            </div>
            <div class="banner-facts">
              <span v-for="fact in facts" :key="fact.key" :title="fact.hint"
                >{{ fact.label }} <b :class="`is-${fact.tone}`">{{ fact.value }}</b></span
              >
            </div>
            <nav class="banner-links" aria-label="常用入口">
              <button v-for="link in quickLinks" :key="link.label" type="button" class="quick" @click="go(link.target)">
                <component :is="link.icon" />{{ link.label }}
              </button>
            </nav>
          </div>
          <aside class="health-card" aria-label="系统健康">
            <span class="health-card__label">系统健康</span>
            <div class="health-card__value">
              <strong>{{ services.percent === undefined ? "—" : `${services.percent}%` }}</strong>
              <span v-if="services.available">{{ services.healthy }}/{{ services.total }} 组件</span>
            </div>
            <div class="meter"><i :class="`is-${services.tone}`" :style="{ width: `${services.percent || 0}%` }" /></div>
            <span class="health-card__foot">{{ hosts.length }} 台主机 · {{ alertCount }} 告警</span>
            <span class="health-card__foot health-card__time"
              >更新于 {{ updatedAt || "—" }}
              <a class="refresh" :class="{ 'is-busy': refreshing }" @click="manualRefresh">刷新</a></span
            >
          </aside>
        </section>

        <section class="kpis" aria-label="关键指标">
          <button v-for="card in kpis" :key="card.key" type="button" class="kpi" :title="card.hint" @click="go(card.target)">
            <span class="kpi-label"><component :is="card.icon" :class="`is-${card.tone}`" />{{ card.label }}</span>
            <span class="kpi-value"
              >{{ card.value }}<small>{{ card.unit }}</small></span
            >
            <span class="kpi-note" :class="`is-${card.tone}`">{{ card.note }}</span>
          </button>
        </section>

        <div class="tiles">
          <section class="tile tile-strategy" aria-label="策略持仓">
            <header class="tile-head">
              <h3>
                策略持仓<small>{{ strategies.length }}</small>
              </h3>
              <span class="tile-head__side">
                <template v-if="strategies.length > 1">
                  <a aria-label="上一个实例" @click="stepStrategy(-1)"><icon-left /></a>
                  <span>{{ Math.min(strategyIndex, strategies.length - 1) + 1 }} / {{ strategies.length }}</span>
                  <a aria-label="下一个实例" @click="stepStrategy(1)"><icon-right /></a>
                </template>
                <a @click="go(resultsLink())">运行结果 →</a>
              </span>
            </header>
            <div class="tile-body">
              <p v-if="!currentStrategy" class="tile-empty">
                {{ strategyLoaded ? "还没有策略实例" : "读取中…" }}
                <a v-if="strategyLoaded" @click="go(instancesLink({ create: true }))">新建实例</a>
              </p>
              <template v-else>
                <button type="button" class="strategy-head" @click="go(currentStrategy.link)">
                  <span class="dot" :class="`is-${currentStrategy.tone}`" />
                  <b class="ellipsis">{{ currentStrategy.name }}</b>
                  <span class="strategy-state" :class="`is-${currentStrategy.tone}`">{{ currentStrategy.state.label }}</span>
                  <span class="strategy-when">最近一期 {{ currentStrategy.lastBar }}</span>
                </button>
                <p v-if="!currentStrategy.holdings.length" class="tile-empty">
                  {{ currentStrategy.enabled ? "暂无目标" : "已停用，不再产出目标" }}
                </p>
                <div v-else class="weights">
                  <template v-for="item in currentStrategy.holdings" :key="item.id">
                    <span class="ellipsis" :title="item.id">{{ item.id }}</span>
                    <div class="meter">
                      <i :class="item.short ? 'is-bad' : 'is-ok'" :style="{ width: `${item.ratio * 100}%` }" />
                    </div>
                    <span class="num">{{ item.weight }}</span>
                  </template>
                  <template v-if="currentStrategy.cash">
                    <span class="muted">现金</span>
                    <div class="meter"><i class="is-idle" :style="{ width: `${currentStrategy.cash.ratio * 100}%` }" /></div>
                    <span class="num muted">{{ currentStrategy.cash.text }}</span>
                  </template>
                </div>
              </template>
            </div>
            <footer class="tile-foot">
              <a v-if="currentStrategy && currentStrategy.more" @click="go(currentStrategy.link)"
                >另有 {{ currentStrategy.more }} 个标的</a
              >
            </footer>
          </section>

          <section class="tile tile-attention" aria-label="需要处理">
            <header class="tile-head">
              <h3>
                需要处理<small v-if="attention.length" class="is-warn">{{ attention.length }}</small>
              </h3>
              <a @click="go('/ops/monitor')">监控 →</a>
            </header>
            <div class="tile-body">
              <div v-if="!attention.length" class="all-clear">
                <component :is="statusIcon" :class="`is-${status.tone}`" />
                <span>{{ overview || monitorError ? (monitorError ? "监控数据暂不可用" : "一切正常") : "读取中…" }}</span>
                <small v-if="updatedAt">最近检查 {{ updatedAt }}</small>
              </div>
              <button
                v-for="item in foldedAttention.shown"
                :key="item.key"
                type="button"
                class="line attention-line"
                :title="item.detail"
                @click="go(item.target)"
              >
                <span class="dot" :class="`is-${item.tone}`" />
                <span class="ellipsis"
                  >{{ item.title }}<em v-if="item.detail"> · {{ item.detail }}</em></span
                >
                <span class="muted">{{ item.when || item.source }}</span>
              </button>
            </div>
            <footer class="tile-foot">
              <a v-if="foldedAttention.hidden" @click="go('/ops/monitor')">另有 {{ foldedAttention.hidden }} 项</a>
            </footer>
          </section>

          <section class="tile tile-third" aria-label="因子计算">
            <header class="tile-head">
              <h3>
                因子计算<small>{{ factors.length }}</small>
              </h3>
              <a @click="go('/factor/tasks?tab=results')">计算结果 →</a>
            </header>
            <div class="tile-body">
              <p v-if="!factors.length" class="tile-empty">{{ factorLoaded ? "当前空间没有计算任务" : "读取中…" }}</p>
              <template v-else>
                <div class="line line-head factor-cols"><span>任务</span><span>最近周期</span><span>进度</span></div>
                <button
                  v-for="row in foldedFactors.shown"
                  :key="row.setId"
                  type="button"
                  class="line factor-cols"
                  :title="`${row.label}：${row.factors} 个因子`"
                  @click="go(row.path)"
                >
                  <span class="ellipsis"><span class="dot" :class="`is-${row.tone}`" />{{ row.label }}</span>
                  <span>{{ row.lastPeriod }}</span>
                  <span :class="{ 'is-warn': row.tone !== 'ok' }">{{ row.lag }}</span>
                </button>
              </template>
            </div>
            <footer class="tile-foot">
              <a v-if="foldedFactors.hidden" @click="go('/factor/tasks')">另有 {{ foldedFactors.hidden }} 项</a>
            </footer>
          </section>

          <section class="tile tile-third" aria-label="数据新鲜度">
            <header class="tile-head">
              <h3>
                数据新鲜度<small>{{ freshness.length }}</small>
              </h3>
              <a @click="go('/collector/tasks?tab=results')">采集结果 →</a>
            </header>
            <div class="tile-body">
              <p v-if="!freshness.length" class="tile-empty">
                {{ monitorError ? "监控数据暂不可用" : overview ? "暂无数据链路观测" : "读取中…" }}
              </p>
              <button
                v-for="row in foldedFreshness.shown"
                :key="row.key"
                type="button"
                class="line fresh-cols"
                :title="row.reason || `${row.stage} · ${row.name} ${row.freq}`"
                @click="go(row.path)"
              >
                <span class="ellipsis"
                  ><span class="dot" :class="`is-${row.tone}`" /><span class="muted">{{ row.stage }} · </span>{{ row.name
                  }}<em v-if="row.freq"> · {{ row.freq }}</em></span
                >
                <span :class="{ 'is-warn': row.tone === 'warn', 'is-bad': row.tone === 'bad' }">{{ row.lag }}</span>
              </button>
            </div>
            <footer class="tile-foot">
              <a v-if="foldedFreshness.hidden" @click="go('/ops/monitor')">另有 {{ foldedFreshness.hidden }} 项</a>
            </footer>
          </section>

          <section class="tile tile-third" aria-label="主机资源">
            <header class="tile-head">
              <h3>
                主机资源<small>{{ hosts.length }}</small>
              </h3>
              <a @click="go('/ops/monitor')">服务 {{ services.available ? `${services.healthy}/${services.total}` : "—" }} →</a>
            </header>
            <div class="tile-body">
              <p v-if="!hosts.length" class="tile-empty">
                {{ monitorError ? "监控数据暂不可用" : overview ? "没有上报的主机" : "读取中…" }}
              </p>
              <template v-else>
                <div class="line line-head host-cols"><span>主机</span><span>CPU</span><span>内存</span><span>磁盘</span></div>
                <button
                  v-for="row in foldedHosts.shown"
                  :key="row.key"
                  type="button"
                  class="line host-cols"
                  @click="go(row.path)"
                >
                  <span class="ellipsis" :title="row.name"><span class="dot" :class="`is-${row.tone}`" />{{ row.name }}</span>
                  <span :class="{ 'is-warn': row.hot.cpu }">{{ row.cpu }}</span>
                  <span :class="{ 'is-warn': row.hot.memory }">{{ row.memory }}</span>
                  <span :class="{ 'is-warn': row.hot.disk }">{{ row.disk }}</span>
                </button>
              </template>
            </div>
            <footer class="tile-foot">
              <a v-if="foldedHosts.hidden" @click="go('/ops/hosts')">另有 {{ foldedHosts.hidden }} 台</a>
            </footer>
          </section>

          <section class="tile tile-wide" aria-label="最近动态">
            <header class="tile-head">
              <h3>最近动态</h3>
              <a @click="go('/ops/activity')">活动日志 →</a>
            </header>
            <div class="tile-body activity-body">
              <p v-if="!activity.length" class="tile-empty">{{ activityLoaded ? "暂无动态" : "读取中…" }}</p>
              <div v-for="row in activity" :key="row.key" class="line activity-line" :title="row.title">
                <span class="muted">{{ row.when }}</span>
                <span class="ellipsis">{{ row.title }}</span>
              </div>
            </div>
          </section>
        </div>

        <footer class="inventory" aria-label="资产">
          <a v-for="item in inventory" :key="item.label" @click="go(item.path)"
            >{{ item.label }} <b>{{ fmt(item.value) }}</b></a
          >
        </footer>
      </template>
    </div>
  </div>
</template>

<script setup lang="ts" name="Home">
import { computed, reactive, ref, watch } from "vue";
import { useRouter } from "vue-router";
import { listActivityLogs, type ActivityLogItem } from "@/api/admin/activity";
import { callControl } from "@/api/admin/http";
import { getNodeList } from "@/api/cloud-node";
import { listFactorSets } from "@/api/factor";
import type { FactorSetInfo } from "@/api/factor/types";
import { monitorApi, type HealthOverview } from "@/api/monitor";
import { listDataSources, listDatasets, listViews } from "@/api/storage/metadata";
import type { Dataset, View } from "@/api/storage/types";
import { listInstances, listStrategies, listStrategyTargets } from "@/api/strategy";
import type { Strategy, StrategyInstance, StrategyTargetSnapshot } from "@/api/strategy-types";
import { listTradingAccounts } from "@/api/trade";
import { usePolling } from "@/hooks/usePolling";
import { useSpaceStore } from "@/store/modules/space";
import { useUserInfoStore } from "@/store/modules/user-info";
import { RequestGate } from "@/utils/request-gate";
import { datasetMatchesAttribution, isLikelyFactorResultDataset } from "@/views/data/shared/module-attribution";
import { alertHref } from "@/views/ops/monitor/health-display";
import { instancesLink, replayLink, resultsLink } from "@/views/strategy/links";
import {
  TILE_LIMITS,
  activityRows,
  attentionItems,
  bannerFacts,
  factorRows,
  fold,
  freshnessRows,
  hostRows,
  kpiCards,
  statusLine,
  strategyRows,
  toneIcon,
  type HomeTarget,
  type NameBook
} from "./home-model";
import { serviceHealth } from "./service-health";

const TARGET_FETCH_LIMIT = 12;

const router = useRouter();
const spaceStore = useSpaceStore();
const userStore = useUserInfoStore();
const selectedSpaceId = computed(() => spaceStore.selectedSpaceId);
const spaceName = computed(() => spaceStore.selectedSpace?.name || "");

const nowMs = ref(Date.now());
const updatedAt = ref("");
const refreshing = ref(false);

// 空间范围的名称与资产数量：切空间时整体重读，旧空间晚到的响应由 spaceLoadGate 丢弃。
const names = reactive<NameBook>({ datasets: {}, views: {} });
const counts = reactive<Record<string, number | null>>({
  sources: null,
  rules: null,
  datasets: null,
  factorSets: null,
  factors: null,
  instances: null,
  accounts: null,
  nodes: null
});
const spaceLoadGate = new RequestGate();

// 实时部分：监控概览、因子任务、策略实例与目标、最近动态，定时刷新。
const overview = ref<HealthOverview>();
const monitorError = ref("");
const factorSets = ref<FactorSetInfo[]>([]);
const factorLoaded = ref(false);
const instances = ref<StrategyInstance[]>([]);
const targets = ref<Record<string, StrategyTargetSnapshot>>({});
const catalog = ref<Strategy[]>([]);
const strategyLoaded = ref(false);
const activityItems = ref<ActivityLogItem[]>([]);
const activityLoaded = ref(false);
const liveLoadGate = new RequestGate();

const factors = computed(() => factorRows(factorSets.value, selectedSpaceId.value || "", names, nowMs.value));
const strategies = computed(() => strategyRows(instances.value, targets.value, catalog.value, nowMs.value));
const freshness = computed(() => freshnessRows(overview.value, names, nowMs.value));
const hosts = computed(() => hostRows(overview.value?.hosts || []));
const foldedFreshness = computed(() => fold(freshness.value, TILE_LIMITS.freshness));
const foldedFactors = computed(() => fold(factors.value, TILE_LIMITS.factors));
const foldedHosts = computed(() => fold(hosts.value, TILE_LIMITS.hosts));
const foldedAttention = computed(() => fold(attention.value, TILE_LIMITS.attention));
const activity = computed(() => activityRows(activityItems.value, names, nowMs.value).slice(0, TILE_LIMITS.activity));
const attention = computed(() =>
  attentionItems({
    alerts: monitorError.value ? [] : overview.value?.alerts || [],
    strategy: { instances: instances.value, targets: targets.value, catalog: catalog.value },
    factors: factors.value,
    freshness: freshness.value,
    names,
    nowMs: nowMs.value,
    alertTarget: alert => alertHref(alert).replace(/^#/, "")
  })
);
const status = computed(() =>
  statusLine({ attention: attention.value, overview: overview.value, monitorError: monitorError.value })
);
const alertCount = computed(() => (monitorError.value ? "—" : String(overview.value?.summary?.alert_count ?? 0)));
const statusIcon = computed(() => toneIcon(status.value.tone));
const services = computed(() => serviceHealth(overview.value, monitorError.value));
const facts = computed(() =>
  bannerFacts({ strategies: strategies.value, factors: factors.value, freshness: freshness.value, nowMs: nowMs.value })
);
const kpis = computed(() =>
  kpiCards({
    strategies: strategies.value,
    strategyLoaded: strategyLoaded.value,
    factors: factors.value,
    factorLoaded: factorLoaded.value,
    freshness: freshness.value,
    alertCount: monitorError.value ? undefined : overview.value?.summary?.alert_count,
    monitorError: monitorError.value,
    nowMs: nowMs.value
  })
);

// 策略磁贴一次只显示一个实例，多个实例左右切换，磁贴高度不变。
const strategyIndex = ref(0);
const currentStrategy = computed(() => strategies.value[Math.min(strategyIndex.value, Math.max(0, strategies.value.length - 1))]);
function stepStrategy(delta: number) {
  const total = strategies.value.length;
  if (total > 1) strategyIndex.value = (strategyIndex.value + delta + total) % total;
}

const userName = computed(() => userStore.account.user?.userName || userStore.account.user?.nickName || "Trader");
const greeting = computed(() => {
  const hour = new Date(nowMs.value).getHours();
  return hour < 6 ? "凌晨好" : hour < 12 ? "早上好" : hour < 18 ? "下午好" : "晚上好";
});

const todayText = computed(() => {
  const date = new Date(nowMs.value);
  const weekday = ["日", "一", "二", "三", "四", "五", "六"][date.getDay()];
  return `${date.getMonth() + 1} 月 ${date.getDate()} 日 星期${weekday}`;
});

const quickLinks: { label: string; icon: string; target: HomeTarget }[] = [
  { label: "K 线浏览", icon: "icon-bar-chart", target: "/collector/tasks?tab=results" },
  { label: "因子结果", icon: "icon-experiment", target: "/factor/tasks?tab=results" },
  { label: "策略结果", icon: "icon-compass", target: resultsLink() },
  { label: "策略回放", icon: "icon-history", target: replayLink() },
  { label: "交易账户", icon: "icon-safe", target: "/trading/accounts" },
  { label: "监控告警", icon: "icon-notification", target: "/ops/monitor" }
];

const inventory = computed(() => [
  { label: "数据源", value: counts.sources, path: "/data/sources" },
  { label: "采集任务", value: counts.rules, path: "/collector/tasks" },
  { label: "采集结果", value: counts.datasets, path: "/collector/tasks?tab=results" },
  { label: "因子", value: counts.factors, path: "/factor/definitions" },
  { label: "计算任务", value: counts.factorSets, path: "/factor/tasks" },
  { label: "策略实例", value: counts.instances, path: "/strategy/tasks" },
  { label: "执行账户", value: counts.accounts, path: "/trading/accounts" },
  { label: "云节点", value: counts.nodes, path: "/collector/tasks?tab=executors" }
]);

function fmt(value: number | null | undefined) {
  return value === null || value === undefined ? "—" : String(value);
}

function go(target: HomeTarget) {
  void router.push(target);
}

function stamp() {
  nowMs.value = Date.now();
  updatedAt.value = new Date(nowMs.value).toLocaleTimeString("zh-CN", { hour12: false });
}

async function listAllDatasets(spaceId: string) {
  const items: Dataset[] = [];
  for (let pageNo = 1; pageNo <= 20; pageNo += 1) {
    const rsp = await listDatasets({ space_id: spaceId, page: { page: pageNo, size: 500 } });
    items.push(...(rsp.datasets || []));
    if (!rsp.page_result?.has_more || !(rsp.datasets || []).length) break;
  }
  return items;
}

async function listAllViews(spaceId: string) {
  const items: View[] = [];
  for (let pageNo = 1; pageNo <= 20; pageNo += 1) {
    const rsp = await listViews({ space_id: spaceId, page: { page: pageNo, size: 500 } });
    items.push(...(rsp.views || []));
    if (!rsp.page_result?.has_more || !(rsp.views || []).length) break;
  }
  return items;
}

/** 空间范围：数据集 / View 名称、资产数量与策略定义名称。单项失败不影响其它项。 */
async function loadSpaceScoped() {
  const token = spaceLoadGate.next();
  const spaceId = selectedSpaceId.value;
  if (!spaceId) return;
  const isCurrent = () => spaceLoadGate.isCurrent(token) && selectedSpaceId.value === spaceId;
  const page = { page: 1, size: 1 };
  await Promise.allSettled([
    Promise.all([listAllDatasets(spaceId), listAllViews(spaceId)]).then(([datasets, views]) => {
      if (!isCurrent()) return;
      names.datasets = Object.fromEntries(datasets.map(item => [item.dataset_id, item.name || item.dataset_id]));
      names.views = Object.fromEntries(views.map(item => [item.view_id, item.name || item.view_id]));
      counts.datasets = datasets.filter(
        item =>
          !isLikelyFactorResultDataset(item) &&
          datasetMatchesAttribution(item, {
            ownerModules: ["collector"],
            datasetRoles: ["raw_collection", "import"],
            includeUnowned: true
          })
      ).length;
    }),
    listDataSources({ space_id: spaceId, page }).then(rsp => {
      if (isCurrent()) counts.sources = Number(rsp.page_result?.total ?? rsp.data_sources?.length ?? 0);
    }),
    callControl<{ space_id: string; page: { page: number; size: number } }, { tasks?: unknown[]; page?: { total?: number } }>(
      "collector",
      "GetTaskList",
      { space_id: spaceId, page: { page: 1, size: 1 } }
    ).then(rsp => {
      if (isCurrent()) counts.rules = Number(rsp.page?.total) || rsp.tasks?.length || 0;
    }),
    listTradingAccounts({ page: { page: 1, size: 1 } }).then(rsp => {
      if (isCurrent()) counts.accounts = Number(rsp.page_result?.total ?? rsp.accounts?.length ?? 0);
    }),
    getNodeList({ page: 1, page_size: 1 }).then(({ items, total }) => {
      if (isCurrent()) counts.nodes = total || items.length;
    }),
    listStrategies({ page: 1, page_size: 200 }).then(rsp => {
      if (isCurrent()) catalog.value = rsp.items;
    })
  ]);
}

/**
 * 实时部分：监控概览、因子任务、策略实例与最新目标、最近动态。
 * 动态多取一些（30 条）：“新增 0、移除 0”的无变化记录会被过滤掉，仍要凑满磁贴的固定条数。
 */
async function loadLive() {
  const token = liveLoadGate.next();
  const spaceId = selectedSpaceId.value;
  if (!spaceId) return;
  const isCurrent = () => liveLoadGate.isCurrent(token) && selectedSpaceId.value === spaceId;
  await Promise.allSettled([
    monitorApi
      .getOverview({ space_id: spaceId })
      .then(result => {
        if (!isCurrent()) return;
        overview.value = result.overview;
        monitorError.value = result.overview ? "" : "Monitor 未返回健康概览";
      })
      .catch(error => {
        if (isCurrent()) monitorError.value = error instanceof Error ? error.message : String(error);
      }),
    listFactorSets({ page: { page: 1, size: 500 } }).then(rsp => {
      if (!isCurrent()) return;
      factorSets.value = rsp.factor_sets ?? [];
      const own = factorSets.value.filter(item => item.factor_set.space_id === spaceId);
      counts.factorSets = own.length;
      counts.factors = own.reduce((total, item) => total + (item.members ?? []).filter(m => m.status === "enabled").length, 0);
      factorLoaded.value = true;
    }),
    loadStrategies(isCurrent),
    listActivityLogs({ space_id: spaceId, page: { page: 1, size: 30 } }).then(rsp => {
      if (!isCurrent()) return;
      activityItems.value = rsp.items ?? [];
      activityLoaded.value = true;
    })
  ]);
  if (isCurrent()) stamp();
}

async function loadStrategies(isCurrent: () => boolean) {
  const page = await listInstances({ page: 1, page_size: 200 });
  if (!isCurrent()) return;
  const enabled = page.items.filter(item => item.enabled).slice(0, TARGET_FETCH_LIMIT);
  const settled = await Promise.allSettled(enabled.map(item => listStrategyTargets(item.instance_id)));
  if (!isCurrent()) return;
  const next: Record<string, StrategyTargetSnapshot> = {};
  settled.forEach((result, index) => {
    if (result.status === "fulfilled") next[enabled[index].instance_id] = result.value;
  });
  instances.value = page.items;
  targets.value = next;
  counts.instances = page.page.total;
  strategyLoaded.value = true;
}

async function manualRefresh() {
  refreshing.value = true;
  try {
    await Promise.all([loadSpaceScoped(), loadLive()]);
  } finally {
    refreshing.value = false;
  }
}

function resetSpace() {
  Object.keys(counts).forEach(key => (counts[key] = null));
  names.datasets = {};
  names.views = {};
  overview.value = undefined;
  monitorError.value = "";
  factorSets.value = [];
  factorLoaded.value = false;
  instances.value = [];
  targets.value = {};
  catalog.value = [];
  strategyLoaded.value = false;
  activityItems.value = [];
  activityLoaded.value = false;
  updatedAt.value = "";
  strategyIndex.value = 0;
}

usePolling(loadLive, 30_000, () => Boolean(selectedSpaceId.value));
watch(
  selectedSpaceId,
  () => {
    resetSpace();
    void loadSpaceScoped();
    void loadLive();
  },
  { immediate: true }
);
</script>

<style lang="scss" scoped>
// 状态色：正常绿、需关注橙、异常红、无观测灰；界面不用细线装饰，层次靠底色与留白。
.is-ok {
  --tone: rgb(var(--green-6));
  --tone-bg: rgb(var(--green-1));
}

.is-warn {
  --tone: rgb(var(--orange-6));
  --tone-bg: rgb(var(--orange-1));
}

.is-bad {
  --tone: rgb(var(--red-6));
  --tone-bg: rgb(var(--red-1));
}

.is-idle {
  --tone: var(--color-text-3);
  --tone-bg: var(--color-fill-2);
}

.home {
  display: grid;
  gap: var(--moox-space-3);
  font-variant-numeric: tabular-nums;
}

a {
  color: rgb(var(--primary-6));
  cursor: pointer;
}

.muted {
  color: var(--color-text-3);
}

.ellipsis {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.num {
  text-align: right;
}

.dot {
  display: inline-block;
  flex: none;
  width: 7px;
  height: 7px;
  margin-right: 6px;
  border-radius: 50%;
  background: var(--tone, var(--color-text-4));
  vertical-align: 1px;
}

.meter {
  height: 6px;
  overflow: hidden;
  border-radius: 3px;
  background: var(--color-fill-2);
}

.meter i {
  display: block;
  height: 100%;
  border-radius: 3px;
  background: var(--tone, var(--color-text-4));
}

.home-empty {
  max-width: 560px;
  padding: var(--moox-space-6) 0;
}

.home-empty h2 {
  margin: 0 0 var(--moox-space-2);
  font-size: 18px;
}

.home-empty p {
  margin: 0 0 var(--moox-space-4);
  color: var(--color-text-2);
}

/* Banner：浅底色整块，状态用图标与文字颜色表达，不画边框与色条 */
.banner {
  display: grid;
  grid-template-columns: minmax(0, 1fr) 190px;
  gap: var(--moox-space-4);
  padding: var(--moox-space-4);
  border-radius: 8px;
  background: var(--color-fill-1);
}

.banner-main {
  display: flex;
  min-width: 0;
  flex-direction: column;
  gap: var(--moox-space-2);
}

.banner-meta {
  color: var(--color-text-3);
  font-size: 12px;
}

.banner-status {
  display: flex;
  align-items: center;
  gap: 10px;
}

.banner-status h2 {
  margin: 0;
  font-size: 22px;
  font-weight: 600;
}

.status-icon {
  display: inline-flex;
  font-size: 26px;
  color: var(--tone);
}

.banner-facts {
  display: flex;
  flex-wrap: wrap;
  gap: 4px 20px;
  color: var(--color-text-2);
  font-size: 13px;
}

.banner-facts b {
  color: var(--tone, var(--color-text-1));
  font-weight: 600;
}

.banner-links {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
  margin-top: 2px;
}

.quick {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  height: 28px;
  padding: 0 12px;
  border: 0;
  border-radius: 6px;
  background: var(--color-bg-2);
  color: var(--color-text-2);
  font-size: 12px;
  cursor: pointer;
}

.quick:hover {
  color: rgb(var(--primary-6));
}

.health-card {
  display: flex;
  flex-direction: column;
  justify-content: space-between;
  gap: 6px;
  padding: 12px 14px;
  border-radius: 8px;
  background: var(--color-bg-2);
}

.health-card__label,
.health-card__foot {
  color: var(--color-text-3);
  font-size: 12px;
}

.health-card__value {
  display: flex;
  align-items: baseline;
  gap: 6px;
}

.health-card__value strong {
  font-size: 26px;
  font-weight: 600;
}

.health-card__value span {
  color: var(--color-text-3);
  font-size: 12px;
}

.health-card__time {
  display: flex;
  justify-content: space-between;
}

.refresh.is-busy {
  pointer-events: none;
  opacity: 0.5;
}

/* 指标卡：固定高度，图标只做小号线条，状态色只落在图标与说明文字 */
.kpis {
  display: grid;
  grid-template-columns: repeat(4, minmax(0, 1fr));
  gap: var(--moox-space-3);
}

.kpi {
  display: flex;
  height: 84px;
  flex-direction: column;
  justify-content: space-between;
  padding: 12px 14px;
  border: 0;
  border-radius: 8px;
  background: var(--color-bg-2);
  box-shadow: 0 0 0 1px var(--color-neutral-3);
  color: inherit;
  text-align: left;
  cursor: pointer;
}

.kpi:hover {
  background: var(--color-fill-1);
}

.kpi-label {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  color: var(--color-text-2);
  font-size: 12px;
}

.kpi-label svg {
  font-size: 16px;
  color: var(--tone);
}

.kpi-value {
  font-size: 22px;
  font-weight: 600;
  line-height: 1.1;
}

.kpi-value small {
  color: var(--color-text-3);
  font-size: 12px;
  font-weight: 400;
}

.kpi-note {
  overflow: hidden;
  color: var(--color-text-3);
  font-size: 12px;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.kpi-note.is-warn,
.kpi-note.is-bad {
  color: var(--tone);
}

/* 磁贴：12 栅格，每个磁贴固定高度；超出内容折叠成“另有 N 项”，不撑高 */
.tiles {
  display: grid;
  grid-template-columns: repeat(12, minmax(0, 1fr));
  gap: var(--moox-space-3);
}

.tile {
  display: flex;
  min-width: 0;
  flex-direction: column;
  overflow: hidden;
  border-radius: 8px;
  background: var(--color-bg-2);
  box-shadow: 0 0 0 1px var(--color-neutral-3);
}

.tile-strategy {
  grid-column: span 7;
  height: 232px;
}

.tile-attention {
  grid-column: span 5;
  height: 232px;
}

.tile-third {
  grid-column: span 4;
  height: 188px;
}

.tile-wide {
  grid-column: span 12;
  height: 136px;
}

.tile-head {
  display: flex;
  align-items: baseline;
  justify-content: space-between;
  padding: 12px 14px 4px;
  font-size: 12px;
}

.tile-head h3 {
  margin: 0;
  font-size: 14px;
  font-weight: 600;
}

.tile-head h3 small {
  margin-left: 6px;
  color: var(--color-text-3);
  font-size: 12px;
  font-weight: 400;
}

.tile-head h3 small.is-warn {
  color: var(--tone);
  font-weight: 600;
}

.tile-head__side {
  display: inline-flex;
  align-items: baseline;
  gap: 8px;
  color: var(--color-text-3);
}

.tile-body {
  flex: 1;
  min-height: 0;
  padding: 2px 14px;
  overflow: hidden;
}

.tile-foot {
  height: 28px;
  padding: 6px 14px 0;
  font-size: 12px;
}

.tile-empty {
  margin: 8px 0;
  color: var(--color-text-3);
  font-size: 13px;
}

/* 行：固定行高，整行可点；悬停只换底色 */
.line {
  display: grid;
  width: calc(100% + 12px);
  height: 28px;
  align-items: center;
  gap: 8px;
  margin: 0 -6px;
  padding: 0 6px;
  border: 0;
  border-radius: 4px;
  background: transparent;
  color: inherit;
  font: inherit;
  font-size: 13px;
  text-align: left;
}

button.line {
  cursor: pointer;
}

button.line:hover {
  background: var(--color-fill-1);
}

.line em {
  color: var(--color-text-3);
  font-style: normal;
}

.line-head {
  color: var(--color-text-3);
  font-size: 12px;
}

.is-warn.num,
.line .is-warn {
  color: var(--tone);
}

.line .is-bad {
  color: var(--tone);
}

.strategy-head {
  display: flex;
  width: calc(100% + 12px);
  height: 28px;
  align-items: center;
  gap: 4px;
  margin: 0 -6px 6px;
  padding: 0 6px;
  border: 0;
  border-radius: 4px;
  background: transparent;
  color: inherit;
  font: inherit;
  font-size: 13px;
  text-align: left;
  cursor: pointer;
}

.strategy-head:hover {
  background: var(--color-fill-1);
}

.strategy-head b {
  min-width: 0;
  font-weight: 600;
}

.strategy-state {
  margin-left: 6px;
  color: var(--tone);
  font-size: 12px;
}

.strategy-when {
  margin-left: auto;
  color: var(--color-text-3);
  font-size: 12px;
  white-space: nowrap;
}

.weights {
  display: grid;
  grid-template-columns: 96px minmax(0, 1fr) 52px;
  align-items: center;
  gap: 8px 10px;
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  font-size: 12px;
}

.attention-line {
  grid-template-columns: minmax(0, 1fr) auto;
}

.attention-line > .dot {
  display: none;
}

.all-clear {
  display: flex;
  height: 100%;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: 6px;
  color: var(--color-text-2);
  font-size: 13px;
}

.all-clear svg {
  font-size: 28px;
  color: var(--tone);
}

.all-clear small {
  color: var(--color-text-3);
}

.factor-cols {
  grid-template-columns: minmax(0, 1fr) 64px 84px;
}

.fresh-cols {
  grid-template-columns: minmax(0, 1fr) 92px;
  white-space: nowrap;
}

.fresh-cols > :last-child {
  text-align: right;
}

.host-cols {
  grid-template-columns: minmax(0, 1fr) 38px 38px 38px;
}

.activity-body {
  display: grid;
  grid-auto-flow: column;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  grid-template-rows: repeat(3, 28px);
  gap: 0 32px;
  align-content: start;
}

.activity-line {
  grid-template-columns: 84px minmax(0, 1fr);
  font-size: 12px;
  white-space: nowrap;
}

.inventory {
  display: flex;
  flex-wrap: wrap;
  gap: 6px 22px;
  font-size: 12px;
}

.inventory a {
  color: var(--color-text-3);
}

.inventory a:hover {
  color: rgb(var(--primary-6));
}

.inventory b {
  margin-left: 2px;
  color: var(--color-text-1);
  font-weight: 600;
}

@media (max-width: 1100px) {
  .banner {
    grid-template-columns: minmax(0, 1fr);
  }

  .kpis {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }

  .tile-strategy,
  .tile-attention,
  .tile-third {
    grid-column: span 12;
  }

  .activity-body {
    grid-auto-flow: row;
    grid-template-columns: minmax(0, 1fr);
    grid-template-rows: none;
  }

  .tile-wide {
    height: auto;
    min-height: 136px;
  }
}

@media (max-width: 640px) {
  .kpis {
    grid-template-columns: minmax(0, 1fr);
  }
}
</style>
