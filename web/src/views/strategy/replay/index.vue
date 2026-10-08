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
      <a-alert v-if="error" type="error" show-icon class="top-alert">{{ error }}</a-alert>
      <a-grid :cols="{ xs: 1, md: 3 }" :col-gap="20" :row-gap="16">
        <a-grid-item>
          <div class="section-title">发起回放</div>
          <a-form layout="vertical">
            <a-form-item label="策略定义" required>
              <a-select v-model="form.strategy_id" allow-search placeholder="选择定义">
                <a-option v-for="item in strategyStore.strategies" :key="item.strategy_id" :value="item.strategy_id"
                  >{{ item.name }}（{{ item.strategy_id }}）</a-option
                >
              </a-select>
            </a-form-item>
            <a-form-item label="View" required>
              <a-select v-model="form.view_id" allow-search :loading="viewsLoading" placeholder="选择现货 View">
                <a-option v-for="view in views" :key="view.view_id" :value="view.view_id"
                  >{{ view.name || view.view_id }}（{{ view.view_id }} · {{ view.freq || "无周期" }}）</a-option
                >
              </a-select>
            </a-form-item>
            <a-form-item label="区间（UTC，左闭右开）" required>
              <a-range-picker v-model="form.range" show-time value-format="YYYY-MM-DDTHH:mm:ss[Z]" style="width: 100%" />
            </a-form-item>
            <a-form-item label="单边手续费（bps）"
              ><a-input-number v-model="form.fee_bps" :min="0" :max="1000" :step="1"
            /></a-form-item>
            <a-button type="primary" long :loading="starting" @click="start">开始回放</a-button>
          </a-form>
          <div class="section-title list-title">回放记录</div>
          <a-list :data="replays" :loading="listLoading" :bordered="false" size="small">
            <template #item="{ item }">
              <a-list-item
                :class="{ active: item.replay_id === selectedId }"
                role="button"
                tabindex="0"
                :aria-pressed="item.replay_id === selectedId"
                @click="select(item.replay_id)"
                @keydown.enter.prevent="select(item.replay_id)"
                @keydown.space.prevent="select(item.replay_id)"
              >
                <div class="replay-row">
                  <div>
                    <a-tag size="small" :color="replayStatusColor(item.status)">{{ replayStatusLabel(item.status) }}</a-tag>
                    <span class="mono">{{ item.view_id }}</span>
                  </div>
                  <div class="muted">{{ formatUtcTime(item.start_time) }} → {{ formatUtcTime(item.end_time) }}</div>
                  <div v-if="item.status === 'running' && item.progress_time" class="muted">
                    进度 {{ formatUtcTime(item.progress_time) }}
                  </div>
                </div>
              </a-list-item>
            </template>
          </a-list>
        </a-grid-item>
        <a-grid-item :span="{ xs: 1, md: 2 }">
          <a-empty v-if="!selected" description="选择或发起一个回放" />
          <template v-else>
            <div class="detail-head">
              <div>
                <a-tag :color="replayStatusColor(selected.status)">{{ replayStatusLabel(selected.status) }}</a-tag>
                <span class="mono">{{ selected.replay_id }}</span>
              </div>
              <a-space>
                <a-popconfirm
                  v-if="selected.status === 'pending' || selected.status === 'running'"
                  content="回放不支持续跑，取消后只保留已写入的周期记录。确定取消？"
                  ok-text="取消回放"
                  cancel-text="继续运行"
                  @ok="cancel"
                >
                  <a-button status="warning" :loading="cancelling">取消回放</a-button>
                </a-popconfirm>
              </a-space>
            </div>
            <a-alert v-if="selected.status === 'failed'" type="error" show-icon class="top-alert"
              >回放失败：{{
                selected.error === "interrupted" ? "进程重启中断，已写入的周期保留，可重新发起" : selected.error
              }}</a-alert
            >
            <div v-if="metrics" class="metrics">
              <div class="metric">
                <span>累计收益</span
                ><strong :class="metrics.total_return >= 0 ? 'up' : 'down'">{{ pct(metrics.total_return) }}</strong>
              </div>
              <div class="metric">
                <span>年化</span
                ><strong :title="metrics.annualized_return === null ? '区间不足一周不计算年化' : undefined">{{
                  pct(metrics.annualized_return)
                }}</strong>
              </div>
              <div class="metric">
                <span>最大回撤</span><strong class="down">{{ pct(metrics.max_drawdown) }}</strong>
              </div>
              <div class="metric">
                <span>平均换手</span><strong>{{ pct(metrics.average_turnover) }}</strong>
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
                <span>目标未达成期数</span><strong>{{ metrics.unfilled_bars }}</strong>
              </div>
            </div>
            <div class="chart-wrap">
              <div ref="chartContainer" class="chart" />
              <a-empty v-if="!bars.length" description="暂无周期记录" />
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
              </a-collapse-item>
            </a-collapse>
            <a-table
              row-key="bar_end_time"
              size="small"
              :data="pagedBars"
              :pagination="barPagination"
              :scroll="{ x: 860 }"
              @page-change="barPage = $event"
            >
              <template #columns>
                <a-table-column title="K 线结束（UTC）" :width="170"
                  ><template #cell="{ record }">{{ formatUtcTime(record.bar_end_time) }}</template></a-table-column
                >
                <a-table-column title="状态" :width="150"
                  ><template #cell="{ record }"
                    ><a-tag size="small" :color="record.status === 'ok' ? 'green' : 'orange'">{{
                      record.status === "ok" ? "ok" : skipReasonLabel(barSkipReason(record.summary_json))
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
                <a-table-column title="换手" :width="90"
                  ><template #cell="{ record }">{{ pct(record.turnover) }}</template></a-table-column
                >
                <a-table-column title="持仓（冻结）" :width="110"
                  ><template #cell="{ record }"
                    >{{ positionsSummary(record.positions_json).holdings }}（{{
                      positionsSummary(record.positions_json).frozen
                    }}）</template
                  ></a-table-column
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
import type { InstrumentTarget, Replay, ReplayBar } from "@/api/strategy-types";
import { listViews } from "@/api/storage/metadata";
import type { View } from "@/api/storage/types";
import { useSpaceStore } from "@/store/modules/space";
import { useStrategyStore } from "@/store/modules/strategy";
import { percent, skipReasonLabel } from "@/views/strategy/model";
import {
  barSkipReason,
  equitySeries,
  formatUtcTime,
  mergeBars,
  parseMetrics,
  positionsSummary,
  replayStatusColor,
  replayStatusLabel
} from "@/views/strategy/replay/model";

defineOptions({ name: "StrategyReplay" });
const route = useRoute();
const spaceStore = useSpaceStore();
const strategyStore = useStrategyStore();
const replays = ref<Replay[]>([]);
const views = ref<View[]>([]);
const bars = ref<ReplayBar[]>([]);
const selectedId = ref("");
const selected = ref<Replay | null>(null);
const listLoading = ref(false);
const viewsLoading = ref(false);
const starting = ref(false);
const cancelling = ref(false);
const error = ref("");
const barPage = ref(1);
const chartContainer = ref<HTMLElement>();
const form = reactive<{ strategy_id: string; view_id: string; range: string[]; fee_bps: number }>({
  strategy_id: String(route.query.strategy_id || ""),
  view_id: String(route.query.view_id || ""),
  range: defaultRange(),
  fee_bps: 10
});
let chart: VChart | null = null;
let poll: ReturnType<typeof setInterval> | null = null;
let polling = false;
// generation 随空间切换递增：旧空间发出的请求返回后一律丢弃；selectRequest 让被新选择取代的详情请求作废。
let generation = 0;
let selectRequest = 0;
const barPageSize = 500;

const metrics = computed(() => parseMetrics(selected.value?.metrics_json));
const pagedBars = computed(() => bars.value.slice((barPage.value - 1) * 50, barPage.value * 50));
const barPagination = computed(() => ({ current: barPage.value, pageSize: 50, total: bars.value.length }));

function defaultRange() {
  const end = new Date();
  end.setUTCMinutes(0, 0, 0);
  const start = new Date(end.getTime() - 30 * 24 * 3600 * 1000);
  const format = (value: Date) => value.toISOString().replace(/\.\d{3}Z$/, "Z");
  return [format(start), format(end)];
}

function pct(value: number | null) {
  return value !== null && Number.isFinite(value) ? `${(value * 100).toFixed(2)}%` : "-";
}

async function loadReplays() {
  const gen = generation;
  listLoading.value = true;
  error.value = "";
  try {
    const items = (await listReplays({ page: 1, page_size: 50 })).items;
    if (gen !== generation) return;
    replays.value = items;
    if (!selectedId.value && items.length) await select(items[0].replay_id);
  } catch (err) {
    if (gen === generation) error.value = err instanceof Error ? err.message : "回放列表加载失败";
  } finally {
    if (gen === generation) listLoading.value = false;
  }
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
  } catch (err) {
    if (gen === generation) error.value = err instanceof Error ? err.message : "View 列表加载失败";
  } finally {
    if (gen === generation) viewsLoading.value = false;
  }
}

/** 从已加载的周期之后增量读取；isCurrent 为假表示请求已被新选择或空间切换取代，返回 null。 */
async function fetchBars(replayId: string, known: ReplayBar[], isCurrent: () => boolean): Promise<ReplayBar[] | null> {
  let merged = known;
  for (let page = Math.floor(known.length / barPageSize) + 1; ; page += 1) {
    const next = await listReplayBars(replayId, { page, page_size: barPageSize });
    if (!isCurrent()) return null;
    merged = mergeBars(merged, next.items);
    if (next.items.length < barPageSize || merged.length >= next.page.total) return merged;
  }
}

async function select(replayId: string) {
  const requestId = ++selectRequest;
  const gen = generation;
  const isCurrent = () => requestId === selectRequest && gen === generation;
  selectedId.value = replayId;
  barPage.value = 1;
  try {
    const replay = await getReplay(replayId);
    if (!isCurrent()) return;
    const all = await fetchBars(replayId, [], isCurrent);
    if (all === null) return;
    selected.value = replay;
    bars.value = all;
    await renderChart(true);
  } catch (err) {
    if (isCurrent()) error.value = err instanceof Error ? err.message : "回放详情加载失败";
  }
}

/** 轮询只刷新排队或运行中的所选回放：周期记录增量追加，图表就地更新数据。 */
async function refreshSelected() {
  const current = selected.value;
  if (!current || (current.status !== "pending" && current.status !== "running")) return;
  const requestId = selectRequest;
  const gen = generation;
  const isCurrent = () => requestId === selectRequest && gen === generation;
  const replay = await getReplay(current.replay_id);
  if (!isCurrent()) return;
  const all = await fetchBars(current.replay_id, bars.value, isCurrent);
  if (all === null) return;
  selected.value = replay;
  if (all.length !== bars.value.length) {
    bars.value = all;
    await renderChart(false);
  }
}

async function renderChart(rebuild: boolean) {
  await nextTick();
  const values = equitySeries(bars.value);
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
        { orient: "bottom", type: "time" },
        { orient: "left", type: "linear", zero: false }
      ],
      line: { style: { lineWidth: 2 } },
      point: { visible: false }
    },
    { dom: chartContainer.value }
  );
  chart.renderSync();
}

