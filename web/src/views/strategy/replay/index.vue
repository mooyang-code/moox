<template>
  <div class="moox-page">
    <div class="moox-inner">
      <div class="result-toolbar">
        <span class="result-count">
          共 {{ replayTotal }} 个回放<span v-if="readAt">；状态读取：{{ readAt }}</span>
          <InfoTip text="基于 View 的研究回放：现货理论账本、按当期收盘价成交，用于比较策略版本，不承诺复现实时结果。" />
        </span>
        <a-space wrap
          ><a-button :loading="listLoading" aria-label="刷新回放" @click="loadReplays"
            ><template #icon><icon-refresh /></template>刷新</a-button
          ><a-button type="primary" @click="openCreate"
            ><template #icon><icon-plus /></template>发起回放</a-button
          ></a-space
        >
      </div>
      <a-alert v-if="pollFailures >= 3" type="warning" show-icon class="top-alert"
        >后台暂时无法访问，页面上的回放状态可能不是最新；恢复后会自动刷新。</a-alert
      >
      <a-grid :cols="{ xs: 1, md: 3 }" :col-gap="20" :row-gap="16">
        <a-grid-item>
          <a-alert v-if="errors.list" type="error" show-icon class="top-alert"
            >{{ errors.list }}<template #action><a-button size="small" @click="loadReplays">重试</a-button></template></a-alert
          >
          <ReplayRecordList
            :replays="replays"
            :strategies="strategyStore.strategyCatalog"
            :selected-id="selectedId"
            :loading="listLoading"
            :page="replayPage"
            :page-size="replayPageSize"
            :total="replayTotal"
            @select="pick"
            @page-change="changeReplayPage"
            @create="openCreate"
          />
        </a-grid-item>
        <a-grid-item :span="{ xs: 1, md: 2 }">
          <a-alert v-if="errors.detail" type="error" show-icon class="top-alert"
            >{{ errors.detail
            }}<template #action
              ><a-button size="small" :disabled="!selectedId" @click="retryDetail">重试</a-button></template
            ></a-alert
          >
          <div v-if="detailLoading" class="detail-loading"><a-spin tip="正在加载回放详情" /></div>
          <a-empty v-else-if="!selected" description="选择或发起一个回放" />
          <template v-else>
            <div class="detail-head">
              <div>
                <a-tag :color="replayStatusColor(selected.status)">{{ replayStatusLabel(selected.status) }}</a-tag>
                <span class="mono">{{ selected.replay_id }}</span>
                <span class="muted" :title="selected.dsl_hash"> · DSL {{ shortHash(selected.dsl_hash) }}</span>
              </div>
              <a-space>
                <a-popconfirm
                  v-if="(selected.status === 'pending' || selected.status === 'running') && selected.replay_id === selectedId"
                  content="回放不支持续跑，取消后只保留已写入的周期记录。确定取消？"
                  ok-text="取消回放"
                  cancel-text="继续运行"
                  @ok="cancel"
                >
                  <a-button status="warning" :loading="cancelling">取消回放</a-button>
                </a-popconfirm>
              </a-space>
            </div>
            <div class="muted detail-info">
              {{ sourceLabel(selected) }}
              <template v-if="selected.instance_id">
                · 实例 <span class="mono">{{ selected.instance_id }}</span
                >，会话 <span class="mono">{{ selected.session_id }}</span></template
              >
              · View {{ selected.view_id }} · 区间 {{ replayRangeLabel(selected) }} · 手续费 {{ selected.fee_bps }} bps
              <template v-if="metrics?.first_bar_end">
                · 实际首根 {{ formatUtcTime(metrics.first_bar_end) }}，末根 {{ formatUtcTime(metrics.last_bar_end) }}</template
              >
            </div>
            <a-alert v-if="selected.status === 'failed'" type="error" show-icon class="top-alert"
              >回放失败：{{
                selected.error === "interrupted" ? "进程重启中断，已写入的周期保留，可重新发起" : selected.error
              }}</a-alert
            >
            <a-alert v-if="metrics && partialMetrics" type="warning" class="top-alert"
              >回放{{ selected.status === "cancelled" ? "已取消" : "失败" }}，以下是截至中断时的部分指标（共
              {{ metrics.bars }} 根）。</a-alert
            >
            <a-alert v-else-if="awaitingShown" type="info" class="top-alert">已取消，正在等待写入截至取消时的部分指标……</a-alert>
            <div v-if="metrics" class="metrics">
              <div class="metric">
                <span>累计收益</span
                ><strong :class="metrics.total_return >= 0 ? 'up' : 'down'">{{ pct(metrics.total_return) }}</strong>
              </div>
              <div class="metric">
                <span>年化</span
                ><strong :title="metrics.annualized_return === null ? '区间不足一周或无法年化，见局限性' : undefined">{{
                  pct(metrics.annualized_return)
                }}</strong>
              </div>
              <div class="metric">
                <span>最大回撤</span><strong class="down">{{ pct(metrics.max_drawdown) }}</strong>
              </div>
              <div class="metric">
                <span>平均单边换手</span><strong>{{ pct(metrics.average_turnover) }}</strong>
              </div>
              <div class="metric">
                <span>平均持仓数</span><strong>{{ metrics.average_holdings.toFixed(2) }}</strong>
              </div>
              <div class="metric">
                <span>周期（ok / 跳过）</span><strong>{{ metrics.ok_bars }} / {{ metrics.skipped_bars }}</strong>
              </div>
              <div class="metric">
                <span>总手续费</span><strong>{{ pct(metrics.total_fee / (metrics.initial_equity || 1)) }}</strong>
              </div>
              <div class="metric">
                <span>目标未达成期数 / 清算次数</span><strong>{{ metrics.unfilled_bars }} / {{ metrics.liquidations }}</strong>
              </div>
            </div>
            <div class="chart-wrap">
              <div ref="chartContainer" class="chart" role="img" :aria-label="chartLabel" />
              <a-empty v-if="!series.length" description="暂无周期记录" />
            </div>
            <a-collapse v-if="metrics" :default-active-key="['limits']" class="limits">
              <a-collapse-item key="limits" header="局限性">
                <ul>
                  <li v-for="note in metrics.limitations" :key="note">{{ note }}</li>
                </ul>
                <div v-if="Object.keys(metrics.skip_reasons || {}).length" class="muted">
                  跳过原因：<span v-for="(count, reason) in metrics.skip_reasons" :key="reason"
                    >{{ skipReasonLabel(String(reason)) }} × {{ count }}；</span
                  >
                </div>
                <div v-if="Object.keys(metrics.factors || {}).length" class="muted">
                  因子定义指纹：<span v-for="(hash, factor) in metrics.factors" :key="factor" :title="hash"
                    >{{ factor }} {{ shortHash(hash) }}；</span
                  >
                </div>
              </a-collapse-item>
            </a-collapse>
            <a-collapse v-if="selected.dsl_yaml" class="limits">
              <a-collapse-item key="dsl" :header="`被回放的 DSL（${shortHash(selected.dsl_hash)}）`">
                <pre class="dsl">{{ selected.dsl_yaml }}</pre>
              </a-collapse-item>
            </a-collapse>
            <a-alert v-if="errors.table" type="error" show-icon class="top-alert"
              >{{ errors.table }}<template #action><a-button size="small" @click="retryTable">重试</a-button></template></a-alert
            >
            <a-table
              row-key="bar_end_time"
              size="small"
              :data="tableRows"
              :loading="tableLoading"
              :pagination="tablePagination"
              :scroll="{ x: 960 }"
              :expandable="{ title: '明细', width: 60 }"
              @page-change="changeTablePage"
            >
              <template #expand-row="{ record }">
                <div class="bar-detail">
                  <div>
                    现金 {{ pct(barDetail(record).cash) }}（占期末权益）· 成交额 {{ barDetail(record).traded.toFixed(4) }} ·
                    手续费 {{ barDetail(record).fee.toFixed(6) }}
                  </div>
                  <div v-if="barDetail(record).buyScale !== null">
                    现金不足，买入按目标的 {{ pct(barDetail(record).buyScale) }} 成交
                  </div>
                  <div v-if="barDetail(record).unfilled.length">未达成目标：{{ barDetail(record).unfilled.join("、") }}</div>
                  <div v-if="barDetail(record).liquidated.length">缺价清算：{{ barDetail(record).liquidated.join("、") }}</div>
                  <div v-for="note in barDetail(record).notes" :key="note" class="muted">{{ note }}</div>
                  <a-table
                    v-if="barDetail(record).positions.length"
                    size="mini"
                    :data="barDetail(record).positions"
                    :pagination="false"
                    row-key="id"
                  >
                    <template #columns>
                      <a-table-column title="标的" data-index="id" />
                      <a-table-column title="数量"
                        ><template #cell="{ record: position }">{{ position.quantity.toPrecision(6) }}</template></a-table-column
                      >
                      <a-table-column title="估值价"
                        ><template #cell="{ record: position }">{{ position.last_price }}</template></a-table-column
                      >
                      <a-table-column title="市值"
                        ><template #cell="{ record: position }">{{ position.value.toFixed(4) }}</template></a-table-column
                      >
                      <a-table-column title="状态"
                        ><template #cell="{ record: position }">{{
                          position.frozen ? `冻结（连续缺价 ${position.missing_bars} 期）` : "可成交"
                        }}</template></a-table-column
                      >
                    </template>
                  </a-table>
                  <div v-else class="muted">本期没有持仓</div>
                </div>
              </template>
              <template #columns>
                <a-table-column title="K 线结束（UTC）" :width="170"
                  ><template #cell="{ record }">{{ formatUtcTime(record.bar_end_time) }}</template></a-table-column
                >
                <a-table-column title="状态" :width="150"
                  ><template #cell="{ record }"
                    ><a-tag size="small" :color="record.status === 'ok' ? 'green' : 'orange'">{{
                      record.status === "ok" ? "ok" : skipReasonLabel(record.skip_reason)
                    }}</a-tag></template
                  ></a-table-column
                >
                <a-table-column title="权益" :width="110"
                  ><template #cell="{ record }">{{ record.equity.toFixed(4) }}</template></a-table-column
                >
                <a-table-column title="收益" :width="90"
                  ><template #cell="{ record }"
                    ><span :class="record.bar_return >= 0 ? 'up' : 'down'">{{ pct(record.bar_return) }}</span></template
                  ></a-table-column
                >
                <a-table-column title="单边换手" :width="90"
                  ><template #cell="{ record }">{{ pct(record.turnover) }}</template></a-table-column
                >
                <a-table-column title="持仓（冻结）" :width="110"
                  ><template #cell="{ record }">{{ record.holdings }}（{{ record.frozen }}）</template></a-table-column
                >
                <a-table-column title="未达成 / 清算" :width="110"
                  ><template #cell="{ record }">{{ record.unfilled }} / {{ record.liquidated }}</template></a-table-column
                >
                <a-table-column title="目标"
                  ><template #cell="{ record }"
                    ><span class="targets">{{
                      record.targets
                        .map((target: InstrumentTarget) => `${target.instrument_id} ${percent(target.target_weight)}`)
                        .join("、") || "-"
                    }}</span></template
                  ></a-table-column
                >
              </template>
            </a-table>
          </template>
        </a-grid-item>
      </a-grid>
      <!-- form 是页面持有的 reactive 草稿：只单向传入，子组件就地修改字段，不整体替换（const 不能被 v-model 赋值）。 -->
      <ReplayCreateDrawer
        :model-value="form"
        :visible="parsed.create"
        :strategies="strategyStore.strategyCatalog"
        :views="views"
        :views-loading="viewsLoading"
        :starting="starting"
        :errors="{ strategies: errors.strategies, views: errors.views, start: errors.start }"
        @close="closeCreate"
        @start="start"
        @retry-strategies="loadStrategies"
        @retry-views="loadViews"
      />
    </div>
  </div>
