<template>
  <div class="moox-page">
    <div class="moox-inner runtime-status">
      <a-alert v-if="!selectedSpaceId" type="warning">请先在顶部选择空间。</a-alert>
      <template v-else>
        <section class="status-card" :class="`status-card--${overall.level}`">
          <div class="status-head">
            <div class="status-verdict">
              <a-tag :color="overall.info.color" size="medium" class="verdict-tag">
                <template #icon><component :is="overall.info.icon" /></template>
                {{ overall.info.text }}
              </a-tag>
              <span v-if="!overall.reasons.length" class="muted">采集计划已生效，各数据集的自动检查都跟得上。</span>
              <span v-else class="verdict-reason">{{ overall.reasons[0] }}</span>
              <a-tooltip v-if="overall.reasons.length > 1">
                <template #content>
                  <div v-for="reason in overall.reasons" :key="reason">{{ reason }}</div>
                </template>
                <span class="more-reasons">等 {{ overall.reasons.length }} 项</span>
              </a-tooltip>
            </div>
            <a-space>
              <span class="muted">{{ status.generated_at ? `更新于 ${localTimeText(status.generated_at)}` : "" }}</span>
              <a-button size="small" @click="logVisible = true">
                <template #icon><icon-history /></template>
                变更记录
              </a-button>
              <a-button size="small" :loading="statusLoading" aria-label="刷新" @click="refreshAll">
                <template #icon><icon-refresh /></template>
              </a-button>
            </a-space>
          </div>
          <div class="metric-grid">
            <div class="metric">
              <span class="metric-label">采集计划</span>
              <b class="metric-value metric-value--text">{{ planText }}</b>
              <span class="metric-sub">{{ planSub }}</span>
            </div>
            <div class="metric">
              <span class="metric-label">函数</span>
              <b class="metric-value">{{ summary.functions }}</b>
              <span class="metric-sub">下发中 {{ summary.pendingFunctions }} · 退役中 {{ summary.retiringFunctions }}</span>
            </div>
            <div class="metric">
              <span class="metric-label">数据集 / 标的</span>
              <b class="metric-value">{{ summary.datasets }} / {{ summary.subjects }}</b>
              <span class="metric-sub">正在采集</span>
            </div>
            <div class="metric">
              <span class="metric-label">近 24h 实时缺口</span>
              <b class="metric-value" :class="{ 'metric-value--warn': summary.gapBars > 0 }">{{ summary.gapBars }} 根</b>
              <span class="metric-sub">{{ summary.gapSubjects }} 个标的，由自动补采修复</span>
            </div>
            <div class="metric">
              <span class="metric-label">补采</span>
              <b class="metric-value">{{ status.active_backfills || 0 }}</b>
              <span class="metric-sub" :class="{ 'danger-text': (status.failed_backfills_24h || 0) > 0 }"
                >进行中 · 24h 失败 {{ status.failed_backfills_24h || 0 }}</span
              >
            </div>
          </div>
        </section>

        <section>
          <header class="block-header">
            <h3>数据集健康</h3>
            <span class="muted">异常排前，点击行展开</span>
          </header>
          <a-table
            v-model:expanded-keys="expandedKeys"
            row-key="key"
            size="small"
            :data="datasetRows"
            :loading="statusLoading"
            :pagination="false"
            :bordered="{ cell: true }"
            :expandable="{ width: 36 }"
            table-layout-fixed
            class="clickable-table"
            @row-click="toggleDataset"
          >
            <template #empty><div class="empty-text">还没有正在采集的数据集</div></template>
            <template #columns>
              <a-table-column title="数据集 / 频率">
                <template #cell="{ record }">
                  <div class="dataset-cell">
                    <span class="mono-text dataset-name" :title="record.dataset_id">{{ record.dataset_id }}</span>
                    <a-tag size="small" class="freq-tag">{{ record.frequency }}</a-tag>
                  </div>
                </template>
              </a-table-column>
              <a-table-column title="标的" data-index="subject_count" :width="64" align="right" />
              <a-table-column title="函数" data-index="function_count" :width="64" align="right" />
              <a-table-column title="检查滞后" :width="110">
                <template #cell="{ record }">
                  <span :style="{ color: lagColor(record.lag_seconds) }">{{ durationText(record.lag_seconds) }}</span>
                </template>
              </a-table-column>
              <a-table-column title="24h 实时缺口" :width="130">
                <template #cell="{ record }">
                  <span v-if="record.gap_bars_24h">{{ record.gap_bars_24h }} 根 / {{ record.gap_subjects_24h }} 标的</span>
                  <span v-else class="muted">0</span>
                </template>
              </a-table-column>
              <a-table-column title="失败分块" :width="90" align="right">
                <template #cell="{ record }">
                  <span :class="record.failed_chunks_24h ? 'danger-text' : 'muted'">{{ record.failed_chunks_24h || 0 }}</span>
                </template>
              </a-table-column>
              <a-table-column title="历史缺口" :width="90" align="right">
                <template #cell="{ record }">
                  <span v-if="record.history_error" class="warn-text">扫描失败</span>
                  <span v-else-if="record.history_gap_bars" class="warn-text">{{ record.history_gap_bars }} 根</span>
                  <span v-else class="muted">{{ record.history_scanned_at ? 0 : "-" }}</span>
                </template>
              </a-table-column>
              <a-table-column title="状态" :width="76" align="center">
                <template #cell="{ record }">
                  <a-tag size="small" :color="levelInfo(record.level).color">{{ levelInfo(record.level).text }}</a-tag>
                </template>
              </a-table-column>
            </template>
            <template #expand-row="{ record }">
              <div class="dataset-detail">
                <div
                  v-for="reason in record.reasons || []"
                  :key="reason"
                  :class="record.level === 'error' ? 'danger-text' : 'warn-text'"
                >
                  <icon-exclamation-circle /> {{ reason }}
                </div>
                <div class="detail-line">
                  首次生效 {{ localTimeText(record.effective_from) }} · 自动检查水位
                  {{ record.scanned_until ? localTimeText(record.scanned_until) : "尚未扫描" }}
                </div>
                <div class="detail-line">
                  历史缺口扫描：
                  <template v-if="!record.history_scanned_at">尚未扫描</template>
                  <template v-else-if="record.history_error"
                    >{{ localTimeText(record.history_scanned_at) }} 失败（{{ record.history_error }}）</template
                  >
                  <template v-else-if="record.history_gap_bars">
                    {{ localTimeText(record.history_scanned_at) }} 发现 {{ record.history_gap_bars }} 根缺口（{{
                      record.history_gap_subjects
                    }}
                    个标的），补采确认后行情源本来没有的部分会自动排除
                  </template>
                  <template v-else>{{ localTimeText(record.history_scanned_at) }} 无缺口</template>
                </div>
                <div v-if="record.absent_bars" class="detail-line">
                  已确认行情源没有 {{ record.absent_bars }} 根（如停牌、无成交），不算缺口、不影响健康
                </div>
                <div class="detail-line">
                  服务函数（{{ record.function_count || 0 }}）：
                  <span class="mono-text">{{ datasetFunctionText(record) }}</span>
                  <a-link v-if="(record.function_count || 0) > 0" class="inline-link" @click.stop="showFunctionsOf(record)"
                    >在函数列表中查看</a-link
                  >
                </div>
                <a-space class="detail-actions" wrap>
                  <a-button size="mini" @click.stop="openRange('check', record.dataset_id)">检查缺口</a-button>
                  <a-button size="mini" @click.stop="openRange('backfill', record.dataset_id)">补采</a-button>
                  <a-popconfirm
                    v-if="record.history_gap_bars"
                    content="按历史扫描发现的缺口补采；补完后行情源本来没有的 K 线会被确认并不再报告。确认补采？"
                    @ok="onConfirmHistory(record)"
                  >
                    <a-button size="mini" type="primary" @click.stop>补采确认</a-button>
                  </a-popconfirm>
                  <a-popconfirm
                    v-if="record.absent_bars"
                    content="清掉该数据集已确认的“行情源缺失”，历史扫描会重新进行；适用于行情源后来补上了数据。确认清除？"
                    @ok="onClearAbsences(record)"
                  >
                    <a-button size="mini" @click.stop>清除并重新检查</a-button>
                  </a-popconfirm>
                </a-space>
              </div>
            </template>
          </a-table>
        </section>

        <section>
          <a-tabs v-model:active-key="activePane" class="pane-tabs" :header-padding="false">
            <a-tab-pane key="functions">
              <template #title>
                函数 <span class="tab-count">{{ summary.functions }}</span>
              </template>
            </a-tab-pane>
            <a-tab-pane key="backfills">
              <template #title>
                补采作业
                <span v-if="status.active_backfills" class="tab-count tab-count--warn">{{ status.active_backfills }}</span>
              </template>
            </a-tab-pane>
          </a-tabs>

          <div v-show="activePane === 'functions'">
            <div class="pane-toolbar">
              <a-input v-model="functionFilters.keyword" placeholder="搜索函数名或数据集" allow-clear style="width: 240px">
                <template #prefix><icon-search /></template>
              </a-input>
              <a-select v-model="functionFilters.region" placeholder="全部地域" allow-clear style="width: 140px">
                <a-option v-for="item in regionOptions" :key="item" :value="item">{{ item }}</a-option>
              </a-select>
              <a-select v-model="functionFilters.market" placeholder="全部市场" allow-clear style="width: 160px">
                <a-option v-for="item in marketOptions" :key="item" :value="item">{{ item }}</a-option>
              </a-select>
              <a-checkbox v-model="functionFilters.abnormalOnly">只看异常</a-checkbox>
              <span class="muted toolbar-count">共 {{ filteredFunctions.length }} 个</span>
            </div>
            <a-table
              row-key="name"
              size="small"
              :data="filteredFunctions"
              :loading="statusLoading"
              :pagination="{ pageSize: 20, showTotal: true, size: 'small' }"
              :bordered="{ cell: true }"
              table-layout-fixed
            >
              <template #empty><div class="empty-text">没有匹配的函数</div></template>
              <template #columns>
                <a-table-column title="函数" :width="270">
                  <template #cell="{ record }"
                    ><span class="mono-text function-name" :title="record.name">{{ record.name }}</span></template
                  >
                </a-table-column>
                <a-table-column title="地域" data-index="region" :width="120" />
                <a-table-column title="市场" data-index="market" :width="150" />
                <a-table-column title="触发" :width="150">
                  <template #cell="{ record }"
                    ><span class="mono-text function-name" :title="record.cron">{{ record.cron }}</span></template
                  >
                </a-table-column>
                <a-table-column title="标的" data-index="subject_count" :width="80" align="right" />
                <a-table-column title="服务的数据集">
                  <template #cell="{ record }">
                    <a-tag v-for="item in record.datasets || []" :key="item" size="small" class="dataset-tag">{{ item }}</a-tag>
                  </template>
                </a-table-column>
                <a-table-column title="状态" :width="90" align="center">
                  <template #cell="{ record }">
                    <a-tag size="small" :color="functionStateInfo(record.state).color">{{
                      functionStateInfo(record.state).text
                    }}</a-tag>
                  </template>
                </a-table-column>
              </template>
            </a-table>
          </div>

          <BackfillJobs
            v-show="activePane === 'backfills'"
            ref="backfillJobs"
            :space-id="selectedSpaceId || ''"
            @check="openRange('check')"
            @backfill="openRange('backfill')"
            @changed="loadStatus"
          />
        </section>
      </template>
    </div>

    <KlineRangeDialog
      v-model:visible="rangeVisible"
      :mode="rangeMode"
      :space-id="selectedSpaceId || ''"
      :dataset-options="datasetOptions"
      :dataset-id="rangeDataset"
      @submitted="onJobSubmitted"
    />

    <a-drawer v-model:visible="logVisible" :width="860" :footer="false" title="标的变更记录" unmount-on-close>
      <ActivityLog :space-id="selectedSpaceId" category="collector.subject_change" :page-size="20" />
    </a-drawer>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref, watch } from "vue";