async function start() {
  error.value = "";
  if (!form.strategy_id || !form.view_id || form.range.length !== 2) {
    error.value = "请选择定义、View 与区间";
    return;
  }
  starting.value = true;
  try {
    const replay = await startReplay({
      strategy_id: form.strategy_id,
      view_id: form.view_id,
      start_time: form.range[0],
      end_time: form.range[1],
      fee_bps: Number(form.fee_bps) || 0
    });
    Message.success("回放已排队");
    await loadReplays();
    await select(replay.replay_id);
  } catch (err) {
    error.value = err instanceof Error ? err.message : "回放发起失败";
  } finally {
    starting.value = false;
  }
}

async function cancel() {
  if (!selected.value) return;
  const gen = generation;
  cancelling.value = true;
  try {
    const cancelled = await cancelReplay(selected.value.replay_id);
    if (gen !== generation) return;
    if (selected.value?.replay_id === cancelled.replay_id) selected.value = cancelled;
    await loadReplays();
  } catch (err) {
    Message.error(err instanceof Error ? err.message : "取消失败");
  } finally {
    cancelling.value = false;
  }
}

function startPolling() {
  poll = setInterval(async () => {
    if (polling) return;
    if (typeof document !== "undefined" && document.visibilityState !== "visible") return;
    if (!replays.value.some(item => item.status === "pending" || item.status === "running")) return;
    polling = true;
    const gen = generation;
    try {
      const items = (await listReplays({ page: 1, page_size: 50 })).items;
      if (gen !== generation) return;
      replays.value = items;
      await refreshSelected();
    } catch {
      // 轮询失败等下一轮；页面上的数据保持不变。
    } finally {
      polling = false;
    }
  }, 5000);
}

onMounted(() => {
  strategyStore.loadAllStrategies(200).catch(() => undefined);
  loadViews();
  loadReplays();
  startPolling();
});
watch(
  () => spaceStore.selectedSpaceId,
  () => {
    generation += 1;
    selectRequest += 1;
    selectedId.value = "";
    selected.value = null;
    bars.value = [];
    views.value = [];
    form.view_id = "";
    listLoading.value = false;
    viewsLoading.value = false;
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
.section-title {
  margin-bottom: 8px;
  font-weight: 600;
}
.list-title {
  margin-top: 20px;
}
.replay-row {
  display: grid;
  gap: 2px;
  cursor: pointer;
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
