<template>
  <div class="trial">
    <div class="trial-head">
      <strong>试算</strong>
      <span class="muted">在选定计算任务的结果 View 的最近一期数据上求值一次（混合市场读两个 View），结果不落库。</span>
    </div>
    <div class="trial-bar">
      <a-select
        v-if="!requirement.declared"
        v-model="singleMarket"
        class="trial-market"
        allow-clear
        placeholder="任务的市场"
        @popup-visible-change="ensureTasks"
      >
        <a-option v-for="item in singleMarkets" :key="item" :value="item">{{ marketLabel(item) }}</a-option>
      </a-select>
      <a-select
        v-for="market in activeMarkets"
        :key="market"
        v-model="selected[market]"
        allow-search
        allow-clear
        :loading="tasksLoading"
        :placeholder="`选择${marketLabel(market)}计算任务`"
        @popup-visible-change="ensureTasks"
      >
        <a-option v-for="task in taskChoices(market)" :key="task.taskId" :value="task.taskId"
          >{{ task.name }}（{{ task.taskId }} · {{ task.freq || "无周期" }}）</a-option
        >
      </a-select>
      <a-button type="primary" :loading="running" :disabled="!source.trim() || !complete" @click="run">{{
        tasksChosen ? "校验并试算" : "仅校验 DSL"
      }}</a-button>
    </div>
    <p class="muted">
      <template v-if="pool">
        策略池要绑定各成员所需市场的并集：{{
          pool.markets.length ? pool.markets.map(marketLabel).join("、") : "成员定义读到后才能确定"
        }}；不选任务时只校验 DSL。
      </template>
      <template v-else>
        策略的市场模式需要绑定：{{
          requirement.declared ? requirement.markets.map(marketLabel).join("、") : "一个计算任务（先选它的市场）"
        }}；不选任务时只校验 DSL。
      </template>
    </p>
    <div v-if="pool?.problems.length" class="muted">
      <div v-for="item in pool.problems" :key="item">{{ item }}</div>
    </div>
    <a-alert v-if="tasksError" type="error" show-icon class="block">计算任务列表加载失败：{{ tasksError }}</a-alert>
    <template v-if="result">
      <p v-if="resultSource !== source" class="trial-outdated">下面的结果对应修改前的 DSL，重新试算看新的结果。</p>
      <a-alert v-if="!result.diagnostics.length && !result.trial" type="success" show-icon class="block">DSL 校验通过</a-alert>
      <a-alert
        v-for="item in result.diagnostics"
        :key="item"
        :type="result.trial || result.resolved_json ? 'info' : 'error'"
        class="block"
        >{{ item }}</a-alert
      >
      <a-tabs v-if="result.resolved_json || result.trial" default-active-key="trial">
        <a-tab-pane v-if="result.trial" key="trial" title="试算结果">
          <p class="block trial-line" :class="{ 'is-skipped': result.trial.status !== 'ok' }">
            {{ result.trial.status === "ok" ? "试算通过" : `本期跳过：${skipReasonLabel(result.trial.skip_reason)}` }}，K 线结束
            {{ formatInstant(result.trial.bar_end_time, displayZone) }}
          </p>
          <p v-if="result.trial.targets.length" class="block trial-line">
            目标：<template v-for="(target, index) in result.trial.targets" :key="targetKey(target.instrument_id, target.market)"
              >{{ index ? " · " : "" }}{{ target.instrument_id }}{{ target.market ? `（${marketLabel(target.market)}）` : "" }}
              {{ percent(target.target_weight) }}</template
            >
          </p>
          <ResultItems :result="result.trial" :items="result.trial_items" />
        </a-tab-pane>
        <a-tab-pane v-if="result.resolved_json" key="binding" title="绑定解析"
          ><ResolvedTable :resolved-json="result.resolved_json" :pool="Boolean(pool)"
        /></a-tab-pane>
      </a-tabs>
    </template>
  </div>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, reactive, ref, watch } from "vue";