import { Message } from "@arco-design/web-vue";
import { storeToRefs } from "pinia";
import ActivityLog from "@/components/activity-log/index.vue";
import {
  clearSourceAbsences,
  confirmHistoryGaps,
  getCollectorStatus,
  type CollectorDatasetStatus,
  type GetCollectorStatusResponse
} from "@/api/collector/runtime-status";
import { useSpaceStore } from "@/store/modules/space";
import { createLatestRequestGuard } from "@/utils/latest-request";
import BackfillJobs from "./backfill-jobs.vue";
import KlineRangeDialog from "./kline-range-dialog.vue";
import {
  datasetFunctions,
  datasetKey,
  durationText,
  filterFunctions,
  functionOptions,
  functionStateInfo,
  lagColor,
  levelInfo,
  levelRank,
  localTimeText,
  statusSummary
} from "./runtime-status-model";

const spaceStore = useSpaceStore();
const { selectedSpaceId } = storeToRefs(spaceStore);
const guard = createLatestRequestGuard();

const status = ref<GetCollectorStatusResponse>({});
const statusLoading = ref(false);
const expandedKeys = ref<string[]>([]);
const activePane = ref<"functions" | "backfills">("functions");
const functionFilters = ref({ keyword: "", region: "", market: "", abnormalOnly: false });
const rangeVisible = ref(false);
const rangeMode = ref<"check" | "backfill">("check");
const rangeDataset = ref("");
const logVisible = ref(false);
const backfillJobs = ref<InstanceType<typeof BackfillJobs> | null>(null);

