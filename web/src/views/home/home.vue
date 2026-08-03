<template>
  <div class="moox-home dashboard-shell">
    <!-- 无空间：引导 -->
    <section v-if="!selectedSpaceId" class="onboard">
      <div class="onboard-copy">
        <h1>从这里开始你的量化数据栈</h1>
        <p>
          MOOX 把行情采集、时序存储、因子计算与宽表查询串成一条链路。
          先创建一个<strong>空间</strong>，所有数据资产与采集配置都在空间内隔离管理。
        </p>
        <a-button type="primary" status="success" size="large" @click="go('/settings/spaces')"> 创建第一个空间 </a-button>
      </div>
      <ol class="onboard-steps">
        <li v-for="(step, i) in setupSteps" :key="step.title">
          <span class="step-idx">{{ i + 1 }}</span>
          <div>
            <strong>{{ step.title }}</strong>
            <p>{{ step.description }}</p>
          </div>
        </li>
      </ol>
    </section>

    <template v-else>
      <section class="dash-command">
        <div class="dash-command-main">
          <div class="dash-kicker">
            <span>{{ greeting }}，{{ displayUserName }}</span>
            <transition name="banner-slogan" mode="out-in">
              <b :key="activeSlogan.headline">{{ activeSlogan.headline }}</b>
            </transition>
          </div>
          <h1>量化系统驾驶舱</h1>
          <transition name="banner-slogan" mode="out-in">
            <p :key="activeSlogan.subtitle">{{ activeSlogan.subtitle }}</p>
          </transition>
        </div>

        <div class="health-score-card">
          <span class="score-label">系统健康度</span>
          <strong>{{ healthScore }}</strong>
          <small>/100 · {{ healthScore >= 80 ? "运行健康" : healthScore >= 60 ? "需要关注" : "存在风险" }}</small>
          <div class="score-bar">
            <span :style="{ width: `${healthScore}%` }"></span>
          </div>
        </div>
      </section>

      <section class="kpi-grid">
        <button
          v-for="item in dashboardKpis"
          :key="item.key"
          class="kpi-card"
          :class="`tone-${item.tone}`"
          @click="go(item.path)"
        >
          <span class="kpi-label">{{ item.label }}</span>
          <strong>
            {{ item.value }}
            <small v-if="item.unit">{{ item.unit }}</small>
          </strong>
          <span class="kpi-note">{{ item.note }}</span>
          <span class="kpi-delta">{{ item.delta }}</span>
        </button>
      </section>

      <div class="dashboard-grid">
        <section class="dash-card health-breakdown-card">
          <div class="dash-card-head">
            <div>
              <h2>健康度评分拆解</h2>
              <p>数据新鲜度权重最高，先发现断流，再看服务。</p>
            </div>
            <span class="dash-chip">A 权重方案</span>
          </div>
          <div class="score-breakdown">
            <div v-for="item in healthBreakdown" :key="item.key" class="score-line" :class="`tone-${item.tone}`">
              <div class="score-line-meta">
                <strong>{{ item.label }}</strong>
                <span>{{ item.note }}</span>
              </div>
              <div class="score-line-meter">
                <span :style="{ width: `${Math.round((item.score / item.max) * 100)}%` }"></span>
              </div>
              <b>{{ item.score }}/{{ item.max }}</b>
            </div>
          </div>
        </section>

        <section class="dash-card freshness-card">
          <div class="dash-card-head compact">
            <div>
              <h2>数据新鲜度</h2>
              <p>最近入库与延迟监控</p>
            </div>
            <span class="dash-chip warn">6m 延迟</span>
          </div>
          <div class="freshness-list">
            <button
              v-for="item in stalenessItems"
              :key="item.name"
              class="freshness-row"
              :class="`tone-${item.tone}`"
              @click="go('/collector/data-management?tab=views&viewTab=browse')"
            >
              <span>
                <strong>{{ item.name }}</strong>
                <small>{{ item.dataset }}</small>
              </span>
              <b>{{ item.delay }}</b>
              <em>{{ item.status }}</em>
            </button>
          </div>
        </section>

        <section class="dash-card incident-card span-2">
          <div class="dash-card-head">
            <div>
              <h2>待处理事项</h2>
              <p>把会影响日常量化运行的问题放到首页。</p>
            </div>
            <span class="dash-chip danger">{{ incidentItems.length }} 项</span>
          </div>
          <div class="incident-table">
            <button
              v-for="item in incidentItems"
              :key="item.title"
              class="incident-row"
              :class="`tone-${item.tone}`"
              @click="go(item.path)"
            >
              <span class="incident-level">{{ item.level }}</span>
              <strong>{{ item.title }}</strong>
              <span>{{ item.meta }}</span>
              <b>{{ item.action }}</b>
            </button>
          </div>
        </section>

        <section class="dash-card pipeline-card">
          <div class="dash-card-head compact">
            <div>
              <h2>数据链路</h2>
              <p>资产到视图的完整度</p>
            </div>
          </div>
          <div class="pipeline-compact">
            <button v-for="node in pipeline" :key="node.key" class="pipeline-step" @click="go(node.path)">
              <span>{{ node.label }}</span>
              <strong>{{ fmt(counts[node.key]) }}</strong>
            </button>
          </div>
        </section>

        <section class="dash-card trade-card">
          <div class="dash-card-head compact">
            <div>
              <h2>交易账户摘要</h2>
              <p>连接、订单与持仓状态</p>
            </div>
            <span class="dash-chip ok">{{ tradeSummary.online }}/{{ tradeSummary.total }} 可用</span>
          </div>
          <div class="trade-metrics">
            <div v-for="item in tradeMetrics" :key="item.label">
              <span>{{ item.label }}</span>
              <strong>{{ item.value }}</strong>
            </div>
          </div>
          <div class="account-lines">
            <button
              v-for="account in tradeAccounts"
              :key="account.name"
              class="account-line"
              :class="`tone-${account.tone}`"
              @click="go('/trading/accounts')"
            >
              <span>{{ account.name }}</span>
              <b>{{ account.status }}</b>
              <em>{{ account.detail }}</em>
            </button>
          </div>
        </section>

        <section class="dash-card span-2 collector-card">
          <div class="dash-card-head">
            <div>
              <h2>采集任务脉搏</h2>
              <p>用任务实例和最近执行状态判断链路是否在工作。</p>
            </div>
            <a-button size="small" @click="go('/collector/rules?tab=instances')">查看任务</a-button>
          </div>
          <div class="pulse-bars">
            <div v-for="bar in taskPulse" :key="bar.label" class="pulse-bar">
              <span>{{ bar.label }}</span>
              <div><i :style="{ height: `${bar.value}%` }"></i></div>
              <b>{{ bar.value }}</b>
            </div>
          </div>
        </section>

        <section class="dash-card ops-card">
          <div class="dash-card-head compact">
            <div>
              <h2>服务与资源</h2>
              <p>网关、服务、主机资源</p>
            </div>
          </div>
          <div class="service-lines">
            <button
              v-for="dep in visibleDeployments"
              :key="dep.name"
              class="service-line"
              :class="`tone-${dep.tone}`"
              @click="go('/ops/services?tab=instances')"
            >
              <span>{{ dep.name }}</span>
              <b>{{ dep.status }}</b>
              <em>{{ dep.addr }}</em>
            </button>
          </div>
          <div class="resource-lines">
            <span class="resource-caption">资源负载</span>
            <button
              v-for="host in visibleHosts"
              :key="host.name"
              class="resource-line"
              :class="`tone-${host.tone}`"
              @click="go('/settings/hosts')"
            >
              <span>{{ host.name }}</span>
              <b>CPU {{ host.cpu }}</b>
              <em>MEM {{ host.memory }}</em>
            </button>
          </div>
        </section>

        <section class="dash-card actions-card">
          <div class="dash-card-head compact">
            <div>
              <h2>高频操作</h2>
              <p>日常检查入口</p>
            </div>
          </div>
          <div class="action-grid">
            <button v-for="item in workflowLinks" :key="item.path" class="action-tile" @click="go(item.path)">
              <b>{{ item.icon }}</b>
              <span>{{ item.title }}</span>
              <small>{{ item.description }}</small>
            </button>
          </div>
        </section>
      </div>
    </template>
  </div>
</template>