import { listFactorTasks } from "@/api/factor";
import type { FactorTaskInfo } from "@/api/factor/types";
import { getStrategy, validateStrategy } from "@/api/strategy";
import type { ValidateStrategyResult } from "@/api/strategy-types";
import ResolvedTable from "@/views/strategy/components/strategy-resolved-table.vue";
import ResultItems from "@/views/strategy/components/strategy-result-items.vue";
import { enabledTasks, isUnifiedTask, type TaskOption } from "@/views/strategy/components/task-options";
import { loadUnifiedDatasetIds } from "@/views/strategy/components/unified-datasets";
import { marketLabel, marketRequirement, poolMembers, poolRequirement, targetKey } from "@/views/strategy/market-mode";
import { percent, skipReasonLabel } from "@/views/strategy/model";
import { formatInstant } from "@/utils/time-zone";
import { useDisplayZone } from "@/hooks/useDisplayZone";

// 页面时间按当前空间登记的时区显示
const displayZone = useDisplayZone();

const props = defineProps<{ source: string; spaceId: string }>();
const singleMarkets = ["spot", "swap", "equity"];
// 策略没有写 market 时只试算一个任务，市场由用户选（不预设）。
const singleMarket = ref<string>();
const selected = reactive<Record<string, string | undefined>>({});
const tasks = ref<TaskOption[]>([]);
const tasksLoading = ref(false);
const running = ref(false);
const result = ref<ValidateStrategyResult | null>(null);
// 结果对应的 DSL：DSL 改了以后旧结果保留，但标明对应修改前的 DSL。
const resultSource = ref("");
const tasksError = ref("");
let loadedSpace = "";
// 切换空间时两个计数都递增，让旧空间的任务列表与试算结果作废。
let tasksRequest = 0;
let runRequest = 0;

// 策略池：要绑定的市场是成员所需市场的并集，成员定义按需读取（null 表示不存在或读取失败）。
const memberDefinitions = reactive<Record<string, string | null>>({});
const pool = computed(() => poolRequirement(props.source, id => (id in memberDefinitions ? memberDefinitions[id] : undefined)));
const requirement = computed(() =>
  pool.value ? { declared: true, markets: pool.value.markets } : marketRequirement(props.source)
);
let memberRequest = 0;
let memberTimer: ReturnType<typeof setTimeout> | undefined;
// 编辑 DSL 时每次按键都会变，停下 400 毫秒后再读新出现的成员，避免把输入到一半的 ID 都读一遍。
function scheduleMemberLoad() {
  clearTimeout(memberTimer);
  memberTimer = setTimeout(() => void loadMissingMembers(), 400);
}
async function loadMissingMembers() {
  const requestId = memberRequest;
  const missing = (poolMembers(props.source) ?? [])
    .map(member => member.strategyId)
    .filter(id => id && !(id in memberDefinitions));
  await Promise.all(
    [...new Set(missing)].map(async id => {
      let definition: string | null = null;
      try {
        definition = (await getStrategy(id)).dsl_yaml || null;
      } catch {
        // 读取失败按“不存在或读取失败”提示
      }
      if (requestId === memberRequest) memberDefinitions[id] = definition;
    })
  );
}
watch(() => props.source, scheduleMemberLoad, { immediate: true });
onBeforeUnmount(() => clearTimeout(memberTimer));
const activeMarkets = computed(() =>
  requirement.value.declared ? requirement.value.markets : singleMarket.value ? [singleMarket.value] : []
);
const tasksChosen = computed(() => activeMarkets.value.some(market => selected[market]));
// 选了任务就要把需要的市场都选全；一个都不选只校验 DSL。
const complete = computed(() => !tasksChosen.value || activeMarkets.value.every(market => selected[market]));

// 另一个市场已选的任务不再出现在这个市场的选项里：一个任务只能属于一个市场。
function taskChoices(market: string) {
  const taken = new Set(activeMarkets.value.filter(item => item !== market).map(item => selected[item]));
  return tasks.value.filter(task => !taken.has(task.taskId));
}

