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
              <a-list-item :class="{ active: item.replay_id === selectedId }" @click="select(item.replay_id)">
                <div class="replay-row">
                  <div>
                    <a-tag size="small" :color="replayStatusColor(item.status)">{{ replayStatusLabel(item.status) }}</a-tag>
                    <span class="mono">{{ item.view_id }}</span>
                  </div>
                  <div class="muted">{{ formatStrategyTime(item.start_time) }} → {{ formatStrategyTime(item.end_time) }}</div>
                  <div v-if="item.status === 'running' && item.progress_time" class="muted">
                    进度 {{ formatStrategyTime(item.progress_time) }}
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
                <a-button
                  v-if="selected.status === 'pending' || selected.status === 'running'"
                  status="warning"
                  :loading="cancelling"
                  @click="cancel"
                  >取消回放</a-button
                >
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
                <span>年化</span><strong>{{ pct(metrics.annualized_return) }}</strong>
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
                <a-table-column title="K 线结束" :width="170"
                  ><template #cell="{ record }">{{ formatStrategyTime(record.bar_end_time) }}</template></a-table-column
                >
                <a-table-column title="状态" :width="80"
                  ><template #cell="{ record }"
                    ><a-tag size="small" :color="record.status === 'ok' ? 'green' : 'orange'">{{
                      record.status
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
import { formatStrategyTime, percent, skipReasonLabel } from "@/views/strategy/model";
import {
  equitySeries,
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
let selectRequest = 0;

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

function pct(value: number) {
  return Number.isFinite(value) ? `${(value * 100).toFixed(2)}%` : "-";
}

async function loadReplays() {
  listLoading.value = true;
  error.value = "";
  try {
    replays.value = (await listReplays({ page: 1, page_size: 50 })).items;
    if (!selectedId.value && replays.value.length) await select(replays.value[0].replay_id);
  } catch (err) {
    error.value = err instanceof Error ? err.message : "回放列表加载失败";
  } finally {
    listLoading.value = false;
  }
}

async function loadViews() {
  const spaceId = spaceStore.selectedSpaceId;
  if (!spaceId) return;
  viewsLoading.value = true;
  try {
    const items: View[] = [];
    for (let page = 1; ; page += 1) {
      const rsp = await listViews({ space_id: spaceId, status: "active", page: { page, size: 200 } });
      items.push(...(rsp.views || []));
      if (!rsp.page_result?.has_more || !(rsp.views || []).length) break;
    }
    views.value = items;
  } finally {
    viewsLoading.value = false;
  }
}

async function select(replayId: string) {
  const requestId = ++selectRequest;
  selectedId.value = replayId;
  barPage.value = 1;
  try {
    const [replay, barPage1] = await Promise.all([getReplay(replayId), listReplayBars(replayId, { page: 1, page_size: 500 })]);
    if (requestId !== selectRequest) return;
    const all = [...barPage1.items];
    for (let page = 2; all.length < barPage1.page.total; page += 1) {
      const next = await listReplayBars(replayId, { page, page_size: 500 });
      if (requestId !== selectRequest) return;
      if (!next.items.length) break;
      all.push(...next.items);
    }
    selected.value = replay;
    bars.value = all;
    await renderChart();
  } catch (err) {
    if (requestId === selectRequest) error.value = err instanceof Error ? err.message : "回放详情加载失败";
  }
}

async function renderChart() {
  await nextTick();
  chart?.release();
  chart = null;
  if (!chartContainer.value || !bars.value.length) return;
  chart = new VChart(
    {
      type: "line",
      data: [{ id: "equity", values: equitySeries(bars.value) }],
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
  cancelling.value = true;
  try {
    selected.value = await cancelReplay(selected.value.replay_id);
    await loadReplays();
  } catch (err) {
    Message.error(err instanceof Error ? err.message : "取消失败");
  } finally {
    cancelling.value = false;
  }
}

function startPolling() {
  poll = setInterval(async () => {
    if (typeof document !== "undefined" && document.visibilityState !== "visible") return;
    const active = replays.value.some(item => item.status === "pending" || item.status === "running");
    if (!active) return;
    replays.value = (await listReplays({ page: 1, page_size: 50 }).catch(() => ({ items: replays.value }))).items;
    if (selectedId.value) await select(selectedId.value);
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
    selectedId.value = "";
    selected.value = null;
    bars.value = [];
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