<script setup lang="ts" name="Home">
import { computed, onBeforeUnmount, onMounted, reactive, ref, watch } from "vue";
import { storeToRefs } from "pinia";
import { useRouter } from "vue-router";
import { useSpaceStore } from "@/store/modules/space";
import { useUserInfoStore } from "@/store/modules/user-info";
import { listDataSources, listDatasets, listFactors, listSubjects, listViews } from "@/api/storage/metadata";
import type { Dataset, PageResult, View } from "@/api/storage/types";
import { pageResultTotal } from "@/views/data/shared/metadata-utils";
import {
  datasetMatchesAttribution,
  isLikelyFactorResultDataset,
  isLikelyFactorResultDatasetId,
  viewMatchesAttribution
} from "@/views/data/shared/module-attribution";
import { callControl } from "@/api/admin/http";
import { listServiceDeployments } from "@/api/admin/sysdeploy";
import type { ServiceDeployment } from "@/api/admin/types";
import { listAccounts } from "@/api/trade";
import { getCurrentMetrics, type HostMetrics } from "@/api/modules/host-monitor";
import { getNodeList } from "@/api/cloud-node";
import { RequestGate } from "@/utils/request-gate";

const router = useRouter();
const spaceStore = useSpaceStore();
const userInfoStore = useUserInfoStore();
const { selectedSpaceId } = storeToRefs(spaceStore);
const { account } = storeToRefs(userInfoStore);
const displayUserName = computed(() => account.value.user?.userName || account.value.user?.nickName || "Trader");

const currentHour = new Date().getHours();
const greeting = computed(() => {
  const h = currentHour;
  if (h < 6) return "凌晨好";
  if (h < 12) return "早上好";
  if (h < 18) return "下午好";
  return "晚上好";
});

const bannerSlogans = [
  { headline: "Quant. Trade. Win.", subtitle: "数字背后是逻辑，逻辑背后是优势" },
  { headline: "Code. Backtest. Deploy.", subtitle: "每一行策略代码，都是对市场的一次深刻理解" },
  { headline: "Signal. Edge. Alpha.", subtitle: "在噪声中寻找信号，在混沌中建立优势" },
  { headline: "Think. Model. Execute.", subtitle: "用系统的力量，对抗市场的随机性" },
  { headline: "Data. Strategy. Freedom.", subtitle: "让算法替你工作，让数据为你决策" },
  { headline: "Logic. Risk. Reward.", subtitle: "控制好每一次回撤，才能守住每一分收益" },
  { headline: "Predict. Position. Profit.", subtitle: "预测不是玄学，是概率与模型的艺术" },
  { headline: "Noise. Filter. Clarity.", subtitle: "过滤市场的喧嚣，只追踪真正有效的信号" },
  { headline: "Pattern. Probability. Edge.", subtitle: "重复出现的规律，就是可以被利用的优势" },
  { headline: "Market. Math. Mastery.", subtitle: "用数学丈量市场，用逻辑掌控交易" },
  { headline: "Build. Test. Evolve.", subtitle: "策略不是一成不变的，进化才是长期生存之道" },
  { headline: "Price. Volume. Truth.", subtitle: "价格和成交量，是市场留下的唯一真相" },
  { headline: "Entropy. Order. Profit.", subtitle: "在市场的混沌中，寻找短暂却真实的秩序" },
  { headline: "Asymmetry. Leverage. Compound.", subtitle: "寻找不对称的赔率，是量化交易最迷人的地方" },
  { headline: "Flow. Trend. Ride.", subtitle: "顺势而为不是妥协，是对市场规律最深的尊重" }
];

function pickRandomSloganIndex(currentIndex?: number) {
  if (bannerSlogans.length <= 1) {
    return 0;
  }

  if (currentIndex === undefined) {
    return Math.floor(Math.random() * bannerSlogans.length);
  }

  const nextIndex = Math.floor(Math.random() * (bannerSlogans.length - 1));
  return nextIndex >= currentIndex ? nextIndex + 1 : nextIndex;
}

const activeSloganIndex = ref(pickRandomSloganIndex());
const activeSlogan = computed(() => bannerSlogans[activeSloganIndex.value]);
let bannerTimer: ReturnType<typeof setInterval> | null = null;

const counts = reactive<Record<string, number | null>>({
  sources: null,
  rules: null,
  datasets: null,
  views: null,
  factors: null,
  accounts: null,
  subjects: null,
  tasks: null
});
const spaceLoadGate = new RequestGate();

const pipeline = [
  { key: "sources", stage: "01", label: "数据源", color: "#3b6fd9", path: "/data/sources" },
  { key: "rules", stage: "02", label: "采集规则", color: "#0d9488", path: "/collector/rules" },
  { key: "datasets", stage: "03", label: "数据集合", color: "#059669", path: "/collector/data-management?tab=datasets" },
  { key: "factors", stage: "04", label: "因子定义", color: "#c026d3", path: "/factor/definitions" },
  { key: "views", stage: "05", label: "数据视图", color: "#ea580c", path: "/collector/data-management?tab=views" },
  { key: "accounts", stage: "06", label: "交易账户", color: "#b45309", path: "/trading/accounts" }
];

const workflowLinks = [
  {
    title: "K 线浏览",
    description: "检查最新 bar 是否入库",
    path: "/collector/data-management?tab=views&viewTab=browse",
    icon: "K",
    tint: "rgba(59, 111, 217, 12%)"
  },
  {
    title: "视图查询",
    description: "查看数据集合生成的视图",
    path: "/collector/data-management?tab=views&viewTab=browse",
    icon: "Q",
    tint: "rgba(234, 88, 12, 12%)"
  },
  {
    title: "采集实例",
    description: "任务执行状态与失败明细",
    path: "/collector/rules?tab=instances",
    icon: "T",
    tint: "rgba(13, 148, 136, 12%)"
  },
  {
    title: "数据集合",
    description: "定义采集写入的数据契约",
    path: "/collector/data-management?tab=datasets",
    icon: "D",
    tint: "rgba(5, 150, 105, 12%)"
  },
  { title: "因子结果", description: "查看因子计算写回结果", path: "/factor/results", icon: "F", tint: "rgba(192, 38, 211, 12%)" },
  { title: "交易账户", description: "账户余额与下单通道", path: "/trading/accounts", icon: "A", tint: "rgba(180, 83, 9, 12%)" }
];

const setupSteps = [
  { title: "创建空间", description: "空间是数据资产、采集与交易的隔离边界，管理台所有请求都带空间上下文。" },
  { title: "登记数据资产", description: "配置数据源、数据对象、字段，再到数据采集里定义数据集合与视图。" },
  { title: "启动采集链路", description: "collector 按规则展开任务，经 cloudnode 下发到云节点执行写入。" },
  { title: "查询与因子", description: "用数据视图浏览 K 线；因子模块自动写回独立结果数据集合。" }
];

const nodesTotal = ref<number | null>(null);
const deployments = ref<ServiceDeployment[]>([]);
const deploymentsLoaded = ref(false);
const hosts = ref<HostMetrics[]>([]);

const nodesNote = computed(() => {
  if (nodesTotal.value === null) return "加载中";
  if (nodesTotal.value === 0) return "尚未注册节点";
  return "已登记云函数节点";
});

const healthBreakdown = computed(() => [
  { key: "freshness", label: "数据新鲜度", score: 26, max: 30, tone: "ok", note: "主力 K 线 6 分钟前入库" },
  { key: "collector", label: "采集任务健康", score: 16, max: 20, tone: "warn", note: "7 个任务需要处理" },
  { key: "nodes", label: "云节点登记", score: 15, max: 15, tone: "ok", note: "已登记云函数节点" },
  { key: "services", label: "服务部署健康", score: 14, max: 15, tone: "ok", note: "核心服务 active" },
  { key: "assets", label: "数据资产完整度", score: 8, max: 10, tone: "ok", note: "Dataset / View 已配置" },
  { key: "trade", label: "交易账户状态", score: 8, max: 10, tone: "ok", note: "5 / 6 账户可用" }
]);

const healthScore = computed(() => healthBreakdown.value.reduce((sum, item) => sum + item.score, 0));

function countOrFallback(v: number | null | undefined, fallback: number): number {
  return v === null || v === undefined ? fallback : v;
}

const dashboardKpis = computed(() => [
  {
    key: "health",
    label: "系统健康度",
    value: String(healthScore.value),
    unit: "/100",
    note: "数据与采集优先",
    delta: "+4 vs 昨日",
    tone: "ok",
    path: "/home"
  },
  {
    key: "freshness",
    label: "数据新鲜度",
    value: "6m",
    unit: "",
    note: "最新 K 线延迟",
    delta: "APT-USDT",
    tone: "ok",
    path: "/collector/data-management?tab=views&viewTab=browse"
  },
  {
    key: "tasks",
    label: "今日采集任务",
    value: fmt(counts.tasks ?? 443),
    unit: "",
    note: "规则展开实例",
    delta: "运行中 18",
    tone: "neutral",
    path: "/collector/rules?tab=instances"
  },
  {
    key: "incidents",
    label: "异常任务",
    value: "7",
    unit: "",
    note: "失败 / 超时",
    delta: "需处理",
    tone: "danger",
    path: "/collector/rules?tab=instances"
  },
  {
    key: "nodes",
    label: "云节点",
    value: String(countOrFallback(nodesTotal.value, 0)),
    unit: "",
    note: nodesNote.value,
    delta: "已登记",
    tone: "neutral",
    path: "/collector/cloudnodes"
  },
  {
    key: "services",
    label: "服务在线",
    value: String(deploymentsLoaded.value ? deployments.value.length : 28),
    unit: "",
    note: "active 部署",
    delta: "gateway ok",
    tone: "ok",
    path: "/ops/services?tab=instances"
  }
]);

