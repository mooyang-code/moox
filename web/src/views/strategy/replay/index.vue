<template>
  <div class="moox-page">
    <div class="moox-inner">
      <div class="page-head">
        <div>
          <h2>策略回放</h2>
          <span>基于 View 的研究回放：现货理论账本、按当期收盘价成交，用于比较策略版本，不承诺复现实时结果。</span>
        </div>
        <a-space
          ><a-button :loading="listLoading" aria-label="刷新回放" @click="loadReplays"
            ><template #icon><icon-refresh /></template>刷新</a-button
          ></a-space
        >
      </div>
      <a-alert v-for="message in errorMessages" :key="message" type="error" show-icon class="top-alert">{{ message }}</a-alert>
      <a-alert v-if="pollFailures >= 3" type="warning" show-icon class="top-alert"
        >后台暂时无法访问，页面上的回放状态可能不是最新；恢复后会自动刷新。</a-alert
      >
      <a-grid :cols="{ xs: 1, md: 3 }" :col-gap="20" :row-gap="16">
        <a-grid-item>
          <div class="section-title">发起回放</div>
          <a-form layout="vertical">
            <a-form-item v-if="form.instance_id" label="回放对象">
              <a-alert type="info" class="instance-alert"
                >按实例 <span class="mono">{{ form.instance_id }}</span>
                当前会话（停用时为最近一次会话）固化的 DSL 版本回放；重新启用实例会按定义的当前版本重新解析。
                <template #action
                  ><a-button size="mini" type="text" @click="form.instance_id = ''">改为选择定义</a-button></template
                ></a-alert
              >
            </a-form-item>
            <template v-else>
              <a-form-item label="回放对象">
                <a-radio-group v-model="form.source" type="button" size="small">
                  <a-radio value="definition">已保存的定义</a-radio>
                  <a-radio value="dsl">DSL 文本（草稿）</a-radio>
                </a-radio-group>
              </a-form-item>
              <a-form-item v-if="form.source === 'definition'" label="策略定义" required>
                <a-select v-model="form.strategy_id" allow-search placeholder="选择定义">
                  <a-option v-for="item in strategyStore.strategies" :key="item.strategy_id" :value="item.strategy_id"
                    >{{ item.name }}（{{ item.strategy_id }}）</a-option
                  >
                </a-select>
              </a-form-item>
              <a-form-item v-else label="DSL 文本" required>
                <a-textarea
                  v-model="form.dsl_yaml"
                  aria-label="回放用的 DSL 文本"
                  :auto-size="{ minRows: 6, maxRows: 16 }"
                  placeholder="粘贴未保存的策略 DSL，直接回放"
                />
              </a-form-item>
            </template>
            <a-form-item label="View" required>
              <a-select v-model="form.view_id" allow-search :loading="viewsLoading" placeholder="选择 View（第一版只支持现货）">
                <a-option v-for="view in views" :key="view.view_id" :value="view.view_id"
                  >{{ view.name || view.view_id }}（{{ view.view_id }} · {{ view.freq || "无周期" }}）</a-option
                >
              </a-select>
            </a-form-item>
            <a-form-item label="区间（UTC，左闭右开，格式 YYYY-MM-DD HH:mm）" required>
              <div class="range-inputs">
                <a-input v-model="form.start" aria-label="开始时间（UTC）" placeholder="开始，例如 2026-09-01 00:00" />
                <a-input v-model="form.end" aria-label="结束时间（UTC）" placeholder="结束，例如 2026-10-01 00:00" />
              </div>
              <a-space class="presets" :size="4">
                <a-button size="mini" @click="applyPreset(1)">近 1 天</a-button>
                <a-button size="mini" @click="applyPreset(7)">近 7 天</a-button>
                <a-button size="mini" @click="applyPreset(30)">近 30 天</a-button>
              </a-space>
            </a-form-item>
            <a-form-item label="单边手续费（bps）"
              ><a-input-number v-model="form.fee_bps" :min="0" :max="1000" :step="1"
            /></a-form-item>
            <a-button type="primary" long :loading="starting" @click="start">开始回放</a-button>
          </a-form>
          <div class="section-title list-title">回放记录</div>
          <a-list :data="replays" :loading="listLoading" :bordered="false" size="small">
            <template #item="{ item }">
              <a-list-item :class="{ active: item.replay_id === selectedId }">
                <button
                  type="button"
                  class="replay-row"
                  :aria-pressed="item.replay_id === selectedId"
                  @click="select(item.replay_id)"
                >
                  <span class="replay-line">
                    <a-tag size="small" :color="replayStatusColor(item.status)">{{ replayStatusLabel(item.status) }}</a-tag>
                    <span>{{ sourceLabel(item) }}</span>
                  </span>
                  <span class="replay-line muted mono">{{ item.view_id }}</span>
                  <span class="replay-line muted">{{ formatUtcTime(item.start_time) }} → {{ formatUtcTime(item.end_time) }}</span>
                  <span v-if="item.status === 'running' && item.progress_time" class="replay-line muted">
                    进度 {{ formatUtcTime(item.progress_time) }}
                  </span>
                </button>
              </a-list-item>
            </template>
          </a-list>
          <a-pagination
            v-if="replayTotal > replayPageSize"
            class="replay-pager"
            size="small"
            simple
            :current="replayPage"
            :page-size="replayPageSize"
            :total="replayTotal"
            @change="changeReplayPage"
          />
        </a-grid-item>
        <a-grid-item :span="{ xs: 1, md: 2 }">
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
              · View {{ selected.view_id }} · 区间 {{ formatUtcTime(selected.start_time) }} →
              {{ formatUtcTime(selected.end_time) }} · 手续费 {{ selected.fee_bps }} bps
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
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, reactive, ref, watch } from "vue";
import { Message } from "@arco-design/web-vue";
import { default as VChart } from "@visactor/vchart";
import { useRoute } from "vue-router";
import { cancelReplay, getReplay, listReplayBars, listReplays, startReplay } from "@/api/strategy";
import type { PageResult } from "@/api/strategy";
import type { InstrumentTarget, Replay, ReplayBar, Strategy } from "@/api/strategy-types";
import { listViews } from "@/api/storage/metadata";
import type { View } from "@/api/storage/types";
import { useSpaceStore } from "@/store/modules/space";
import { useStrategyStore } from "@/store/modules/strategy";
import { percent, shortHash, skipReasonLabel } from "@/views/strategy/model";
import {
  equitySeries,
  equitySummary,
  formatUtcTime,
  mergeBars,
  parseBarDetail,
  parseMetrics,
  parseUtcInput,
  recentUtcRange,
  replayStatusColor,
  replayStatusLabel
} from "@/views/strategy/replay/model";