</template>

<script setup lang="ts">
import { StaleSpaceRequestError } from "@/api/admin/stale-space";
import { computed, nextTick, onBeforeUnmount, onMounted, reactive, ref, watch } from "vue";
import { Message } from "@arco-design/web-vue";
import type VChart from "@visactor/vchart";
import { useRoute, useRouter } from "vue-router";
import { cancelReplay, getReplay, listReplayBars, listReplays, startReplay } from "@/api/strategy";
import type { PageResult } from "@/api/strategy";
import type { InstrumentTarget, Replay, ReplayBar } from "@/api/strategy-types";
import { reportControlError, serverNow } from "@/api/admin/http";
import { listViews } from "@/api/storage/metadata";
import type { View } from "@/api/storage/types";
import { useSpaceStore } from "@/store/modules/space";
import { useStrategyStore } from "@/store/modules/strategy";
import { percent, shortHash, skipReasonLabel } from "@/views/strategy/model";
import { sameQuery } from "@/views/strategy/query";
import InfoTip from "@/views/factor/shared/info-tip.vue";
import { STRATEGY_TASKS_ROUTE, normalizeTaskTab } from "@/views/strategy/links";
import ReplayCreateDrawer from "@/views/strategy/replay/replay-create-drawer.vue";
import { type ReplayFormState, replaySourceOf, validateReplayForm } from "@/views/strategy/replay/replay-form-model";
import { buildReplayQuery, hasPrefill, parseReplayQuery, type ReplayQueryState } from "@/views/strategy/replay/replay-query";
import ReplayRecordList from "@/views/strategy/replay/replay-record-list.vue";
import {
  equitySeries,
  equitySummary,
  formatUtcTime,
  mergeBars,
  parseBarDetail,
  parseMetrics,
  parseUtcInput,
  recentUtcRange,
  replayRangeLabel,
  replaySourceLabel,
  replayStatusColor,
  replayStatusLabel
} from "@/views/strategy/replay/model";