const stalenessItems = [
  { name: "APT-USDT", dataset: "BINANCE spot / 1m kline", delay: "6m", status: "fresh", tone: "ok" },
  { name: "BTC-USDT", dataset: "BINANCE spot / ticker", delay: "2m", status: "fresh", tone: "ok" },
  { name: "ETH-USDT", dataset: "OKX spot / 5m kline", delay: "18m", status: "watch", tone: "warn" },
  { name: "factor.momentum", dataset: "daily factor view", delay: "48m", status: "late", tone: "danger" }
];

const incidentItems = [
  {
    level: "P1",
    title: "factor.momentum 今日未刷新",
    meta: "因子结果延迟 48m",
    action: "打开结果",
    path: "/factor/results",
    tone: "danger"
  },
  {
    level: "P2",
    title: "7 个采集实例失败",
    meta: "交易所限频 / 网络超时",
    action: "处理任务",
    path: "/collector/rules?tab=instances",
    tone: "warn"
  },
  {
    level: "P3",
    title: "1 个交易账户同步较慢",
    meta: "Binance futures 14m 未更新",
    action: "账户摘要",
    path: "/trading/accounts",
    tone: "neutral"
  }
];

const tradeSummary = computed(() => ({
  total: countOrFallback(counts.accounts, 6),
  online: 5,
  ordersToday: 128,
  failedOrders: 2,
  positions: 11,
  lastFill: "19:28:41"
}));

const tradeMetrics = computed(() => [
  { label: "账户", value: `${tradeSummary.value.online}/${tradeSummary.value.total}` },
  { label: "今日订单", value: String(tradeSummary.value.ordersToday) },
  { label: "失败订单", value: String(tradeSummary.value.failedOrders) },
  { label: "持仓", value: String(tradeSummary.value.positions) }
]);

const tradeAccounts = [
  { name: "Binance Spot", status: "online", detail: "现货 / 最近成交 19:28", tone: "ok" },
  { name: "OKX Spot", status: "online", detail: "现货 / 余额同步 3m", tone: "ok" },
  { name: "Binance Futures", status: "watch", detail: "合约 / 同步延迟 14m", tone: "warn" }
];

const taskPulse = [
  { label: "09:00", value: 42 },
  { label: "10:00", value: 66 },
  { label: "11:00", value: 58 },
  { label: "12:00", value: 74 },
  { label: "13:00", value: 81 },
  { label: "14:00", value: 63 },
  { label: "15:00", value: 88 },
  { label: "16:00", value: 71 },
  { label: "17:00", value: 93 },
  { label: "18:00", value: 78 },
  { label: "19:00", value: 84 },
  { label: "20:00", value: 69 }
];

const visibleDeployments = computed(() => {
  const fallback = [
    { name: "admin-gateway", status: "active", addr: "same-origin", tone: "ok" },
    { name: "storage-primary", status: "active", addr: ":20201", tone: "ok" },
    { name: "collector", status: "active", addr: ":11402", tone: "ok" },
    { name: "cloudnode", status: "active", addr: ":11401", tone: "ok" },
    { name: "trade", status: "watch", addr: ":11200", tone: "warn" }
  ];
  if (!deployments.value.length) return fallback;
  return deployments.value.slice(0, 5).map(dep => ({
    name: dep.service_name,
    status: dep.status,
    addr: `${dep.host}:${dep.port}`,
    tone: dep.status === "active" ? "ok" : "warn"
  }));
});

const visibleHosts = computed(() => {
  const fallback = [
    { name: "prod-main", cpu: "31%", memory: "58%", tone: "ok" },
    { name: "collector-01", cpu: "64%", memory: "71%", tone: "warn" },
    { name: "query-node", cpu: "22%", memory: "46%", tone: "ok" }
  ];
  if (!hosts.value.length) return fallback;
  return hosts.value.slice(0, 3).map(host => ({
    name: host.host_name || host.address,
    cpu: formatPercent(host.cpu?.usage),
    memory: formatPercent(host.memory?.percent),
    tone: host.status === "online" && (host.cpu?.usage ?? 0) < 80 && (host.memory?.percent ?? 0) < 85 ? "ok" : "warn"
  }));
});

function fmt(v: number | null | undefined): string {
  return v === null || v === undefined ? "—" : String(v);
}

function formatPercent(v?: number): string {
  return typeof v === "number" && Number.isFinite(v) ? `${v.toFixed(0)}%` : "—";
}

function countFrom(page?: PageResult, fallbackLength = 0): number {
  return page ? pageResultTotal(page) : fallbackLength;
}

const go = (path: string) => {
  router.push(path);
};

async function loadSpaceScoped() {
  const token = spaceLoadGate.next();
  const spaceId = selectedSpaceId.value;
  if (!spaceId) return;
  const page = { page: 1, size: 1 };
  const isCurrent = () => spaceLoadGate.isCurrent(token) && selectedSpaceId.value === spaceId;

  const jobs: Array<Promise<void>> = [
    listDataSources({ space_id: spaceId, page }).then(rsp => {
      if (isCurrent()) counts.sources = countFrom(rsp.page_result, rsp.data_sources?.length);
    }),
    loadCollectorAssetCounts(spaceId).then(result => {
      if (!isCurrent()) return;
      counts.datasets = result.datasets;
      counts.views = result.views;
    }),
    listFactors({ space_id: spaceId, page }).then(rsp => {
      if (isCurrent()) counts.factors = countFrom(rsp.page_result, rsp.factors?.length);
    }),
    listSubjects({ space_id: spaceId, page }).then(rsp => {
      if (isCurrent()) counts.subjects = countFrom(rsp.page_result, rsp.subjects?.length);
    }),
    callControl<{ space_id: string; page: { page: number; size: number } }, { rules?: unknown[]; page?: { total?: number } }>(
      "collectmgr",
      "GetTaskRuleList",
      { space_id: spaceId, page: { page: 1, size: 1 } }
    ).then(rsp => {
      if (isCurrent()) counts.rules = Number(rsp.page?.total) || rsp.rules?.length || 0;
    }),
    callControl<
      { filter: { space_id: string; page: { page: number; size: number } } },
      { instances?: unknown[]; page?: { total?: number } }
    >("collectmgr", "GetTaskInstanceList", { filter: { space_id: spaceId, page: { page: 1, size: 1 } } }).then(rsp => {
      if (isCurrent()) counts.tasks = Number(rsp.page?.total) || rsp.instances?.length || 0;
    }),
    listAccounts({ page: { page: 1, size: 1 } }).then(rsp => {
      if (isCurrent()) counts.accounts = rsp.page_result?.total ?? rsp.accounts?.length ?? 0;
    }),
    getNodeList({ page: 1, page_size: 200 }).then(({ items, total }) => {
      if (!isCurrent()) return;
      nodesTotal.value = total || items.length;
    })
  ];

  await Promise.allSettled(jobs);
}

async function loadCollectorAssetCounts(spaceId: string): Promise<{ datasets: number; views: number }> {
  const [datasetItems, viewItems] = await Promise.all([listAllDatasets(spaceId), listAllViews(spaceId)]);
  const datasetById = new Map(datasetItems.map(item => [item.dataset_id, item]));
  const datasets = datasetItems.filter(
    item =>
      !isLikelyFactorResultDataset(item) &&
      datasetMatchesAttribution(item, {
        ownerModules: ["collector"],
        datasetRoles: ["raw_collection", "import"],
        includeUnowned: true
      })
  ).length;
  const views = viewItems.filter(
    item =>
      !viewUsesLikelyFactorDataset(item, datasetById) &&
      viewMatchesAttribution(item, {
        ownerModules: ["collector"],
        viewRoles: ["collection_browse", "analysis"],
        includeUnowned: true
      })
  ).length;
  return { datasets, views };
}

async function listAllDatasets(spaceId: string) {
  const items: Dataset[] = [];
  const size = 500;
  for (let pageNo = 1; ; pageNo += 1) {
    const rsp = await listDatasets({ space_id: spaceId, page: { page: pageNo, size } });
    items.push(...(rsp.datasets || []));
    if (!rsp.page_result?.has_more || (rsp.datasets || []).length === 0) {
      return items;
    }
  }
}