defineOptions({ name: "StrategyReplay" });
const route = useRoute();
const spaceStore = useSpaceStore();
const strategyStore = useStrategyStore();
const replayPageSize = 20;
const tablePageSize = 50;
const seriesPageSize = 2000;
const replays = ref<Replay[]>([]);
const replayTotal = ref(0);
const replayPage = ref(1);
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
const errorMessages = computed(() => Object.values(errors).filter(Boolean));
// pollFailures 是后台轮询连续失败的次数：轮询请求不弹全局错误，连续失败 3 次后页面给出一条提示。
const pollFailures = ref(0);
// awaitingMetrics 是刚取消、执行器还没写入部分指标的回放：继续轮询直到指标出现或超时。
// clock 随轮询更新，让“是否仍在等待”的判断按时间失效，而不依赖轮询里的副作用去清除。
const awaitingMetrics = ref<{ replayId: string; until: number } | null>(null);
const clock = ref(Date.now());
const chartContainer = ref<HTMLElement>();
const [presetStart, presetEnd] = recentUtcRange(30);
const form = reactive<{
  source: "definition" | "dsl";
  strategy_id: string;
  dsl_yaml: string;
  instance_id: string;
  view_id: string;
  start: string;
  end: string;
  fee_bps: number;
}>({
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
let poll: ReturnType<typeof setInterval> | null = null;
let polling = false;
// generation 随空间切换递增：旧空间发出的请求返回后一律丢弃；selectRequest 让被新选择取代的详情请求作废。
// listSeq 是列表请求的发出序号，listApplied 是已应用的最新序号：晚返回的旧请求（或旧页）不能覆盖新结果；
// listLoadingSeq 只跟踪手动加载，决定加载状态何时结束。
let generation = 0;
let selectRequest = 0;
let tableRequest = 0;
let listSeq = 0;
let listApplied = 0;
let listLoadingSeq = 0;

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
  if (!replay.strategy_id) return "DSL 文本（草稿）";
  const definition = strategyStore.strategies.find((item: Strategy) => item.strategy_id === replay.strategy_id);
  return definition ? `${definition.name}（${replay.strategy_id}）` : replay.strategy_id;
}
const tablePagination = computed(() => ({ current: tablePage.value, pageSize: tablePageSize, total: tableTotal.value }));

function pct(value: number | null) {
  return value !== null && Number.isFinite(value) ? `${(value * 100).toFixed(2)}%` : "-";
}

function applyPreset(days: number) {
  [form.start, form.end] = recentUtcRange(days);
}

async function loadStrategies() {
  try {
    await strategyStore.loadAllStrategies(200);
    errors.strategies = "";
  } catch (err) {
    errors.strategies = `策略定义加载失败：${err instanceof Error ? err.message : "未知错误"}`;
  }
}

/** 应用一次列表响应：已有更新的响应、页码已变或空间已切换时丢弃，返回是否应用。 */
function applyReplayList(gen: number, seq: number, page: number, result: PageResult<Replay>): boolean {
  if (gen !== generation || seq <= listApplied || page !== replayPage.value) return false;
  listApplied = seq;
  replays.value = result.items;
  replayTotal.value = result.page.total;
  errors.list = "";
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
    if (!applyReplayList(gen, seq, page, result)) return;
    pollFailures.value = 0;
    if (!selectedId.value && result.items.length) await select(result.items[0].replay_id);
  } catch (err) {
    if (gen === generation && seq > listApplied)
      errors.list = `回放列表加载失败：${err instanceof Error ? err.message : "未知错误"}`;
  } finally {
    if (gen === generation && loadingSeq === listLoadingSeq) listLoading.value = false;
  }
}