async function ensureTasks(visible = true) {
  const spaceId = props.spaceId;
  if (!visible || !spaceId || loadedSpace === spaceId) return;
  const requestId = ++tasksRequest;
  tasksLoading.value = true;
  try {
    const infos: FactorTaskInfo[] = [];
    for (let page = 1; ; page += 1) {
      const rsp = await listFactorTasks({ page: { page, size: 200 } });
      if (requestId !== tasksRequest) return;
      infos.push(...(rsp.factor_tasks || []));
      if (!rsp.page_result?.has_more || !(rsp.factor_tasks || []).length) break;
    }
    // 统一数据集上的任务要登记为 multi，试算面板按市场选任务，不列它们。
    const unifiedIds = await loadUnifiedDatasetIds(spaceId);
    if (requestId !== tasksRequest) return;
    tasks.value = enabledTasks(infos, spaceId).filter(task => !isUnifiedTask(task, unifiedIds));
    loadedSpace = spaceId;
    tasksError.value = "";
  } catch (err) {
    if (requestId === tasksRequest) tasksError.value = err instanceof Error ? err.message : "计算任务列表加载失败";
  } finally {
    if (requestId === tasksRequest) tasksLoading.value = false;
  }
}

async function run() {
  const requestId = ++runRequest;
  running.value = true;
  try {
    const factorTasks = Object.fromEntries(
      activeMarkets.value.flatMap(market => (selected[market] ? [[market, selected[market] as string]] : []))
    );
    const source = props.source;
    const next = await validateStrategy(source, factorTasks);
    if (requestId === runRequest) {
      result.value = next;
      resultSource.value = source;
    }
  } catch (err) {
    if (requestId === runRequest) resultSource.value = props.source;
    if (requestId === runRequest)
      result.value = {
        diagnostics: [err instanceof Error ? err.message : "试算失败"],
        resolved_json: "",
        trial: null,
        trial_items: []
      };
  } finally {
    if (requestId === runRequest) running.value = false;
  }
}

// 所选任务一变，在途的试算作废、已有结果清除：不能在当前任务下展示别的任务的结果。
watch(
  () => [singleMarket.value, JSON.stringify(selected)],
  () => {
    runRequest += 1;
    result.value = null;
    running.value = false;
  },
  { flush: "sync" }
);
// DSL 一变，在途的试算作废；已有结果保留，并标明对应修改前的 DSL。
watch(
  () => props.source,
  () => {
    runRequest += 1;
    running.value = false;
  },
  { flush: "sync" }
);

watch(
  () => props.spaceId,
  () => {
    tasksRequest += 1;
    runRequest += 1;
    loadedSpace = "";
    tasks.value = [];
    // 成员定义属于原来的空间，换空间后重读
    memberRequest += 1;
    for (const key of Object.keys(memberDefinitions)) delete memberDefinitions[key];
    scheduleMemberLoad();
    singleMarket.value = undefined;
    for (const key of Object.keys(selected)) delete selected[key];
    result.value = null;
    tasksLoading.value = false;
    tasksError.value = "";
    running.value = false;
  }
);
</script>

<style scoped>
.trial-line {
  margin: 0 0 6px;
  font-size: 13px;
}
.trial-line.is-skipped {
  color: rgb(var(--warning-6));
}
.trial-outdated {
  margin: 0 0 8px;
  color: rgb(var(--warning-6));
  font-size: 13px;
}
.trial {
  display: grid;
  gap: 10px;
  margin-top: 16px;
}
.trial-head {
  display: flex;
  align-items: baseline;
  gap: 10px;
}
.trial-bar {
  display: flex;
  gap: 8px;
}
.trial-bar .arco-select {
  flex: 1;
}
.trial-bar .trial-market {
  flex: 0 0 120px;
}
.muted {
  color: var(--color-text-3);
  font-size: 12px;
}
.block {
  margin-bottom: 8px;
}
@media (max-width: 640px) {
  .trial-bar {
    flex-direction: column;
  }
}
</style>