async function listAllViews(spaceId: string) {
  const items: View[] = [];
  const size = 500;
  for (let pageNo = 1; ; pageNo += 1) {
    const rsp = await listViews({ space_id: spaceId, page: { page: pageNo, size } });
    items.push(...(rsp.views || []));
    if (!rsp.page_result?.has_more || (rsp.views || []).length === 0) {
      return items;
    }
  }
}

function viewUsesLikelyFactorDataset(view: View, datasetById: Map<string, Dataset>) {
  const dataset = datasetById.get(view.primary_dataset_id);
  if (dataset) {
    return isLikelyFactorResultDataset(dataset);
  }
  return isLikelyFactorResultDatasetId(view.primary_dataset_id);
}

async function loadGlobal() {
  await Promise.allSettled([
    listServiceDeployments({ status: "active", page: { page: 1, size: 50 } })
      .then(rsp => {
        deployments.value = rsp.deployments ?? [];
      })
      .finally(() => {
        deploymentsLoaded.value = true;
      }),
    getCurrentMetrics()
      .then(rsp => {
        hosts.value = (rsp.metrics ?? []).slice(0, 4);
      })
      .catch(() => {
        hosts.value = [];
      })
  ]);
}

async function refreshAll() {
  await Promise.all([loadSpaceScoped(), loadGlobal()]);
}

function resetCounts() {
  Object.keys(counts).forEach(k => {
    counts[k] = null;
  });
  nodesTotal.value = null;
}

watch(selectedSpaceId, () => {
  resetCounts();
  loadSpaceScoped();
});

onMounted(() => {
  refreshAll();
  bannerTimer = setInterval(() => {
    activeSloganIndex.value = pickRandomSloganIndex(activeSloganIndex.value);
  }, 15000);
});

onBeforeUnmount(() => {
  spaceLoadGate.next();
  if (bannerTimer) {
    clearInterval(bannerTimer);
    bannerTimer = null;
  }
});
</script>

<style lang="scss" scoped>
$mono: "SF Mono", "JetBrains Mono", "IBM Plex Mono", ui-monospace, Menlo, Consolas, monospace;
$display: "PingFang SC", "Hiragino Sans GB", "Microsoft YaHei", system-ui, sans-serif;

.moox-home {
  --home-accent: #0d9488;
  --home-accent-soft: rgba(13, 148, 136, 10%);
  --home-ink: var(--color-text-1);
  --home-muted: var(--color-text-3);

  box-sizing: border-box;
  display: flex;
  flex: 1;
  flex-direction: column;
  gap: var(--moox-space-5);
  width: 100%;
  max-width: min(1600px, 100%);
  min-height: 0;
  margin: 0 auto;
  padding: var(--moox-space-4) var(--moox-space-6) var(--moox-space-10);
  overflow-x: hidden;
  overflow-y: auto;
  font-family: $display;
}

/* ---------- 顶栏 ---------- */
.home-top {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: var(--moox-space-4);
}

.home-top-left {
  display: flex;
  align-items: center;
  gap: 10px;
}

.home-badge {
  padding: 3px var(--moox-space-2);
  border-radius: 6px;
  background: var(--home-accent-soft);
  color: var(--home-accent);
  font-family: $mono;
  font-size: 11px;
  font-weight: 700;
  letter-spacing: 0.12em;
}

.home-top-title {
  font-size: 13px;
  font-weight: 600;
  color: var(--home-muted);
}

.home-top-right {
  display: flex;
  align-items: center;
  gap: var(--moox-space-3);
}

.home-clock {
  font-family: $mono;
  font-size: 14px;
  font-variant-numeric: tabular-nums;
  color: var(--home-ink);
}

.home-clock-sub {
  font-family: $mono;
  font-size: 11px;
  color: var(--home-muted);
}

/* ---------- 无空间引导 ---------- */
.onboard {
  display: grid;
  grid-template-columns: 1.1fr 1fr;
  gap: var(--moox-space-6);
  padding: var(--moox-space-7);
  border: 1px solid var(--color-border-2);
  border-radius: 16px;
  background: radial-gradient(ellipse 80% 60% at 100% 0%, rgba(13, 148, 136, 8%), transparent 55%), var(--color-bg-2);
}

.onboard-copy h1 {
  margin: 0 0 var(--moox-space-3);
  font-size: clamp(22px, 3vw, 30px);
  font-weight: 700;
  line-height: 1.25;
  color: var(--home-ink);
}

.onboard-copy p {
  margin: 0 0 var(--moox-space-6);
  max-width: 480px;
  font-size: 14px;
  line-height: 1.75;
  color: var(--home-muted);

  strong {
    color: var(--home-accent);
    font-weight: 600;
  }
}

.onboard-steps {
  display: flex;
  flex-direction: column;
  gap: var(--moox-space-3);
  margin: 0;
  padding: 0;
  list-style: none;

  li {
    display: flex;
    gap: 14px;
    padding: 14px var(--moox-space-4);
    border: 1px solid var(--color-border-2);
    border-radius: 12px;
    background: var(--color-fill-1);
  }

  strong {
    display: block;
    font-size: 14px;
    color: var(--home-ink);
  }

  p {
    margin: 6px 0 0;
    font-size: 12px;
    line-height: 1.6;
    color: var(--home-muted);
  }
}

.step-idx {
  flex-shrink: 0;
  width: 28px;
  height: 28px;
  border-radius: 8px;
  background: var(--home-accent-soft);
  color: var(--home-accent);
  font-family: $mono;
  font-size: 13px;
  font-weight: 700;
  line-height: 28px;
  text-align: center;
}

/* ---------- 欢迎区 ---------- */
.welcome {
  display: grid;
  grid-template-columns: 1fr auto;
  gap: 28px;
  align-items: center;
  padding: 28px var(--moox-space-7);
  border: 1px solid var(--color-border-2);
  border-radius: 16px;
  background: linear-gradient(135deg, var(--color-bg-2) 0%, var(--color-fill-1) 100%);
  box-shadow: 0 1px 0 rgba(15, 23, 42, 4%);
}

.welcome-greet {
  margin: 0 0 6px;
  font-size: 14px;
  color: var(--home-muted);
}

.welcome-headline {
  margin: 0;
  font-size: clamp(20px, 2.5vw, 26px);
  font-weight: 700;
  line-height: 1.3;
  color: var(--home-ink);
}

.welcome-desc {
  margin: 10px 0 0;
  max-width: 520px;
  font-size: 13px;
  line-height: 1.7;
  color: var(--home-muted);
}

.welcome-actions {
  display: flex;
  flex-wrap: wrap;
  gap: 10px;
  margin-top: var(--moox-space-5);
}

.welcome-readiness {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 14px;
  min-width: 200px;
}

.readiness-ring {
  position: relative;
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  width: 108px;
  height: 108px;
  border-radius: 50%;
  background: conic-gradient(var(--home-accent) calc(var(--ring-pct) * 1%), var(--color-fill-3) 0);

  &::before {
    content: "";
    position: absolute;
    inset: 8px;
    border-radius: 50%;
    background: var(--color-bg-2);
  }
}

.readiness-pct {
  position: relative;
  z-index: 1;
  font-family: $mono;
  font-size: 26px;
  font-weight: 700;
  font-variant-numeric: tabular-nums;
  color: var(--home-accent);
}

.readiness-label {
  position: relative;
  z-index: 1;
  margin-top: 2px;
  font-size: 11px;
  color: var(--home-muted);
}

.readiness-list {
  margin: 0;
  padding: 0;
  list-style: none;
  width: 100%;

  li {
    display: flex;
    align-items: center;
    gap: 6px;
    padding: var(--moox-space-1) 0;
    font-size: 12px;
    color: var(--home-muted);

    &.ok {
      color: #00b42a;
    }

    :deep(svg) {
      flex-shrink: 0;
      font-size: 14px;
    }
  }
}

/* ---------- 数据链路 ---------- */
.flow-section {
  padding: var(--moox-space-5) var(--moox-space-6);
  border: 1px solid var(--color-border-2);
  border-radius: 14px;
  background: var(--color-bg-2);
}

.section-head {
  display: flex;
  align-items: baseline;
  justify-content: space-between;
  margin-bottom: var(--moox-space-4);

  h2 {
    margin: 0;
    font-size: 15px;
    font-weight: 700;
    color: var(--home-ink);
  }

  span {
    font-size: 12px;
    color: var(--home-muted);
  }
}

.flow-track {
  display: flex;
  gap: var(--moox-space-1);
  overflow-x: auto;
  padding-bottom: var(--moox-space-1);
}