function changeReplayPage(page: number) {
  replayPage.value = page;
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
  const live = () => isCurrent() && requestId === tableRequest;
  tableLoading.value = true;
  try {
    const result = await listReplayBars(replayId, { page: tablePage.value, page_size: tablePageSize }, { silent });
    if (!live()) return;
    tableRows.value = result.items;
    tableTotal.value = result.page.total;
    errors.table = "";
  } catch (err) {
    if (!live()) return;
    if (silent) throw err;
    errors.table = `周期记录加载失败：${err instanceof Error ? err.message : "未知错误"}`;
  } finally {
    if (requestId === tableRequest) tableLoading.value = false;
  }
}

async function select(replayId: string) {
  const requestId = ++selectRequest;
  const gen = generation;
  const isCurrent = () => requestId === selectRequest && gen === generation;
  // 立即清掉上一个回放的详情：加载期间不能显示旧回放，取消等操作也不能作用在旧回放上。
  selectedId.value = replayId;
  selected.value = null;
  series.value = [];
  tableRows.value = [];
  tableTotal.value = 0;
  tablePage.value = 1;
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
  const replayId = selectedId.value;
  const requestId = selectRequest;
  const gen = generation;
  void loadTablePage(replayId, () => requestId === selectRequest && gen === generation && selectedId.value === replayId);
}