defineOptions({ name: "StrategyReplay" });
const ROUTE_NAME = STRATEGY_TASKS_ROUTE;
/** 本 Tab 是否是当前页面：离开工作台或切到其它 Tab 后不再读写 URL。 */
function isCurrentTab() {
  return route.name === ROUTE_NAME && normalizeTaskTab(route.query.tab) === "replay";
}
const route = useRoute();
const router = useRouter();
// URL 保存分析对象、两处分页、创建抽屉及其预填；表单草稿与请求状态仍在页面内。
// 用户操作先写 URL 再读取，外部变化（深链接、前进后退）由下面的监听转成读取，二者用“与当前状态相同则不动”避免循环。
const parsed = computed(() => parseReplayQuery(route.query));
const spaceStore = useSpaceStore();
const strategyStore = useStrategyStore();
const replayPageSize = 20;
const tablePageSize = 50;
const seriesPageSize = 2000;
const replays = ref<Replay[]>([]);
const replayTotal = ref(0);
const replayPage = ref(parseReplayQuery(route.query).page);
const views = ref<View[]>([]);
// series 是 brief 周期记录（只有曲线与摘要字段），tableRows 是表格当前页的完整记录。
const series = ref<ReplayBar[]>([]);
const tableRows = ref<ReplayBar[]>([]);
const tableTotal = ref(0);
const tablePage = ref(1);
const tableLoading = ref(false);
const selectedId = ref("");
const selected = ref<Replay | null>(null);
const detailLoading = ref(false);
const listLoading = ref(false);
// readAt 是回放列表最近一次成功读取的时间（轮询也算）。
const readAt = ref("");
const viewsLoading = ref(false);
const starting = ref(false);
const cancelling = ref(false);
// 错误按来源分别记录：某一处成功只清除自己的错误，不会误清其它来源的提示；切换空间时全部清空。
const errors = reactive<Record<"strategies" | "views" | "list" | "detail" | "table" | "start", string>>({
  strategies: "",
  views: "",
  list: "",
  detail: "",
  table: "",
  start: ""
});
// pollFailures 是后台轮询连续失败的次数：轮询请求不弹全局错误，连续失败 3 次后页面给出一条提示。
const pollFailures = ref(0);
// awaitingMetrics 是刚取消、执行器还没写入部分指标的回放：继续轮询直到指标出现或超时。
// clock 随轮询更新，让“是否仍在等待”的判断按时间失效，而不依赖轮询里的副作用去清除。
const awaitingMetrics = ref<{ replayId: string; until: number } | null>(null);
const clock = ref(Date.now());
const chartContainer = ref<HTMLElement>();
const [presetStart, presetEnd] = recentUtcRange(30);
const form = reactive<ReplayFormState>({
  source: "definition",
  strategy_id: String(route.query.strategy_id || ""),
  dsl_yaml: "",
  instance_id: String(route.query.instance_id || ""),
  view_id: String(route.query.view_id || ""),
  start: presetStart,
  end: presetEnd,
  fee_bps: 10
});
let chart: VChart | null = null;
// 图表库体积很大（约 2MB）：第一次真正要画曲线时才加载，回放列表与创建表单不必等它。
let chartLibrary: Promise<typeof import("@visactor/vchart")> | null = null;
function loadChartLibrary() {
  chartLibrary ??= import("@visactor/vchart");
  return chartLibrary;
}
let poll: ReturnType<typeof setInterval> | null = null;
let polling = false;
// generation 随空间切换递增：旧空间发出的请求返回后一律丢弃；selectRequest 让被新选择取代的详情请求作废。
// listSeq 是列表请求的发出序号，listApplied 是已应用的最新序号：晚返回的旧请求（或旧页）不能覆盖新结果；
// listLoadingSeq 只跟踪手动加载，决定加载状态何时结束；tableLoadingRequest 同样只跟踪表格的手动加载。
let generation = 0;
let selectRequest = 0;
let tableRequest = 0;
let tableLoadingRequest = 0;
let listSeq = 0;
let listApplied = 0;
let listLoadingSeq = 0;
// userPicks 是用户在列表里手动选择的次数：发起回放等待列表期间用户另选过，就不再切到新回放。
let userPicks = 0;
// tableRefreshPending 是表格落后于服务端、需要再静默刷新一次的回放：手动请求在途时被跳过的后台刷新、失败的后台刷新
// 都记在这里；之后任何一次对它的表格请求发出时清除（读到的数据是此刻之后的），请求失败或又被跳过会重新记下。
let tableRefreshPending = "";
// tableLoadedPage 是表格当前显示的数据所属的页（0 表示还没有读到）：手动翻页失败时页码回到它。
let tableLoadedPage = 0;