.flow-node {
  position: relative;
  flex: 1;
  min-width: 120px;
  display: flex;
  flex-direction: column;
  align-items: flex-start;
  gap: 2px;
  padding: 14px var(--moox-space-4);
  border: 1px solid var(--color-border-2);
  border-radius: 12px;
  border-top: 3px solid var(--node-color);
  background: var(--color-fill-1);
  cursor: pointer;
  text-align: left;
  animation: fade-up 0.45s both;
  transition:
    border-color 0.15s,
    box-shadow 0.15s,
    transform 0.15s;

  &:hover {
    border-color: var(--node-color);
    box-shadow: 0 6px 20px rgba(15, 23, 42, 6%);
    transform: translateY(-1px);
  }
}

.flow-stage {
  font-family: $mono;
  font-size: 10px;
  font-weight: 600;
  letter-spacing: 0.1em;
  color: var(--node-color);
  opacity: 0.85;
}

.flow-count {
  font-family: $mono;
  font-size: 28px;
  font-weight: 700;
  font-variant-numeric: tabular-nums;
  line-height: 1.1;
  color: var(--home-ink);
}

.flow-name {
  font-size: 13px;
  font-weight: 600;
  color: var(--color-text-2);
}

.flow-arrow {
  position: absolute;
  right: -14px;
  top: 50%;
  z-index: 1;
  transform: translateY(-50%);
  font-size: 18px;
  color: var(--color-text-4);
  pointer-events: none;
}

@keyframes fade-up {
  from {
    opacity: 0;
    transform: translateY(6px);
  }

  to {
    opacity: 1;
    transform: translateY(0);
  }
}

/* ---------- Bento ---------- */
.bento {
  display: grid;
  grid-template-columns: 1.4fr 1fr;
  grid-template-rows: auto auto;
  gap: var(--moox-space-4);
}

.bento-card {
  padding: var(--moox-space-5) 22px;
  border: 1px solid var(--color-border-2);
  border-radius: 14px;
  background: var(--color-bg-2);
}

.bento-workflow {
  grid-row: span 2;
}

.workflow-grid {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 10px;
}

.workflow-item {
  display: flex;
  flex-direction: column;
  align-items: flex-start;
  gap: 6px;
  padding: 14px;
  border: 1px solid var(--color-border-2);
  border-radius: 12px;
  background: var(--color-fill-1);
  cursor: pointer;
  text-align: left;
  transition:
    border-color 0.15s,
    background 0.15s;

  &:hover {
    border-color: rgb(var(--primary-5));
    background: var(--color-bg-2);
  }
}

.workflow-icon {
  display: flex;
  align-items: center;
  justify-content: center;
  width: 32px;
  height: 32px;
  border-radius: 8px;
  font-family: $mono;
  font-size: 13px;
  font-weight: 700;
  color: var(--home-accent);
}

.workflow-title {
  font-size: 14px;
  font-weight: 600;
  color: var(--home-ink);
}

.workflow-desc {
  font-size: 12px;
  line-height: 1.5;
  color: var(--home-muted);
}

.ops-metrics {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 10px;
}

.ops-metric {
  display: flex;
  flex-direction: column;
  gap: var(--moox-space-1);
  padding: 14px;
  border: 1px solid var(--color-border-2);
  border-radius: 12px;
  background: var(--color-fill-1);
  cursor: pointer;
  text-align: left;
  transition: border-color 0.15s;

  &:hover {
    border-color: rgb(var(--primary-5));
  }

  strong {
    font-family: $mono;
    font-size: 24px;
    font-weight: 700;
    font-variant-numeric: tabular-nums;
    color: var(--home-ink);

    small {
      font-size: 14px;
      font-weight: 500;
      color: var(--home-muted);
    }
  }
}

.ops-label {
  font-size: 12px;
  color: var(--home-muted);
}

.ops-note {
  font-size: 11px;
  color: var(--home-muted);

  &.ok {
    color: #00b42a;
  }

  &.warn {
    color: #ff7d00;
  }
}

.asset-grid {
  display: grid;
  grid-template-columns: repeat(3, minmax(0, 1fr));
  gap: var(--moox-space-2);
}

.asset-chip {
  display: flex;
  flex-direction: column;
  gap: 2px;
  padding: var(--moox-space-3);
  border: 1px solid var(--color-border-2);
  border-radius: 10px;
  background: var(--color-fill-1);
  cursor: pointer;
  text-align: left;
  transition: background 0.15s;

  &:hover {
    background: var(--color-fill-2);
  }
}

.asset-val {
  font-family: $mono;
  font-size: 20px;
  font-weight: 700;
  font-variant-numeric: tabular-nums;
  color: var(--home-ink);
}

.asset-name {
  font-size: 12px;
  color: var(--home-muted);
}

/* ---------- 基础设施折叠 ---------- */
.infra-collapse {
  border: 1px solid var(--color-border-2);
  border-radius: 14px;
  background: var(--color-bg-2);
  overflow: hidden;

  :deep(.arco-collapse-item-header) {
    padding: 14px var(--moox-space-5);
    font-size: 13px;
    font-weight: 600;
    color: var(--home-muted);
    background: transparent;
  }

  :deep(.arco-collapse-item-content) {
    background: transparent;
  }
}

.infra-body {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: var(--moox-space-4);
  padding: 0 var(--moox-space-5) var(--moox-space-4);
}

.infra-panel {
  padding: 14px;
  border: 1px solid var(--color-border-2);
  border-radius: 10px;
  background: var(--color-fill-1);
}

.infra-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 10px;

  h3 {
    margin: 0;
    font-size: 13px;
    font-weight: 600;
    color: var(--home-ink);
  }
}

.infra-empty {
  margin: 0;
  padding: var(--moox-space-3) 0;
  font-size: 12px;
  color: var(--home-muted);
  text-align: center;
}

.svc-line,
.host-line {
  display: grid;
  grid-template-columns: 8px 1fr auto;
  gap: var(--moox-space-2);
  align-items: center;
  padding: 6px var(--moox-space-1);
  font-size: 12px;
  border-radius: 6px;

  &:hover {
    background: var(--color-fill-2);
  }
}

.host-line {
  grid-template-columns: 8px 1fr 80px 52px 52px;
}

.svc-dot {
  width: 7px;
  height: 7px;
  border-radius: 50%;

  &.ok {
    background: #00b42a;
  }

  &.warn {
    background: #ff7d00;
  }
}

.svc-name,
.host-name {
  overflow: hidden;
  font-weight: 600;
  color: var(--home-ink);
  text-overflow: ellipsis;
  white-space: nowrap;
}

.svc-kind {
  color: var(--home-muted);
}

.svc-addr {
  font-family: $mono;
  font-size: 11px;
  color: var(--color-text-2);
}

.host-bar {
  height: 5px;
  overflow: hidden;
  border-radius: 999px;
  background: var(--color-fill-3);
}

.host-bar-fill {
  display: block;
  height: 100%;
  border-radius: 999px;
  transition: width 0.35s;
}

.host-pct {
  font-family: $mono;
  font-size: 10px;
  color: var(--home-muted);
  text-align: right;
}

/* ---------- 响应式 ---------- */
@media (max-width: 1024px) {
  .onboard {
    grid-template-columns: 1fr;
  }

  .welcome {
    grid-template-columns: 1fr;
  }

  .welcome-readiness {
    flex-direction: row;
    flex-wrap: wrap;
    justify-content: flex-start;
    align-items: flex-start;
  }

  .bento {
    grid-template-columns: 1fr;
  }

  .bento-workflow {
    grid-row: auto;
  }

  .infra-body {
    grid-template-columns: 1fr;
  }
}

@media (max-width: 640px) {
  .moox-home {
    padding: var(--moox-space-3) var(--moox-space-3) var(--moox-space-8);
  }

  .home-top {
    flex-direction: column;
    align-items: flex-start;
  }

  .flow-track {
    flex-direction: column;
  }

  .flow-node {
    min-width: 0;
    width: 100%;
  }

  .flow-arrow {
    display: none;
  }

  .workflow-grid,
  .ops-metrics,
  .asset-grid {
    grid-template-columns: 1fr;
  }
}