/** 所选回放是否还需要轮询：排队或运行中，或刚取消、部分指标尚未写入（最多等 2 分钟）。 */
function selectedNeedsRefresh(): boolean {
  const current = selected.value;
  if (!current || current.replay_id !== selectedId.value) return false;
  return current.status === "pending" || current.status === "running" || awaitingShown.value;
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
  const lastPage = Math.max(1, Math.ceil(all.length / tablePageSize));
  if (tablePage.value >= lastPage - 1 || replay.status !== current.status) await loadTablePage(replayId, isCurrent, true);
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
  chart = new VChart(
    {
      type: "line",
      data: [{ id: "equity", values }],
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

/** 回放对象三选一：实例会话固化的版本、已保存的定义，或未保存的 DSL 文本。 */
function replaySource(): { instance_id: string } | { strategy_id: string } | { dsl_yaml: string } | null {
  if (form.instance_id) return { instance_id: form.instance_id };
  if (form.source === "dsl") return form.dsl_yaml.trim() ? { dsl_yaml: form.dsl_yaml } : null;
  return form.strategy_id ? { strategy_id: form.strategy_id } : null;
}

async function start() {
  errors.start = "";
  const startTime = parseUtcInput(form.start);
  const endTime = parseUtcInput(form.end);
  const source = replaySource();
  if (!form.view_id || !source) {
    errors.start = "请选择 View，并选择定义、粘贴 DSL 文本或从实例详情进入";
    return;
  }
  if (!startTime || !endTime) {
    errors.start = "时间格式应为 YYYY-MM-DD HH:mm（UTC），例如 2026-09-01 00:00";
    return;
  }
  const gen = generation;
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
    replayPage.value = 1;
    await loadReplays();
    if (gen !== generation) return;
    await select(started.replay.replay_id);
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
  // 执行器只在已经算出周期时才写部分指标：取消前正在运行、且已经有周期或进度的回放才值得等待。
  const expectMetrics = selected.value.status === "running" && (series.value.length > 0 || Boolean(selected.value.progress_time));
  cancelling.value = true;
  try {
    await cancelReplay(replayId);
    if (gen !== generation) return;
    // 执行器读完当前分段才写入部分指标：继续轮询这个回放，直到指标出现或超时。
    clock.value = Date.now();
    awaitingMetrics.value = expectMetrics ? { replayId, until: clock.value + 120_000 } : null;
    await loadReplays();
    // 取消后整体重读：补上取消前写入的最后几根与部分指标。
    if (gen === generation && selectedId.value === replayId) await select(replayId);
  } catch (err) {
    Message.error(err instanceof Error ? err.message : "取消失败");
  } finally {
    cancelling.value = false;
  }
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
  loadStrategies();
  loadViews();
  loadReplays();
  startPolling();
});
watch(
  () => spaceStore.selectedSpaceId,
  () => {
    generation += 1;
    selectRequest += 1;
    tableRequest += 1;
    selectedId.value = "";
    selected.value = null;
    detailLoading.value = false;
    awaitingMetrics.value = null;
    for (const key of Object.keys(errors) as (keyof typeof errors)[]) errors[key] = "";
    series.value = [];
    tableRows.value = [];
    tableTotal.value = 0;
    views.value = [];
    form.view_id = "";
    form.instance_id = "";
    replayPage.value = 1;
    listLoading.value = false;
    viewsLoading.value = false;
    tableLoading.value = false;
    pollFailures.value = 0;
    chart?.release();
    chart = null;
    loadViews();
    loadReplays();
  }
);
onBeforeUnmount(() => {
  if (poll) clearInterval(poll);
  chart?.release();
});
</script>

<style scoped>
.page-head {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 16px;
  margin-bottom: var(--moox-space-2);
}
.page-head h2 {
  margin: 0 0 4px;
}
.page-head span,
.muted {
  color: var(--color-text-3);
  font-size: 12px;
}
.top-alert {
  margin-bottom: var(--moox-space-2);
}
.instance-alert {
  width: 100%;
}
.section-title {
  margin-bottom: 8px;
  font-weight: 600;
}
.list-title {
  margin-top: 20px;
}
.range-inputs {
  display: grid;
  gap: 6px;
  width: 100%;
}
.presets {
  margin-top: 6px;
}
.replay-row {
  display: grid;
  gap: 2px;
  width: 100%;
  padding: 0;
  border: none;
  background: transparent;
  color: inherit;
  font: inherit;
  text-align: left;
  cursor: pointer;
}
.replay-row:focus-visible {
  outline: 2px solid rgb(var(--arcoblue-6));
  outline-offset: 2px;
}
.replay-line {
  display: block;
}
.replay-pager {
  margin-top: 8px;
}
.active {
  background: var(--color-fill-2);
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
@media (max-width: 640px) {
  .page-head {
    align-items: flex-start;
    flex-direction: column;
  }
}
</style>