// pending 是刚发出、路由还没生效的导航目标：连续两次写入（如先 push 再由 select 补写）以它为基准，
// 避免读到滞后的 route.query 而把 push 误变成 replace，或用旧的对象写回。
let pending: { state: ReplayQueryState; query: Record<string, string>; mode: "push" | "replace" } | null = null;

/** 把状态补丁写入 URL：只在回放页自己的路由上写，且与当前（或待生效）query 语义相同时不导航。 */
function writeQuery(patch: Partial<ReplayQueryState>, mode: "push" | "replace" = "replace") {
  if (!isCurrentTab()) return;
  const state = { ...(pending?.state ?? parsed.value), ...patch };
  const query = { tab: "replay", ...buildReplayQuery(state) };
  if (sameQuery(query, pending?.query ?? route.query)) return;
  // 上一次 push 还没生效就又来一次写入：后者会取消前者，必须沿用 push，否则这条历史记录就被替换掉了。
  if (pending?.mode === "push") mode = "push";
  const mine = { state, query, mode };
  pending = mine;
  const done = mode === "push" ? router.push({ name: ROUTE_NAME, query }) : router.replace({ name: ROUTE_NAME, query });
  void Promise.resolve(done).finally(() => {
    if (pending === mine) pending = null;
  });
}

const metrics = computed(() => parseMetrics(selected.value?.metrics_json));
const partialMetrics = computed(() => selected.value?.status === "cancelled" || selected.value?.status === "failed");
// 等待提示只针对正在显示、刚被取消、还没有指标的那个回放，超过等待期限即失效。
const awaitingShown = computed(() => {
  const waiting = awaitingMetrics.value;
  const current = selected.value;
  return Boolean(
    waiting &&
      current &&
      waiting.replayId === current.replay_id &&
      current.status === "cancelled" &&
      !metrics.value &&
      clock.value <= waiting.until
  );
});
const chartLabel = computed(() => equitySummary(series.value));
const detailCache = new WeakMap<ReplayBar, ReturnType<typeof parseBarDetail>>();

/** 一期的持仓账本与摘要（完整记录才有），按记录缓存解析结果。 */
function barDetail(bar: ReplayBar) {
  let detail = detailCache.get(bar);
  if (!detail) {
    detail = parseBarDetail(bar);
    detailCache.set(bar, detail);
  }
  return detail;
}

/** 回放对象的说明：定义名称（或 ID），或未保存的 DSL 文本。 */
function sourceLabel(replay: Replay): string {
  return replaySourceLabel(replay, strategyStore.strategyCatalog);
}
const tablePagination = computed(() => ({ current: tablePage.value, pageSize: tablePageSize, total: tableTotal.value }));

function pct(value: number | null) {
  return value !== null && Number.isFinite(value) ? `${(value * 100).toFixed(2)}%` : "-";
}

async function loadStrategies() {
  try {
    await strategyStore.loadAllStrategies(200);
    errors.strategies = "";
  } catch (err) {
    if (err instanceof StaleSpaceRequestError) return; // 切换空间造成的取消，空间监听会重新加载
    errors.strategies = `策略定义加载失败：${err instanceof Error ? err.message : "未知错误"}`;
  }
}

/**
 * 应用一次列表响应：已有更新的响应、页码已变或空间已切换时丢弃，返回是否应用。
 * 还没有选中回放时选中第一条：手动加载与轮询哪个先应用都会触发，选择在后台进行，不拖住列表的加载态。
 */