.dashboard-shell {
  --dash-bg: #f8fafc;
  --dash-surface: #ffffff;
  --dash-surface-soft: #f1f5f9;
  --dash-ink: #1e293b;
  --dash-muted: #64748b;
  --dash-faint: #94a3b8;
  --dash-blue: #1e40af;
  --dash-blue-soft: #dbeafe;
  --dash-cyan: #0284c7;
  --dash-cyan-soft: #e0f2fe;
  --dash-amber: #d97706;
  --dash-amber-soft: #fef3c7;
  --dash-green: #059669;
  --dash-green-soft: #d1fae5;
  --dash-red: #dc2626;
  --dash-red-soft: #fee2e2;
  --dash-border: #dbeafe;
  --dash-border-strong: #bfdbfe;
  --dash-shadow: 0 1px 3px rgba(30, 58, 138, 7%), 0 1px 2px rgba(30, 58, 138, 5%);
  --dash-shadow-strong: 0 10px 30px rgba(30, 58, 138, 11%), 0 2px 8px rgba(30, 64, 175, 7%);
  --home-accent: var(--dash-blue);
  --home-accent-soft: var(--dash-blue-soft);
  --home-ink: var(--dash-ink);
  --home-muted: var(--dash-muted);

  gap: 14px;
  max-width: min(1680px, 100%);
  background: var(--dash-bg);
  color: var(--dash-ink);
  font-family: "Fira Sans", "PingFang SC", "Hiragino Sans GB", "Microsoft YaHei", system-ui, sans-serif;
}

.dashboard-shell :where(button) {
  font: inherit;
  touch-action: manipulation;
}

.dashboard-shell :where(button, .arco-btn) {
  min-height: 36px;
}

.dashboard-shell :where(button:focus-visible, .arco-btn:focus-visible) {
  outline: 2px solid var(--dash-blue);
  outline-offset: 2px;
}

.dashboard-shell .home-top {
  position: sticky;
  top: 0;
  z-index: 2;
  padding: var(--moox-space-2) 0 2px;
  background: linear-gradient(180deg, var(--dash-bg) 75%, rgba(248, 250, 252, 0));
}

.dashboard-shell .home-badge {
  border: 1px solid var(--dash-border);
  background: var(--dash-surface);
  color: var(--dash-blue);
  font-family: $mono;
  letter-spacing: 0;
}

.dashboard-shell .home-top-title,
.dashboard-shell .home-clock-sub {
  color: var(--dash-muted);
}

.dashboard-shell .home-clock {
  color: var(--dash-ink);
}

.dashboard-shell .onboard {
  border-color: var(--dash-border);
  border-radius: 8px;
  background: linear-gradient(135deg, rgba(219, 234, 254, 70%), rgba(255, 255, 255, 92%)), var(--dash-surface);
  box-shadow: var(--dash-shadow);
}

.dash-command {
  position: relative;
  isolation: isolate;
  display: grid;
  grid-template-columns: minmax(0, 1fr) 220px;
  gap: var(--moox-space-4);
  align-items: stretch;
  min-height: 132px;
  padding: 18px;
  overflow: visible;
  border: 1px solid rgba(147, 197, 253, 65%);
  border-radius: 8px;
  background: linear-gradient(
    135deg,
    rgba(239, 246, 255, 98%) 0%,
    rgba(219, 234, 254, 90%) 42%,
    rgba(224, 242, 254, 86%) 72%,
    rgba(191, 219, 254, 74%) 100%
  );
  box-shadow: var(--dash-shadow-strong);
}

.dash-command::before {
  content: "";
  position: absolute;
  inset: 0;
  z-index: -1;
  border-radius: inherit;
  background:
    url("data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' width='520' height='176' viewBox='0 0 520 176'%3E%3Cg id='volume-bars' fill='%230f172a' fill-opacity='.08'%3E%3Crect x='300' y='148' width='8' height='10' rx='2'/%3E%3Crect x='312' y='145' width='8' height='13' rx='2'/%3E%3Crect x='324' y='150' width='8' height='8' rx='2'/%3E%3Crect x='336' y='142' width='8' height='16' rx='2'/%3E%3Crect x='348' y='136' width='8' height='22' rx='2'/%3E%3Crect x='360' y='144' width='8' height='14' rx='2'/%3E%3Crect x='372' y='130' width='8' height='28' rx='2'/%3E%3Crect x='384' y='126' width='8' height='32' rx='2'/%3E%3Crect x='396' y='118' width='8' height='40' rx='2'/%3E%3Crect x='408' y='122' width='8' height='36' rx='2'/%3E%3Crect x='420' y='110' width='8' height='48' rx='2'/%3E%3Crect x='432' y='116' width='8' height='42' rx='2'/%3E%3C/g%3E%3Cg id='candlestick-45-layer' stroke='%230f172a' stroke-linecap='round' stroke-linejoin='round'%3E%3Cpath id='angle-45-guide' d='M304 150 L434 20' fill='none' stroke='%230f172a' stroke-width='1' stroke-opacity='.16' stroke-dasharray='4 8'/%3E%3Cpath d='M304 154V116 M316 144V104 M328 148V96 M340 134V84 M352 120V72 M364 126V62 M376 108V52 M388 100V44 M400 92V36 M412 82V28 M424 72V20 M436 64V14' stroke-width='1' stroke-opacity='.25'/%3E%3Crect class='candle-body' x='300' y='128' width='8' height='18' rx='2' fill='%23059669' fill-opacity='.14' stroke='%230f172a' stroke-opacity='.25'/%3E%3Crect class='candle-body' x='312' y='112' width='8' height='24' rx='2' fill='%230f172a' fill-opacity='.13' stroke='%230f172a' stroke-opacity='.23'/%3E%3Crect class='candle-body' x='324' y='104' width='8' height='34' rx='2' fill='%23059669' fill-opacity='.12' stroke='%230f172a' stroke-opacity='.24'/%3E%3Crect class='candle-body' x='336' y='92' width='8' height='30' rx='2' fill='%23059669' fill-opacity='.14' stroke='%230f172a' stroke-opacity='.25'/%3E%3Crect class='candle-body' x='348' y='78' width='8' height='34' rx='2' fill='%23059669' fill-opacity='.15' stroke='%230f172a' stroke-opacity='.26'/%3E%3Crect class='candle-body' x='360' y='76' width='8' height='40' rx='2' fill='%23d97706' fill-opacity='.10' stroke='%230f172a' stroke-opacity='.22'/%3E%3Crect class='candle-body' x='372' y='62' width='8' height='34' rx='2' fill='%23059669' fill-opacity='.15' stroke='%230f172a' stroke-opacity='.27'/%3E%3Crect class='candle-body' x='384' y='54' width='8' height='34' rx='2' fill='%23059669' fill-opacity='.14' stroke='%230f172a' stroke-opacity='.25'/%3E%3Crect class='candle-body' x='396' y='46' width='8' height='30' rx='2' fill='%23059669' fill-opacity='.15' stroke='%230f172a' stroke-opacity='.26'/%3E%3Crect class='candle-body' x='408' y='40' width='8' height='32' rx='2' fill='%23d97706' fill-opacity='.10' stroke='%230f172a' stroke-opacity='.22'/%3E%3Crect class='candle-body' x='420' y='30' width='8' height='28' rx='2' fill='%23059669' fill-opacity='.16' stroke='%230f172a' stroke-opacity='.28'/%3E%3Crect class='candle-body' x='432' y='22' width='8' height='28' rx='2' fill='%23059669' fill-opacity='.15' stroke='%230f172a' stroke-opacity='.27'/%3E%3C/g%3E%3C/svg%3E"),
    repeating-linear-gradient(118deg, transparent 0 11px, rgba(15, 23, 42, 7%) 12px 13px, transparent 14px 27px),
    linear-gradient(90deg, rgba(30, 64, 175, 8%) 1px, transparent 1px),
    linear-gradient(180deg, rgba(30, 64, 175, 7%) 1px, transparent 1px);
  background-position:
    right clamp(128px, 12vw, 180px) center,
    0 0,
    0 0,
    0 0;
  background-repeat: no-repeat, repeat, repeat, repeat;
  background-size:
    min(58vw, 620px) auto,
    36px 36px,
    34px 34px,
    34px 34px;
  clip-path: inset(0 round 8px);
  mix-blend-mode: multiply;
  opacity: 0.72;
  mask-image: linear-gradient(90deg, rgba(0, 0, 0, 52%), rgba(0, 0, 0, 34%) 58%, rgba(0, 0, 0, 12%) 88%, transparent);
}

.dash-command::after {
  content: "";
  position: absolute;
  inset: 0 auto 0 0;
  width: 4px;
  border-radius: 8px 0 0 8px;
  background: linear-gradient(180deg, var(--dash-blue), var(--dash-cyan), var(--dash-green));
}

.dash-command-main {
  position: relative;
  z-index: 1;
  display: grid;
  align-content: center;
  min-width: 0;
}

.dash-kicker {
  display: flex;
  flex-wrap: wrap;
  gap: var(--moox-space-2);
  align-items: center;
  margin-bottom: var(--moox-space-2);
  color: var(--dash-muted);
  font-size: 13px;
}