const summary = computed(() => statusSummary(status.value));
const overall = computed(() => ({
  level: status.value.level || "ok",
  info: levelInfo(status.value.level),
  reasons: status.value.reasons || []
}));
const datasetRows = computed(() =>
  [...(status.value.datasets || [])]
    .sort((a, b) => levelRank(b.level) - levelRank(a.level))
    .map(item => ({ ...item, key: datasetKey(item) }))
);
const datasetOptions = computed(() =>
  [...new Set((status.value.datasets || []).map(item => item.dataset_id || "").filter(Boolean))].sort()
);
const filteredFunctions = computed(() => {
  const list = filterFunctions(status.value.functions || [], functionFilters.value);
  // 下发中、退役中排前，其余保持后端顺序。
  const rank = (state?: string) => (state === "pending" ? 2 : state === "retiring" ? 1 : 0);
  return [...list].sort((a, b) => rank(b.state) - rank(a.state));
});
const regionOptions = computed(() => functionOptions(status.value.functions || [], "region"));
const marketOptions = computed(() => functionOptions(status.value.functions || [], "market"));

const planText = computed(() => {
  const { open, applied } = status.value;
  if (open) return `#${open.id} 变更中`;
  if (applied) return `#${applied.id} 已生效`;
  return "尚无计划";
});
const planSub = computed(() => {
  const plan = status.value.open || status.value.applied;
  return plan ? `${localTimeText(plan.updated_at)} 更新` : "创建采集任务后自动生成";
});