function applyReplayList(gen: number, seq: number, page: number, result: PageResult<Replay>): boolean {
  if (gen !== generation || seq <= listApplied || page !== replayPage.value) return false;
  listApplied = seq;
  readAt.value = new Date().toLocaleTimeString("zh-CN", { hour12: false });
  replays.value = result.items;
  replayTotal.value = result.page.total;
  errors.list = "";
  if (!selectedId.value && !(pending?.state ?? parsed.value).replayId && result.items.length)
    void select(result.items[0].replay_id);
  return true;
}

async function loadReplays() {
  const gen = generation;
  const seq = ++listSeq;
  const loadingSeq = ++listLoadingSeq;
  const page = replayPage.value;
  listLoading.value = true;
  try {
    const result = await listReplays({ page, page_size: replayPageSize });
    if (applyReplayList(gen, seq, page, result)) pollFailures.value = 0;
  } catch (err) {
    if (gen === generation && seq > listApplied)
      errors.list = `回放列表加载失败：${err instanceof Error ? err.message : "未知错误"}`;
  } finally {
    if (gen === generation && loadingSeq === listLoadingSeq) listLoading.value = false;
  }
}

function changeReplayPage(page: number) {
  replayPage.value = page;
  writeQuery({ page });
  loadReplays();
}

async function loadViews() {
  const gen = generation;
  const spaceId = spaceStore.selectedSpaceId;
  if (!spaceId) return;
  viewsLoading.value = true;
  try {
    const items: View[] = [];
    for (let page = 1; ; page += 1) {
      const rsp = await listViews({ space_id: spaceId, status: "active", page: { page, size: 200 } });
      if (gen !== generation) return;
      items.push(...(rsp.views || []));
      if (!rsp.page_result?.has_more || !(rsp.views || []).length) break;
    }
    views.value = items;
    errors.views = "";
  } catch (err) {
    if (gen === generation) errors.views = `View 列表加载失败：${err instanceof Error ? err.message : "未知错误"}`;
  } finally {
    if (gen === generation) viewsLoading.value = false;
  }
}

/** 从已加载的 brief 记录之后增量读取；isCurrent 为假表示请求已被新选择或空间切换取代，返回 null。 */
async function fetchSeries(
  replayId: string,
  known: ReplayBar[],
  isCurrent: () => boolean,
  silent = false
): Promise<ReplayBar[] | null> {
  let merged = known;
  for (let page = Math.floor(known.length / seriesPageSize) + 1; ; page += 1) {
    const next = await listReplayBars(replayId, { page, page_size: seriesPageSize, brief: true }, { silent });
    if (!isCurrent()) return null;
    merged = mergeBars(merged, next.items);
    if (next.items.length < seriesPageSize || merged.length >= next.page.total) return merged;
  }
}

/**
 * 读取表格当前页的完整记录（含目标与持仓）。被更新的表格请求、选择或空间切换取代时丢弃结果与错误；
 * 成功时清除表格的错误。silent 用于后台轮询：失败交给轮询计数，不在页面上单独提示。
 */
async function loadTablePage(replayId: string, isCurrent: () => boolean, silent = false) {
  const requestId = ++tableRequest;
  // 轮询刷新不显示加载遮罩：运行中的回放停在末页时，每 5 秒闪一次会很干扰。
  const loadingId = silent ? 0 : ++tableLoadingRequest;
  const live = () => isCurrent() && requestId === tableRequest;
  if (tableRefreshPending === replayId) tableRefreshPending = "";
  const page = tablePage.value;
  if (!silent) tableLoading.value = true;
  try {
    const result = await listReplayBars(replayId, { page, page_size: tablePageSize }, { silent });
    if (!live()) return;
    tableRows.value = result.items;
    tableTotal.value = result.page.total;
    tableLoadedPage = page;
    errors.table = "";
  } catch (err) {
    if (!live()) return;
    if (silent) {
      // 后台刷新失败：回放可能已是终态、轮询不再刷新表格，记下待刷新让轮询继续重试（连续失败由轮询计数提示）。
      tableRefreshPending = replayId;
      throw err;
    }
    // 手动翻页失败：页码回到上一次成功显示的页。Arco 的分页只在页码变化时触发翻页，页码停在失败的那一页就点不动了。
    // 还没有成功显示过任何页（首次加载失败）时回到第 1 页。
    tablePage.value = tableLoadedPage || 1;
    writeQuery({ barPage: tablePage.value });
    // 表格停在旧快照：记为待刷新，结尾补一次静默刷新，之后由轮询继续重试（回放已是终态时也一样）。
    tableRefreshPending = replayId;
    errors.table = `周期记录加载失败：${err instanceof Error ? err.message : "未知错误"}`;
  } finally {
    if (!silent && loadingId === tableLoadingRequest) {
      tableLoading.value = false;
      // 手动请求在途时跳过了后台刷新：回放可能已经结束、轮询不会再刷新表格，这里补一次，免得表格停在手动请求读到的旧快照上。
      if (tableRefreshPending === replayId && isCurrent()) {
        void loadTablePage(replayId, isCurrent, true).catch(err => {
          errors.table = `周期记录加载失败：${err instanceof Error ? err.message : "未知错误"}`;
        });
      }
    }
  }
}

/**
 * 是否等待部分指标：已取消、已经有周期、还没有指标的回放，执行器读完当前分段才写入指标，继续轮询等它（最多 2 分钟，
 * 按浏览器时钟计时）。observedCancel 表示亲眼看到它被取消（本页取消成功，或轮询看到从排队、运行中变为已取消），
 * 直接等待；直接打开一个已取消的回放时才判断它是不是刚取消的，并且用服务端时间比较（浏览器时钟可能不准），
 * 读不到服务端时间就不等待。
 */