.dash-kicker b {
  padding: 3px 7px;
  border: 1px solid rgba(2, 132, 199, 24%);
  border-radius: 999px;
  background: rgba(224, 242, 254, 72%);
  color: var(--dash-cyan);
  font-family: $mono;
  font-size: 11px;
  font-weight: 700;
  letter-spacing: 0;
}

.banner-slogan-enter-active,
.banner-slogan-leave-active {
  transition:
    opacity 0.24s ease,
    transform 0.24s ease;
}

.banner-slogan-enter-from {
  opacity: 0;
  transform: translateY(6px);
}

.banner-slogan-leave-to {
  opacity: 0;
  transform: translateY(-6px);
}

.dash-command h1 {
  margin: 0;
  color: var(--dash-ink);
  font-size: 30px;
  font-weight: 750;
  line-height: 1.18;
}

.dash-command p {
  max-width: 760px;
  margin: 10px 0 0;
  color: var(--dash-muted);
  font-size: 14px;
  line-height: 1.65;
}

.health-score-card {
  position: relative;
  z-index: 1;
  display: grid;
  grid-template-columns: minmax(0, 1fr) auto;
  align-items: end;
  gap: var(--moox-space-2) var(--moox-space-3);
  min-width: 0;
  padding: 14px;
  border: 1px solid rgba(30, 64, 175, 18%);
  border-radius: 8px;
  background: linear-gradient(180deg, rgba(255, 255, 255, 88%), rgba(248, 250, 252, 76%));
  box-shadow: inset 0 1px 0 rgba(255, 255, 255, 82%);
}

.score-label {
  grid-column: 1 / -1;
  color: var(--dash-muted);
  font-size: 12px;
  font-weight: 700;
}

.health-score-card strong {
  color: var(--dash-blue);
  font-family: $mono;
  font-size: 38px;
  font-weight: 700;
  font-variant-numeric: tabular-nums;
  line-height: 0.92;
}