function datasetFunctionText(record: CollectorDatasetStatus) {
  const names = datasetFunctions(status.value.functions || [], record);
  if (!names.length) return "-";
  return names.length > 3 ? `${names.slice(0, 3).join("、")} 等` : names.join("、");
}

async function loadStatus() {
  const spaceId = selectedSpaceId.value || "";
  if (!spaceId) return;
  const request = guard.begin();
  statusLoading.value = true;
  try {
    const data = await getCollectorStatus(spaceId);
    if (!request.isLatest() || spaceId !== (selectedSpaceId.value || "")) return;
    status.value = data;
    // 首次加载时展开第一个异常的数据集。
    if (!expandedKeys.value.length) {
      const first = (data.datasets || []).find(item => item.level && item.level !== "ok");
      if (first) expandedKeys.value = [datasetKey(first)];
    }
  } catch (error) {
    if (request.isLatest()) console.error("获取运行状态失败:", error);
  } finally {
    if (request.isLatest()) statusLoading.value = false;
  }
}

function refreshAll() {
  void loadStatus();
  void backfillJobs.value?.reload();
}

function toggleDataset(record: { key: string }) {
  expandedKeys.value = expandedKeys.value.includes(record.key)
    ? expandedKeys.value.filter(key => key !== record.key)
    : [...expandedKeys.value, record.key];
}

function showFunctionsOf(record: CollectorDatasetStatus) {
  functionFilters.value = { keyword: datasetKey(record), region: "", market: "", abnormalOnly: false };
  activePane.value = "functions";
}

function openRange(mode: "check" | "backfill", datasetId = "") {
  rangeMode.value = mode;
  rangeDataset.value = datasetId;
  rangeVisible.value = true;
}

function onJobSubmitted() {
  activePane.value = "backfills";
  backfillJobs.value?.showAttention();
  void loadStatus();
}

async function onConfirmHistory(record: CollectorDatasetStatus) {
  try {
    const result = await confirmHistoryGaps(selectedSpaceId.value || "", record.dataset_id || "", record.frequency || "");
    Message.success(result.job_id ? `已创建补采作业，共 ${result.chunks || 0} 个分块` : "没有待确认的历史缺口");
    if (result.job_id) onJobSubmitted();
  } catch (error) {
    Message.error(error instanceof Error ? error.message : "补采确认失败");
  }
}

async function onClearAbsences(record: CollectorDatasetStatus) {
  try {
    const result = await clearSourceAbsences(selectedSpaceId.value || "", record.dataset_id || "", record.frequency || "");
    Message.success(`已清除 ${Number(result.cleared || 0)} 段；历史扫描会在几分钟内重新进行，实时区间可用“检查缺口”确认`);
    void loadStatus();
  } catch (error) {
    Message.error(error instanceof Error ? error.message : "清除失败");
  }
}