function maybeAwaitMetrics(replay: Replay, observedCancel: boolean) {
  if (replay.status !== "cancelled" || parseMetrics(replay.metrics_json) || !series.value.length) return;
  if (awaitingMetrics.value?.replayId === replay.replay_id) return;
  // 直接打开时等待期限从取消时刻（按服务端时间换算）算起，而不是从打开页面算起。
  let elapsed = 0;
  if (!observedCancel) {
    const now = serverNow();
    const updated = Date.parse(replay.updated_at);
    if (now === null || !Number.isFinite(updated) || now - updated > 120_000) return;
    elapsed = Math.max(0, now - updated);
  }
  clock.value = Date.now();
  awaitingMetrics.value = { replayId: replay.replay_id, until: clock.value + 120_000 - elapsed };
}

/** 用户在列表里选择一个回放。 */
function pick(replayId: string) {
  userPicks += 1;
  writeQuery({ replayId, barPage: 1 }, "push");
  void select(replayId);
}

/**
 * 选择并加载一个回放。observedCancel 见 maybeAwaitMetrics；keepTablePage 为 true 且重读的是同一个回放时，表格停在
 * 当前页（取消后的整体重读）。
 */
async function select(replayId: string, options: { observedCancel?: boolean; keepTablePage?: boolean; tablePage?: number } = {}) {
  const requestId = ++selectRequest;
  const gen = generation;
  const isCurrent = () => requestId === selectRequest && gen === generation;
  const page = options.tablePage ?? (options.keepTablePage && selectedId.value === replayId ? tablePage.value : 1);
  // 自动选择、发起后选择等没有经过 pick 的入口也要让 URL 指向当前对象（已指向时不动）。
  writeQuery({ replayId, barPage: page });
  // 立即清掉上一个回放的详情与错误：加载期间不能显示旧回放，取消等操作也不能作用在旧回放上。
  tableLoadedPage = 0;
  selectedId.value = replayId;
  selected.value = null;
  errors.detail = "";
  series.value = [];
  tableRows.value = [];
  tableTotal.value = 0;
  tablePage.value = page;
  errors.table = "";
  chart?.release();
  chart = null;
  detailLoading.value = true;
  try {
    const replay = await getReplay(replayId);
    if (!isCurrent()) return;
    const all = await fetchSeries(replayId, [], isCurrent);
    if (all === null) return;
    selected.value = replay;
    series.value = all;
    errors.detail = "";
    pollFailures.value = 0;
    maybeAwaitMetrics(replay, Boolean(options.observedCancel));
    detailLoading.value = false;
    await Promise.all([renderChart(true), loadTablePage(replayId, isCurrent)]);
  } catch (err) {
    if (isCurrent()) errors.detail = `回放详情加载失败：${err instanceof Error ? err.message : "未知错误"}`;
  } finally {
    if (isCurrent()) detailLoading.value = false;
  }
}

function changeTablePage(page: number) {
  tablePage.value = page;
  writeQuery({ barPage: page });
  const replayId = selectedId.value;
  const requestId = selectRequest;
  const gen = generation;
  void loadTablePage(replayId, () => requestId === selectRequest && gen === generation && selectedId.value === replayId);
}

function retryDetail() {
  if (selectedId.value) void select(selectedId.value, { tablePage: tablePage.value });
}

function retryTable() {
  const replayId = selectedId.value;
  const requestId = selectRequest;
  const gen = generation;
  if (replayId)
    void loadTablePage(replayId, () => requestId === selectRequest && gen === generation && selectedId.value === replayId);
}

/** 所选回放是否还需要轮询：排队或运行中，或刚取消、部分指标尚未写入（最多等 2 分钟），或表格落后、待刷新。 */
function selectedNeedsRefresh(): boolean {
  const current = selected.value;
  if (!current || current.replay_id !== selectedId.value) return false;
  return (
    current.status === "pending" ||
    current.status === "running" ||
    awaitingShown.value ||
    tableRefreshPending === current.replay_id
  );
}

/** 轮询只刷新需要刷新的所选回放：曲线增量追加并就地更新，表格停在末页时一并刷新。 */
async function refreshSelected() {
  const current = selected.value;
  if (!current || !selectedNeedsRefresh()) return;
  const replayId = current.replay_id;
  const requestId = selectRequest;
  const gen = generation;
  const isCurrent = () => requestId === selectRequest && gen === generation && selectedId.value === replayId;
  const replay = await getReplay(replayId, { silent: true });
  if (!isCurrent()) return;
  const all = await fetchSeries(replayId, series.value, isCurrent, true);
  if (all === null) return;
  selected.value = replay;
  if (all.length !== series.value.length) {
    series.value = all;
    await renderChart(false);
  }
  maybeAwaitMetrics(replay, current.status === "pending" || current.status === "running");
  const lastPage = Math.max(1, Math.ceil(all.length / tablePageSize));
  // 表格没有落后、停在较早的页、状态也没变化时不用刷新。
  if (tableRefreshPending !== replayId && tablePage.value < lastPage - 1 && replay.status === current.status) return;
  // 用户正在手动翻页（遮罩还在）时不做后台刷新：后台请求会把还没返回的手动请求作废，失败时表格就停在旧页的数据上。
  // 记下待刷新，由手动请求结束时补上。
  if (tableLoading.value) {
    tableRefreshPending = replayId;
    return;
  }
  await loadTablePage(replayId, isCurrent, true);
}