.health-score-card small {
  align-self: end;
  overflow: hidden;
  padding-bottom: 3px;
  color: var(--dash-muted);
  font-size: 12px;
  font-weight: 600;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.score-bar {
  grid-column: 1 / -1;
  height: 8px;
  overflow: hidden;
  border-radius: 999px;
  background: rgba(191, 219, 254, 68%);
}

.score-bar span {
  display: block;
  height: 100%;
  border-radius: inherit;
  background: linear-gradient(90deg, var(--dash-blue), var(--dash-cyan), var(--dash-green));
  box-shadow: 0 0 12px rgba(2, 132, 199, 24%);
}

.kpi-grid {
  display: grid;
  grid-template-columns: repeat(6, minmax(0, 1fr));
  gap: 10px;
}

.kpi-card {
  position: relative;
  isolation: isolate;
  display: grid;
  min-width: 0;
  min-height: 116px;
  padding: 13px 14px;
  overflow: hidden;
  border: 1px solid rgba(191, 219, 254, 86%);
  border-radius: 8px;
  background: linear-gradient(180deg, rgba(255, 255, 255, 100%), rgba(248, 250, 252, 92%));
  box-shadow: var(--dash-shadow);
  color: inherit;
  cursor: pointer;
  text-align: left;
  transition:
    border-color 0.18s ease,
    box-shadow 0.18s ease,
    transform 0.18s ease;
}

.kpi-card::before {
  content: "";
  position: absolute;
  inset: 0 0 auto;
  height: 3px;
  background: var(--tone, var(--dash-blue));
}

.kpi-card::after {
  content: "";
  position: absolute;
  right: -26px;
  bottom: -38px;
  z-index: -1;
  width: 96px;
  height: 96px;
  border: 1px solid var(--tone, var(--dash-blue));
  border-radius: 50%;
  opacity: 0.08;
}

.kpi-card > * {
  position: relative;
  z-index: 1;
}

.kpi-card:hover {
  border-color: var(--tone, var(--dash-blue));
  box-shadow: var(--dash-shadow-strong);
  transform: translateY(-1px);
}

.kpi-label,
.kpi-note,
.kpi-delta {
  display: block;
  min-width: 0;
}

.kpi-label {
  color: var(--dash-muted);
  font-size: 12px;
  font-weight: 600;
}

.kpi-card strong {
  align-self: center;
  color: var(--dash-ink);
  font-family: $mono;
  font-size: 32px;
  font-weight: 700;
  font-variant-numeric: tabular-nums;
  line-height: 1;
}

.kpi-card strong small {
  color: var(--dash-muted);
  font-size: 14px;
  font-weight: 600;
}

.kpi-note {
  color: var(--dash-muted);
  font-size: 12px;
  line-height: 1.35;
}

.kpi-delta {
  justify-self: start;
  padding: 3px 7px;
  border-radius: 999px;
  background: rgba(30, 64, 175, 7%);
  color: var(--tone, var(--dash-blue));
  font-size: 11px;
  font-weight: 700;
}

.tone-ok {
  --tone: var(--dash-green);
  --tone-soft: var(--dash-green-soft);
}

.tone-warn {
  --tone: var(--dash-amber);
  --tone-soft: var(--dash-amber-soft);
}

.tone-danger {
  --tone: var(--dash-red);
  --tone-soft: var(--dash-red-soft);
}

.tone-neutral {
  --tone: var(--dash-blue);
  --tone-soft: var(--dash-blue-soft);
}

.dashboard-grid {
  display: grid;
  grid-template-columns: repeat(4, minmax(0, 1fr));
  gap: var(--moox-space-3);
  align-items: start;
}

.dash-card {
  position: relative;
  min-width: 0;
  padding: 15px;
  border: 1px solid var(--dash-border);
  border-radius: 8px;
  background: linear-gradient(180deg, rgba(255, 255, 255, 100%), rgba(248, 250, 252, 70%));
  box-shadow: var(--dash-shadow);
  transition:
    border-color 0.18s ease,
    box-shadow 0.18s ease;
}

.dash-card:hover {
  border-color: rgba(147, 197, 253, 88%);
  box-shadow: var(--dash-shadow-strong);
}

.dash-card.span-2 {
  grid-column: span 2;
}

.dash-card-head {
  display: flex;
  gap: var(--moox-space-3);
  align-items: flex-start;
  justify-content: space-between;
  margin-bottom: var(--moox-space-3);
}

.dash-card-head.compact {
  margin-bottom: 10px;
}

.dash-card-head h2 {
  display: flex;
  gap: var(--moox-space-2);
  align-items: center;
  margin: 0;
  color: var(--dash-ink);
  font-size: 15px;
  font-weight: 750;
  line-height: 1.35;
}

.dash-card-head h2::before {
  content: "";
  width: 4px;
  height: 14px;
  border-radius: 999px;
  background: linear-gradient(180deg, var(--dash-blue), var(--dash-cyan));
}

.dash-card-head p {
  margin: var(--moox-space-1) 0 0;
  color: var(--dash-muted);
  font-size: 12px;
  line-height: 1.45;
}

.dash-chip {
  flex: none;
  padding: var(--moox-space-1) 7px;
  border: 1px solid var(--dash-border-strong);
  border-radius: 999px;
  background: var(--dash-blue-soft);
  color: var(--dash-blue);
  font-size: 11px;
  font-weight: 700;
  white-space: nowrap;
}

.dash-chip.ok {
  border-color: rgba(5, 150, 105, 28%);
  background: var(--dash-green-soft);
  color: var(--dash-green);
}

.dash-chip.warn {
  border-color: rgba(217, 119, 6, 28%);
  background: var(--dash-amber-soft);
  color: var(--dash-amber);
}

.dash-chip.danger {
  border-color: rgba(220, 38, 38, 24%);
  background: var(--dash-red-soft);
  color: var(--dash-red);
}

.score-breakdown,
.freshness-list,
.incident-table,
.account-lines,
.service-lines,
.resource-lines {
  display: grid;
  gap: var(--moox-space-2);
}

.score-line {
  display: grid;
  grid-template-columns: minmax(120px, 1fr) minmax(86px, 0.8fr) auto;
  gap: 10px;
  align-items: center;
  min-height: 42px;
  padding: var(--moox-space-2) 0;
  border-bottom: 1px solid rgba(219, 234, 254, 65%);
}

.score-line:last-child {
  border-bottom: 0;
}

.score-line-meta {
  min-width: 0;
}

.score-line-meta strong,
.freshness-row strong,
.incident-row strong {
  display: block;
  overflow: hidden;
  color: var(--dash-ink);
  font-size: 13px;
  font-weight: 700;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.score-line-meta span,
.freshness-row small {
  display: block;
  overflow: hidden;
  margin-top: 3px;
  color: var(--dash-muted);
  font-size: 11px;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.score-line-meter {
  height: 7px;
  overflow: hidden;
  border-radius: 999px;
  background: var(--dash-surface-soft);
}

.score-line-meter span {
  display: block;
  height: 100%;
  border-radius: inherit;
  background: var(--tone, var(--dash-blue));
}

.score-line b {
  color: var(--tone, var(--dash-blue));
  font-family: $mono;
  font-size: 12px;
  font-variant-numeric: tabular-nums;
}

.freshness-row,
.incident-row,
.account-line,
.service-line,
.resource-line {
  display: grid;
  gap: var(--moox-space-2);
  align-items: center;
  min-width: 0;
  min-height: 44px;
  padding: 9px 10px;
  border: 1px solid rgba(219, 234, 254, 70%);
  border-radius: 7px;
  background: linear-gradient(180deg, rgba(248, 250, 252, 70%), rgba(255, 255, 255, 100%));
  color: inherit;
  cursor: pointer;
  text-align: left;
  transition:
    border-color 0.18s ease,
    background 0.18s ease;
}

.freshness-row {
  grid-template-columns: minmax(0, 1fr) 46px 54px;
}

.freshness-row:hover,
.incident-row:hover,
.account-line:hover,
.service-line:hover,
.resource-line:hover,
.action-tile:hover,
.pipeline-step:hover {
  border-color: var(--tone, var(--dash-blue));
  background: var(--tone-soft, var(--dash-blue-soft));
}

.freshness-row b,
.incident-row b,
.account-line b,
.service-line b,
.resource-line b {
  color: var(--tone, var(--dash-blue));
  font-family: $mono;
  font-size: 12px;
  font-weight: 700;
  font-variant-numeric: tabular-nums;
}

.freshness-row em,
.account-line em,
.service-line em,
.resource-line em {
  overflow: hidden;
  color: var(--dash-muted);
  font-size: 11px;
  font-style: normal;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.incident-row {
  grid-template-columns: 44px minmax(160px, 1fr) minmax(140px, 0.8fr) 74px;
}

.incident-level {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 32px;
  min-height: 24px;
  border-radius: 6px;
  background: var(--tone-soft, var(--dash-blue-soft));
  color: var(--tone, var(--dash-blue));
  font-family: $mono;
  font-size: 11px;
  font-weight: 700;
}

.incident-row > span:not(.incident-level) {
  overflow: hidden;
  color: var(--dash-muted);
  font-size: 12px;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.pipeline-compact {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: var(--moox-space-2);
}

.pipeline-step,
.action-tile {
  min-width: 0;
  border: 1px solid rgba(219, 234, 254, 75%);
  border-radius: 7px;
  background: var(--dash-surface);
  color: inherit;
  cursor: pointer;
  text-align: left;
  transition:
    border-color 0.18s ease,
    background 0.18s ease;
}

.pipeline-step {
  display: grid;
  gap: 5px;
  min-height: 68px;
  padding: 10px;
}

.pipeline-step span {
  color: var(--dash-muted);
  font-size: 12px;
  font-weight: 600;
}

.pipeline-step strong {
  color: var(--dash-ink);
  font-family: $mono;
  font-size: 24px;
  font-variant-numeric: tabular-nums;
}

.trade-metrics {
  display: grid;
  grid-template-columns: repeat(4, minmax(0, 1fr));
  gap: 6px;
  margin-bottom: 10px;
}

.trade-metrics div {
  display: grid;
  gap: var(--moox-space-1);
  min-width: 0;
  padding: var(--moox-space-2);
  border: 1px solid rgba(219, 234, 254, 72%);
  border-radius: 7px;
  background: var(--dash-surface-soft);
}

.trade-metrics span {
  overflow: hidden;
  color: var(--dash-muted);
  font-size: 11px;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.trade-metrics strong {
  color: var(--dash-ink);
  font-family: $mono;
  font-size: 17px;
  font-variant-numeric: tabular-nums;
}

.account-line,
.service-line,
.resource-line {
  grid-template-columns: minmax(0, 1fr) 58px minmax(0, 1fr);
}

.account-line span,
.service-line span,
.resource-line span {
  overflow: hidden;
  color: var(--dash-ink);
  font-size: 12px;
  font-weight: 700;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.pulse-bars {
  display: grid;
  grid-template-columns: repeat(12, minmax(0, 1fr));
  gap: var(--moox-space-2);
  align-items: end;
  min-height: 176px;
  padding: var(--moox-space-2) 2px 0;
}

.pulse-bar {
  display: grid;
  gap: 6px;
  align-items: end;
  justify-items: center;
  min-width: 0;
}

.pulse-bar span,
.pulse-bar b {
  color: var(--dash-muted);
  font-family: $mono;
  font-size: 10px;
  font-variant-numeric: tabular-nums;
}

.pulse-bar div {
  position: relative;
  width: 100%;
  max-width: 28px;
  height: 112px;
  overflow: hidden;
  border-radius: 6px 6px 3px 3px;
  background: linear-gradient(180deg, rgba(219, 234, 254, 42%) 0 1px, transparent 1px 25%), var(--dash-surface-soft);
}

.pulse-bar i {
  position: absolute;
  right: 0;
  bottom: 0;
  left: 0;
  border-radius: inherit;
  background: linear-gradient(180deg, var(--dash-blue), var(--dash-green));
}

.resource-lines {
  margin-top: var(--moox-space-3);
  padding-top: var(--moox-space-3);
  border-top: 1px solid rgba(219, 234, 254, 72%);
}

.resource-caption {
  color: var(--dash-muted);
  font-size: 12px;
  font-weight: 700;
}

.action-grid {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: var(--moox-space-2);
}

.action-tile {
  display: grid;
  grid-template-columns: 28px minmax(0, 1fr);
  gap: 2px var(--moox-space-2);
  min-height: 64px;
  padding: 10px;
}

.action-tile b {
  grid-row: span 2;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 28px;
  height: 28px;
  border-radius: 7px;
  background: var(--dash-blue-soft);
  color: var(--dash-blue);
  font-family: $mono;
  font-size: 12px;
}

.action-tile span,
.action-tile small {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.action-tile span {
  color: var(--dash-ink);
  font-size: 12px;
  font-weight: 700;
}

.action-tile small {
  color: var(--dash-muted);
  font-size: 11px;
}

@media (prefers-reduced-motion: reduce) {
  .kpi-card,
  .freshness-row,
  .incident-row,
  .account-line,
  .service-line,
  .resource-line,
  .pipeline-step,
  .action-tile {
    transition: none;
  }

  .banner-slogan-enter-active,
  .banner-slogan-leave-active {
    transition: none;
  }
}

@media (max-width: 1280px) {
  .dashboard-grid {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }

  .kpi-grid {
    grid-template-columns: repeat(3, minmax(0, 1fr));
  }
}

@media (max-width: 900px) {
  .dash-command {
    grid-template-columns: 1fr;
  }

  .health-score-card {
    grid-template-columns: minmax(0, 1fr) auto;
  }

  .health-score-card strong {
    grid-column: auto;
  }

  .score-bar {
    grid-column: 1 / -1;
  }

  .dashboard-grid,
  .kpi-grid {
    grid-template-columns: 1fr;
  }

  .dash-card.span-2 {
    grid-column: auto;
  }

  .incident-row {
    grid-template-columns: 44px minmax(0, 1fr);
  }

  .incident-row > span:not(.incident-level),
  .incident-row b {
    grid-column: 2;
  }

  .pulse-bars {
    grid-template-columns: repeat(6, minmax(0, 1fr));
  }
}

@media (max-width: 640px) {
  .dashboard-shell {
    gap: var(--moox-space-3);
    padding: 10px 10px var(--moox-space-7);
  }

  .dashboard-shell .home-top {
    gap: var(--moox-space-2);
    padding-top: var(--moox-space-1);
  }

  .dashboard-shell .home-top-right {
    flex-wrap: wrap;
    gap: var(--moox-space-2);
    width: 100%;
  }

  .dash-command,
  .dash-card {
    padding: var(--moox-space-3);
  }

  .dash-command h1 {
    font-size: 24px;
  }

  .health-score-card {
    grid-template-columns: minmax(0, 1fr) auto;
  }

  .health-score-card strong {
    grid-column: auto;
    font-size: 36px;
  }

  .freshness-row,
  .account-line,
  .service-line,
  .resource-line {
    grid-template-columns: minmax(0, 1fr) auto;
  }

  .freshness-row em,
  .account-line em,
  .service-line em,
  .resource-line em {
    grid-column: 1 / -1;
  }

  .score-line {
    grid-template-columns: 1fr auto;
  }

  .score-line-meter {
    grid-column: 1 / -1;
  }

  .trade-metrics,
  .pipeline-compact,
  .action-grid {
    grid-template-columns: 1fr;
  }
}
</style>