watch(selectedSpaceId, () => {
  guard.invalidate();
  status.value = {};
  expandedKeys.value = [];
  functionFilters.value = { keyword: "", region: "", market: "", abnormalOnly: false };
  void loadStatus();
});
onMounted(loadStatus);
</script>

<style scoped>
.runtime-status {
  display: flex;
  flex-direction: column;
  gap: var(--moox-space-4);
}
.status-card {
  padding: var(--moox-space-3) var(--moox-space-4);
  border: 1px solid var(--color-border-2);
  border-radius: 8px;
  background: var(--color-bg-2);
}
.status-card--warn {
  border-color: rgb(var(--warning-3));
}
.status-card--error {
  border-color: rgb(var(--danger-3));
}
.status-head {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  justify-content: space-between;
  gap: var(--moox-space-2);
}
.status-verdict {
  display: flex;
  min-width: 0;
  flex: 1;
  align-items: center;
  gap: var(--moox-space-2);
}
.verdict-reason {
  overflow: hidden;
  color: var(--color-text-2);
  font-size: 13px;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.more-reasons {
  flex: none;
  color: rgb(var(--arcoblue-6));
  font-size: 12px;
  cursor: default;
}
.metric-grid {
  display: grid;
  grid-template-columns: repeat(5, minmax(0, 1fr));
  gap: var(--moox-space-2);
  margin-top: var(--moox-space-3);
}
@media (max-width: 1100px) {
  .metric-grid {
    grid-template-columns: repeat(auto-fit, minmax(160px, 1fr));
  }
}
.metric {
  display: flex;
  flex-direction: column;
  gap: 2px;
  padding: var(--moox-space-2) var(--moox-space-3);
  border-radius: 6px;
  background: var(--color-fill-1);
}
.metric-label,
.metric-sub {
  color: var(--color-text-3);
  font-size: 12px;
}
.metric-value {
  color: var(--color-text-1);
  font-size: 20px;
  font-weight: 500;
  line-height: 28px;
}
.metric-value--text {
  font-size: 15px;
}
.metric-value--warn {
  color: rgb(var(--warning-6));
}
.block-header {
  display: flex;
  align-items: baseline;
  justify-content: space-between;
  gap: var(--moox-space-2);
  margin-bottom: var(--moox-space-2);
}
.block-header h3 {
  margin: 0;
  font-size: 15px;
}
.clickable-table :deep(.arco-table-tr) {
  cursor: pointer;
}
.dataset-cell {
  display: flex;
  min-width: 0;
  align-items: center;
  gap: var(--moox-space-1);
}
.dataset-name {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  word-break: normal;
}
.freq-tag {
  flex: none;
}
.dataset-detail {
  display: flex;
  flex-direction: column;
  gap: 4px;
  font-size: 12px;
  line-height: 20px;
  cursor: default;
}
.detail-line {
  color: var(--color-text-2);
}
.detail-actions {
  margin-top: var(--moox-space-1);
}
.inline-link {
  margin-left: var(--moox-space-1);
  font-size: 12px;
}
.pane-tabs :deep(.arco-tabs-content) {
  display: none;
}
.tab-count {
  margin-left: 4px;
  padding: 0 6px;
  border-radius: 8px;
  background: var(--color-fill-2);
  color: var(--color-text-2);
  font-size: 12px;
}
.tab-count--warn {
  background: rgb(var(--warning-1));
  color: rgb(var(--warning-6));
}
.pane-toolbar {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: var(--moox-space-2);
  margin: var(--moox-space-2) 0 var(--moox-space-toolbar-table);
}
.toolbar-count {
  margin-left: auto;
}
.function-name {
  display: block;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  word-break: normal;
}
.dataset-tag {
  max-width: 100%;
  margin: 1px 4px 1px 0;
}
.dataset-tag :deep(.arco-tag-content) {
  overflow: hidden;
  text-overflow: ellipsis;
}
.muted {
  color: var(--color-text-3);
  font-size: 12px;
}
.danger-text {
  color: rgb(var(--danger-6));
}
.warn-text {
  color: rgb(var(--warning-6));
}
.mono-text {
  font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace;
  font-size: 12px;
  word-break: break-all;
}
.empty-text {
  padding: var(--moox-space-4) 0;
  color: var(--color-text-3);
  text-align: center;
}
</style>