async function renderChart(rebuild: boolean) {
  await nextTick();
  const values = equitySeries(series.value);
  if (!rebuild && chart) {
    await chart.updateData("equity", values);
    return;
  }
  chart?.release();
  chart = null;
  if (!chartContainer.value || !values.length) return;
  const { default: ChartClass } = await loadChartLibrary();
  // 加载期间可能已换了回放、已由另一次绘制建好图表或页面已卸载：以最新的序列与容器为准，不重复创建。
  const latest = equitySeries(series.value);
  if (chart || !chartContainer.value || !latest.length) return;
  chart = new ChartClass(
    {
      type: "line",
      data: [{ id: "equity", values: latest }],
      xField: "time",
      yField: "value",
      axes: [
        // 页面其余位置都按 UTC 显示：时间轴与提示框同样用 UTC，并带上时分。
        { orient: "bottom", type: "time", layers: [{ timeFormat: "%m-%d %H:%M", timeFormatMode: "utc" }] },
        { orient: "left", type: "linear", zero: false }
      ],
      line: { style: { lineWidth: 2 } },
      point: { visible: false },
      tooltip: {
        mark: {
          title: { value: (datum: any) => utcLabel(datum?.time) },
          content: [{ key: "权益", value: (datum: any) => Number(datum?.value).toFixed(4) }]
        },
        dimension: {
          title: { value: (datum: any) => utcLabel(datum?.time) },
          content: [{ key: "权益", value: (datum: any) => Number(datum?.value).toFixed(4) }]
        }
      }
    },
    { dom: chartContainer.value }
  );
  chart.renderSync();
}

function utcLabel(time: unknown): string {
  const value = typeof time === "number" ? time : Number(time);
  return Number.isFinite(value) ? formatUtcTime(new Date(value).toISOString()) : "";
}

async function start() {
  errors.start = validateReplayForm(form, parseUtcInput);
  const startTime = parseUtcInput(form.start);
  const endTime = parseUtcInput(form.end);
  const source = replaySourceOf(form);
  if (errors.start || !source || !startTime || !endTime) return;
  const gen = generation;
  const picks = userPicks;
  starting.value = true;
  try {
    const started = await startReplay({
      ...source,
      view_id: form.view_id,
      start_time: startTime,
      end_time: endTime,
      fee_bps: Number(form.fee_bps) || 0
    });
    if (gen !== generation) return;
    Message.success(
      `回放已排队：共 ${started.bar_count} 根，首根 ${formatUtcTime(started.first_bar_end)}，末根 ${formatUtcTime(started.last_bar_end)}`
    );
    closeCreate();
    replayPage.value = 1;
    writeQuery({ page: 1 });
    await loadReplays();
    // 等待期间用户在列表里另选了回放：尊重用户的选择，不再切到新回放。
    if (gen !== generation || userPicks !== picks) return;
    // 列表原先为空时，列表加载会自动选中第一条（就是新回放），已经在加载，不必再选一次。
    if (selectedId.value !== started.replay.replay_id) await select(started.replay.replay_id);
  } catch (err) {
    if (gen === generation) errors.start = `回放发起失败：${err instanceof Error ? err.message : "未知错误"}`;
  } finally {
    starting.value = false;
  }
}

async function cancel() {
  // 只能取消正在显示的回放：切换选择后详情尚未加载完成时不能作用在旧回放上。
  if (!selected.value || selected.value.replay_id !== selectedId.value) return;
  const gen = generation;
  const replayId = selected.value.replay_id;
  cancelling.value = true;
  try {
    await cancelReplay(replayId);
    if (gen !== generation) return;
    await loadReplays();
    // 取消后整体重读：补上取消前写入的最后几根，表格停在当前页；本页取消成功即进入等待部分指标。
    if (gen === generation && selectedId.value === replayId)
      await select(replayId, { observedCancel: true, keepTablePage: true });
  } catch (err) {
    // 请求层已提示过的错误不再重复；切换空间后，旧空间的取消失败不再提示。
    if (gen === generation) reportControlError(err);
  } finally {
    cancelling.value = false;
  }
}

function openCreate() {
  if (!parsed.value.create) writeQuery({ create: true }, "push");
}
/** 关闭创建抽屉：预填一并从 URL 清除（它们只表示创建来源，不表示分析对象）。 */
function closeCreate() {
  writeQuery({ create: false });
}
/** 建立创建上下文时应用入口预填；之后的翻页、选择、轮询都不会覆盖正在编辑的草稿。 */
function applyPrefill() {
  const { strategyId, instanceId, viewId } = parsed.value.prefill;
  if (instanceId) form.instance_id = instanceId;
  else if (strategyId) {
    form.instance_id = "";
    form.strategy_id = strategyId;
    form.source = "definition";
  }
  if (viewId) form.view_id = viewId;
}
/** URL 里有显式 replay_id 时直接读取它（即使不在当前列表页），失败也不回退到第一条。 */
function applyRouteSelection() {
  const { replayId, barPage } = parsed.value;
  if (replayId && replayId !== selectedId.value) void select(replayId, { tablePage: barPage });
}

function startPolling() {
  poll = setInterval(async () => {
    clock.value = Date.now();
    if (awaitingMetrics.value && clock.value > awaitingMetrics.value.until) awaitingMetrics.value = null;
    if (polling) return;
    if (typeof document !== "undefined" && document.visibilityState !== "visible") return;
    // 所选回放不在当前列表页时也要刷新：轮询条件看列表与所选回放两处。
    if (!replays.value.some(item => item.status === "pending" || item.status === "running") && !selectedNeedsRefresh()) return;
    polling = true;
    const gen = generation;
    const seq = ++listSeq;
    const page = replayPage.value;
    try {
      // 轮询请求不弹全局错误：失败由下面的计数在页面上统一提示，断网时不会每 5 秒弹出一串错误。
      const result = await listReplays({ page, page_size: replayPageSize }, { silent: true });
      if (gen !== generation) return;
      applyReplayList(gen, seq, page, result);
      await refreshSelected();
      if (gen === generation) pollFailures.value = 0;
    } catch {
      if (gen === generation) pollFailures.value += 1;
    } finally {
      polling = false;
    }
  }, 5000);
}

onMounted(() => {
  // 策略定义按空间授权：空间尚未初始化时请求会被拒绝，由下面的空间监听在就绪后补载。
  if (spaceStore.selectedSpaceId) {
    loadStrategies();
    applyRouteSelection();
  }
  loadViews();
  loadReplays();
  startPolling();
  normalizeEntry();
});
/** 旧入口只带来源预填、没有 create=1：规范化为打开创建抽屉。 */
function normalizeEntry() {
  if (!parsed.value.create && hasPrefill(parsed.value.prefill)) writeQuery({ create: true });
}
watch(
  () => parsed.value.create,
  open => {
    if (open) applyPrefill();
  },
  { immediate: true }
);
// URL 的外部变化：预填入口、前进后退、深链接。
watch(
  () =>
    [
      parsed.value.replayId,
      parsed.value.barPage,
      parsed.value.page,
      hasPrefill(parsed.value.prefill) && !parsed.value.create
    ] as const,
  ([replayId, barPage, page, legacy]) => {
    if (!isCurrentTab()) return;
    if (legacy) normalizeEntry();
    if (page !== replayPage.value) {
      replayPage.value = page;
      void loadReplays();
    }
    if (!spaceStore.selectedSpaceId) return;
    // URL 去掉了 replay_id（例如再次点击菜单进入本页）：本地选择跟着回到默认——选中列表第一条并写回 URL。
    if (!replayId) {
      if (selectedId.value && !pending && replays.value.length) void select(replays.value[0].replay_id);
      return;
    }
    if (replayId !== selectedId.value) {
      // 外部导航（前进后退、深链接）等同用户另选：发起回放等待期间发生时，完成后不再抢选新回放。
      userPicks += 1;
      void select(replayId, { tablePage: barPage });
    } else if (barPage !== tablePage.value) changeTablePage(barPage);
  }
);

watch(
  () => spaceStore.selectedSpaceId,
  (next, previous) => {
    generation += 1;
    selectRequest += 1;
    tableRequest += 1;
    selectedId.value = "";
    selected.value = null;
    detailLoading.value = false;
    awaitingMetrics.value = null;
    // 策略定义按空间授权，随空间切换重新加载；各来源的错误随空间切换清空。
    for (const key of Object.keys(errors) as (keyof typeof errors)[]) errors[key] = "";
    if (next) void loadStrategies();
    replays.value = [];
    replayTotal.value = 0;
    series.value = [];
    tableRows.value = [];
    tableTotal.value = 0;
    views.value = [];
    // 空间初始化（无 → 有）只是首次加载，保留深链接与预填；真正的 A → B 切换才清空对象、分页、创建上下文。
    if (previous) {
      form.view_id = "";
      form.instance_id = "";
      replayPage.value = 1;
      writeQuery({ replayId: "", page: 1, barPage: 1, create: false });
    }
    listLoading.value = false;
    viewsLoading.value = false;
    tableLoading.value = false;
    pollFailures.value = 0;
    chart?.release();
    chart = null;
    loadViews();
    loadReplays();
    // 只有空间初始化才按 URL 恢复深链接选择；真实切换时旧空间的 replay_id 不属于新空间，由新列表自动选择。
    if (next && !previous) applyRouteSelection();
  }
);
onBeforeUnmount(() => {
  if (poll) clearInterval(poll);
  // 作废进行中的请求：返回后不再翻页读取、不再渲染图表。
  generation += 1;
  selectRequest += 1;
  tableRequest += 1;
  chart?.release();
  chart = null;
});
</script>

<style scoped lang="scss">
@use "../shared/strategy-page.scss";

.muted {
  color: var(--color-text-3);
  font-size: 12px;
}
.top-alert {
  margin-bottom: var(--moox-space-2);
}
.section-title {
  margin-bottom: 8px;
  font-weight: 600;
}
.mono {
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  font-size: 12px;
}
.detail-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 6px;
}
.detail-info {
  margin-bottom: 12px;
}
.metrics {
  display: grid;
  grid-template-columns: repeat(4, minmax(0, 1fr));
  gap: 10px;
  margin-bottom: 12px;
}
.metric {
  display: grid;
  gap: 4px;
  padding: 10px;
  background: var(--color-fill-2);
}
.metric span {
  color: var(--color-text-3);
  font-size: 12px;
}
.metric strong {
  font-size: 16px;
}
.up {
  color: rgb(var(--green-6));
}
.down {
  color: rgb(var(--red-6));
}
.chart-wrap {
  margin-bottom: 12px;
}
.chart {
  min-height: 260px;
  width: 100%;
}
.limits {
  margin-bottom: 12px;
}
.limits ul {
  margin: 0;
  padding-left: 18px;
}
.targets {
  color: var(--color-text-2);
  font-size: 12px;
}
.detail-loading {
  display: flex;
  justify-content: center;
  padding: 48px 0;
}
.dsl {
  margin: 0;
  white-space: pre-wrap;
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  font-size: 12px;
}
.bar-detail {
  display: grid;
  gap: 6px;
  font-size: 12px;
}
@media (max-width: 900px) {
  .metrics {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }
}
</style>
