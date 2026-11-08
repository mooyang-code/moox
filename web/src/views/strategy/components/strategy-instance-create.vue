<template>
  <a-drawer :visible="visible" :width="'min(760px, 100vw)'" title="新建策略实例" unmount-on-close @cancel="close">
    <a-alert v-if="error" type="error" show-icon class="form-alert">{{ error }}</a-alert>
    <a-form layout="vertical">
      <a-form-item
        label="策略定义"
        required
        :validate-status="fieldErrors.strategy_id ? 'error' : undefined"
        :help="fieldErrors.strategy_id"
      >
        <a-select v-model="form.strategy_id" allow-search placeholder="选择一份 DSL 定义" @change="onStrategyChange">
          <a-option v-for="item in strategies" :key="item.strategy_id" :value="item.strategy_id"
            >{{ item.name }}（{{ item.strategy_id }}）</a-option
          >
        </a-select>
      </a-form-item>
      <a-descriptions v-if="preview" :column="1" bordered size="small" class="preview">
        <a-descriptions-item v-if="preview.pool" label="策略池">{{ describePool(preview.pool) }}</a-descriptions-item>
        <a-descriptions-item v-else label="规则">{{
          preview.rules.map(rule => `${rule.name || rule.id}（${rule.type}）`).join("、")
        }}</a-descriptions-item>
        <a-descriptions-item v-if="!preview.pool" label="标的池">{{ preview.universe }}</a-descriptions-item>
        <a-descriptions-item label="bar 断言">{{ preview.bar }}</a-descriptions-item>
      </a-descriptions>
      <a-alert v-if="poolRequirement?.problems.length" type="warning" show-icon class="form-alert"
        ><div>策略池的成员还不能用，暂不能创建：</div>
        <div v-for="item in poolRequirement.problems" :key="item">{{ item }}</div></a-alert
      >
      <a-form-item
        v-if="!requirement.declared"
        label="市场"
        :validate-status="!activeMarkets.length && fieldErrors.factor_tasks ? 'error' : undefined"
        :help="!activeMarkets.length ? fieldErrors.factor_tasks : undefined"
      >
        <a-radio-group v-model="form.single_market" type="button" @change="onMarketChange">
          <a-radio v-for="item in singleMarkets" :key="item" :value="item">{{ marketLabel(item) }}</a-radio>
        </a-radio-group>
        <template #extra
          >策略没有写
          market，只绑一个因子任务：先选任务的数据所属的市场（必须与任务源数据集的市场一致，创建前的预检会核对）。</template
        >
      </a-form-item>
      <p v-else-if="activeMarkets.length && !unifiedMode" class="meta-hint mode-hint">
        {{ poolRequirement ? "策略池要绑定各成员所需市场的并集：" : "策略的市场模式需要绑定："
        }}{{ activeMarkets.map(marketLabel).join("、") }} 的因子任务{{
          activeMarkets.length > 1 ? "（现货、永续各一个，两个任务的周期、日历和用到的列要一致）" : ""
        }}。
      </p>
      <a-form-item v-if="canUnified" label="输入方式">
        <a-radio-group v-model="form.input_mode" type="button" @change="onInputModeChange">
          <a-radio value="split">{{ activeMarkets.length > 1 ? "现货、永续各一个因子任务" : "市场自己的因子任务" }}</a-radio>
          <a-radio value="unified">统一数据集上的一个因子任务</a-radio>
        </a-radio-group>
        <template #extra
          >统一数据集把现货、永续放在同一个数据集里（用 series_tag 区分），一个因子任务、一个 View
          同时覆盖两个市场，策略到达即求值；
          分开时现货、永续各有自己的数据集与因子任务，策略等两边都到齐再求值。统一数据集上的任务暂不支持
          factor_params（带可变成员的任务在这种方式下不能选），也不能进策略池。</template
        >
      </a-form-item>
      <a-form-item
        v-for="market in taskKeys"
        :key="market"
        :label="`${marketLabel(market)}因子任务`"
        required
        :validate-status="fieldErrors.factor_tasks ? 'error' : undefined"
        :help="market === taskKeys[0] ? fieldErrors.factor_tasks : undefined"
      >
        <a-select
          v-model="form.tasks[market]"
          allow-search
          allow-clear
          :loading="tasksLoading"
          :placeholder="
            market === UNIFIED_TASK_KEY
              ? '选择一个统一数据集上运行中、还没有关联实例的因子任务'
              : '选择一个运行中、还没有关联实例的因子任务'
          "
          @change="onTaskChange"
        >
          <a-option
            v-for="task in taskChoices(market)"
            :key="task.taskId"
            :value="task.taskId"
            :disabled="Boolean(varBlockReason && task.varMembers.length)"
          >
            {{ task.name }}（{{ task.taskId }} · {{ task.freq || "无周期"
            }}{{ varBlockReason && task.varMembers.length ? " · 有可变成员，不可选" : "" }}）
          </a-option>
        </a-select>
      </a-form-item>
      <p v-if="tasksError" class="meta-hint meta-error">
        因子任务列表读取失败：{{ tasksError }}<a-link @click="loadTasksList">重试</a-link>
      </p>
      <p v-if="datasetsError" class="meta-hint meta-error">
        数据集信息读取失败（{{ datasetsError }}），暂时分不出哪些任务在统一数据集上；<a-link @click="loadDatasetsList"
          >重试</a-link
        >
      </p>
      <p v-else-if="unifiedMode && !tasksLoading && !taskChoices(UNIFIED_TASK_KEY).length" class="meta-hint">
        当前空间没有可用的统一数据集因子任务：任务的源数据集要是统一数据集（market_type 为 multi），任务运行中且还没有关联实例。
      </p>
      <p v-else-if="!tasksLoading && !tasks.length" class="meta-hint">
        当前空间没有可用的因子任务：任务必须处于运行中，且还没有关联策略实例。请先到「因子」模块创建并启用任务。
      </p>
      <p v-else class="meta-hint">一个因子任务只能被一个实例独占使用，关联创建后不能修改，也不能删除实例。</p>
      <p v-if="varBlockReason && hasVarMemberTasks" class="meta-hint">标为“有可变成员，不可选”的任务：{{ varBlockReason }}。</p>
      <div v-for="item in selectedTasks" :key="item.task.taskId" class="task-columns">
        <div class="task-columns__head">
          <strong
            >{{ marketLabel(item.market) }}任务的列（DSL 里直接写这些列名{{
              item.market === UNIFIED_TASK_KEY ? "，现货、永续共用" : ""
            }}）</strong
          >
          <a-button size="mini" @click="copyColumns(item.task)">复制全部列名</a-button>
        </div>
        <a-space wrap size="mini">
          <a-tag v-for="column in item.task.columns" :key="column" size="small">{{ column }}</a-tag>
          <span v-if="!item.task.columns.length" class="meta-hint">这个任务还没有成员，先到「因子」模块给任务添加因子。</span>
        </a-space>
        <p v-if="item.task.varMembers.length && varBlockReason" class="meta-hint meta-error">
          可变成员 {{ item.task.varMembers.join("、") }}：{{ varBlockReason }}，请换一个没有可变成员的任务。
        </p>
        <p v-else-if="item.task.varMembers.length" class="meta-hint">
          可变成员 {{ item.task.varMembers.join("、") }}：DSL 的 factor_params 要为每个可变成员给出参数（策略逐期提交），可用
          params("成员键", "参数名") 读取当期实际参数。
        </p>
      </div>
      <a-form-item label="运行模式">
        <a-radio-group v-model="form.mode" type="button" @change="onModeChange">
          <a-radio value="compute">仅计算（不向交易模块发送目标）</a-radio>
          <a-radio value="trade">发送至交易模块</a-radio>
        </a-radio-group>
      </a-form-item>
      <a-form-item
        v-if="form.mode === 'trade'"
        label="组合账户"
        required
        :validate-status="fieldErrors.logical_account_id ? 'error' : undefined"
        :help="fieldErrors.logical_account_id"
      >
        <a-select v-model="form.logical_account_id" allow-search :loading="accountsLoading" placeholder="选择接收目标的组合账户">
          <a-option
            v-for="option in accountOptions"
            :key="option.account.logical_account_id"
            :value="option.account.logical_account_id"
            :disabled="!!option.disabledReason"
            >{{ option.label }}</a-option
          >
        </a-select>
        <template #extra>
          <span v-if="accountsError" class="meta-error"
            >账户列表读取失败：{{ accountsError }}<a-link @click="loadAccountsList">重试</a-link>；也可以改为“仅计算”。</span
          >
          <span v-else-if="market.noAccount">{{ market.noAccount }}，请改为“仅计算”。</span>
          <span v-else-if="!accountsLoading && !accounts.length">当前空间没有可用的组合账户，请先在交易模块创建。</span>
        </template>
      </a-form-item>
      <a-form-item label="实例 ID（可选）"><a-input v-model="form.instance_id" placeholder="留空自动生成" /></a-form-item>
    </a-form>
    <div class="check-head">
      <strong>绑定预检</strong>
      <a-button size="small" :loading="checking" :disabled="!form.strategy_id || !tasksSelected" @click="check"
        >解析列与成员契约</a-button
      >
    </div>
    <a-alert
      v-for="item in check_result?.diagnostics || []"
      :key="item"
      :type="check_result?.resolved_json ? 'info' : 'error'"
      class="form-alert"
      >{{ item }}</a-alert
    >
    <ResolvedTable v-if="check_result?.resolved_json" :resolved-json="check_result.resolved_json" />
    <a-alert type="info" show-icon class="confirm-note"
      >创建后实例保持停用。启用时后台会把因子任务与实例永久关联，并重新解析 结果
      View、列与成员契约，固化为会话快照；绑定组合账户的实例在启用时认领账户会话。</a-alert
    >
    <template #footer>
      <a-space>
        <a-button @click="close">取消</a-button>
        <a-button type="primary" status="success" :loading="saving" @click="submit">创建停用实例</a-button>
      </a-space>
    </template>
  </a-drawer>
</template>

<script setup lang="ts">
import { StaleSpaceRequestError } from "@/api/admin/stale-space";
import { computed, reactive, ref, watch } from "vue";
import { Message } from "@arco-design/web-vue";
import { createInstance, getInstance, getStrategy, validateStrategy } from "@/api/strategy";
import type { Strategy, ValidateStrategyResult } from "@/api/strategy-types";
import { listFactorTasks } from "@/api/factor";
import { listAllDatasets } from "@/api/storage/metadata";
import { listLogicalAccounts, listTradingAccounts } from "@/api/trade";
import type { LogicalAccount, TradingAccount } from "@/api/trade/types";
import type { FactorTaskInfo } from "@/api/factor/types";
import { availableTasks, isUnifiedTask, varMemberBlockReason, type TaskOption } from "@/views/strategy/components/task-options";
import { isUnifiedMarketType } from "@/views/data/shared/series-market";
import ResolvedTable from "@/views/strategy/components/strategy-resolved-table.vue";
import { logicalAccountOptions, marketsToResolved, resolvedMarkets } from "@/views/strategy/components/logical-account-options";
import {
  UNIFIED_TASK_KEY,
  marketLabel,
  marketRequirement,
  poolMembers,
  poolRequirement as buildPoolRequirement
} from "@/views/strategy/market-mode";
import { describePool, parseDSL } from "@/views/strategy/dsl";

const props = defineProps<{ visible: boolean; strategies: Strategy[]; spaceId: string; presetStrategyId?: string }>();
const emit = defineEmits<{ "update:visible": [boolean]; created: [string] }>();
const saving = ref(false);
const checking = ref(false);
const tasksLoading = ref(false);
const accountsLoading = ref(false);
const tasksError = ref("");
const datasetsError = ref("");
const accountsError = ref("");
const error = ref("");
const fieldErrors = reactive<Record<"strategy_id" | "factor_tasks" | "logical_account_id", string>>({
  strategy_id: "",
  factor_tasks: "",
  logical_account_id: ""
});
// mode 决定是否发送目标：账户读取失败只影响“发送至交易模块”，不阻断仅计算；切换模式不会悄悄改选账户。
const form = reactive({
  instance_id: "",
  strategy_id: "",
  // 策略没有写 market 时只绑一个任务，市场由用户选定。
  single_market: "",
  // 输入方式：split 是各市场各一个因子任务，unified 是统一数据集上的一个因子任务（只有策略写了 market、不是策略池时可选）。
  input_mode: "split" as "split" | "unified",
  // 任务键（市场，或统一数据集的 multi）→ 因子任务。
  tasks: {} as Record<string, string>,
  logical_account_id: "",
  mode: "compute" as "compute" | "trade"
});
const tasks = ref<TaskOption[]>([]);
// 统一数据集（market_type 为 multi）的数据集 ID：因子任务的源数据集在其中的任务在统一数据集上。
const unifiedDatasetIds = ref<ReadonlySet<string>>(new Set());
const accounts = ref<LogicalAccount[]>([]);
// 成员执行账户：用来在账户选项前写测试网还是生产；读不到时为 null，选项只写“实盘”。
const tradingAccounts = ref<ReadonlyMap<string, TradingAccount> | null>(null);
const check_result = ref<ValidateStrategyResult | null>(null);
let tasksRequest = 0;
let datasetsRequest = 0;
let accountsRequest = 0;
let submitRequest = 0;
let checkRequest = 0;

const selectedStrategy = computed(() => props.strategies.find(item => item.strategy_id === form.strategy_id));
const preview = computed(() => (selectedStrategy.value ? parseDSL(selectedStrategy.value.dsl_yaml).preview : null));

// 只列策略驱动且可交易市场包含策略输出市场的组合账户，已被其他实例占用的显示但不可选；没有交易账户的市场（A 股）列表为空。
// 预检之前策略池就按成员市场的并集过滤；预检之后以服务端解析出的市场为准。
const market = computed(() =>
  check_result.value?.resolved_json || !poolRequirement.value?.markets.length
    ? resolvedMarkets(check_result.value?.resolved_json)
    : marketsToResolved(poolRequirement.value.markets)
);
const accountOptions = computed(() => logicalAccountOptions(accounts.value, market.value, tradingAccounts.value));

// 策略的市场模式决定要绑定哪些市场的任务；没写 market 时只绑一个，市场由用户选。
const singleMarkets = ["spot", "swap", "equity"];
// 策略池：成员引用的策略定义先从目录里找，目录里没有的按需读取（memberDefinitions：null 表示读取失败或不存在）。
const memberDefinitions = reactive<Record<string, string | null>>({});
const poolRequirement = computed(() =>
  selectedStrategy.value
    ? buildPoolRequirement(selectedStrategy.value.dsl_yaml, id => {
        const found = props.strategies.find(item => item.strategy_id === id);
        return found ? found.dsl_yaml : id in memberDefinitions ? memberDefinitions[id] : undefined;
      })
    : null
);
const requirement = computed(() =>
  poolRequirement.value
    ? { declared: true, markets: poolRequirement.value.markets }
    : marketRequirement(selectedStrategy.value?.dsl_yaml ?? "")
);
let memberRequest = 0;
const memberInflight = new Set<string>();
async function loadMissingMembers() {
  const dsl = selectedStrategy.value?.dsl_yaml;
  if (!dsl) return;
  const requestId = memberRequest;
  const missing = (poolMembers(dsl) ?? [])
    .map(member => member.strategyId)
    .filter(id => id && !(id in memberDefinitions) && !props.strategies.some(item => item.strategy_id === id));
  await Promise.all(
    [...new Set(missing)].map(async id => {
      const flight = `${requestId}:${id}`;
      if (memberInflight.has(flight)) return;
      memberInflight.add(flight);
      let definition: string | null = null;
      try {
        definition = (await getStrategy(id)).dsl_yaml || null;
      } catch {
        // 读取失败按“不存在或读取失败”提示，不阻塞其他成员
      }
      memberInflight.delete(flight);
      if (requestId === memberRequest) memberDefinitions[id] = definition;
    })
  );
}
watch(selectedStrategy, () => void loadMissingMembers(), { immediate: true });
// 预检或成员定义读到后账户列表会收窄：已选的账户不在新列表里（或变成不可选）时清掉，不能悄悄提交不符合的账户。
watch(accountOptions, options => {
  if (!form.logical_account_id) return;
  const kept = options.find(option => option.account.logical_account_id === form.logical_account_id);
  if (!kept || kept.disabledReason) form.logical_account_id = "";
});
const activeMarkets = computed(() =>
  requirement.value.declared ? requirement.value.markets : form.single_market ? [form.single_market] : []
);
// 统一数据集：策略写了 market（统一数据集没有可当默认值的市场）、不是策略池时可以选统一数据集上的任务。
const canUnified = computed(() => !poolRequirement.value && requirement.value.declared && requirement.value.markets.length > 0);
const unifiedMode = computed(() => canUnified.value && form.input_mode === "unified");
// 要绑定的任务键：统一数据集只有一个键 multi，否则是策略涉及的各市场。
const taskKeys = computed(() => (unifiedMode.value ? [UNIFIED_TASK_KEY] : activeMarkets.value));
const selectedTasks = computed(() =>
  taskKeys.value.flatMap(market => {
    const task = tasks.value.find(item => item.taskId === form.tasks[market]);
    return task ? [{ market, task }] : [];
  })
);
// 带可变成员的任务在当前绑定方式下能不能选（策略池、统一数据集、双市场都不能写 factor_params）。
const varBlockReason = computed(() =>
  varMemberBlockReason({
    pool: Boolean(poolRequirement.value),
    unified: unifiedMode.value,
    marketCount: activeMarkets.value.length
  })
);
const hasVarMemberTasks = computed(() => taskKeys.value.some(market => taskChoices(market).some(task => task.varMembers.length)));
const tasksSelected = computed(() => taskKeys.value.length > 0 && taskKeys.value.every(market => Boolean(form.tasks[market])));
// 统一数据集上的任务只能选 multi，其余键只能选单市场数据集上的任务；另一个键已选的任务不再出现：一个任务只能登记一次。
// 数据集信息读取失败时分不出哪些任务在统一数据集上，先都列出，由绑定预检核对。
function taskChoices(market: string) {
  const taken = new Set(taskKeys.value.filter(item => item !== market).map(item => form.tasks[item]));
  return tasks.value.filter(task => {
    if (taken.has(task.taskId)) return false;
    if (datasetsError.value) return true;
    return isUnifiedTask(task, unifiedDatasetIds.value) === (market === UNIFIED_TASK_KEY);
  });
}
// 提交与预检的 factor_tasks：只含要绑定的任务键。
function factorTasks(): Record<string, string> {
  return Object.fromEntries(taskKeys.value.map(market => [market, form.tasks[market]]));
}

async function copyColumns(task: TaskOption) {
  const text = task.columns.join("\n");
  try {
    await navigator.clipboard.writeText(text);
    Message.success("列名已复制");
  } catch {
    Message.warning("浏览器不允许写剪贴板，请手动选择列名复制");
  }
}

function close() {
  submitRequest += 1;
  saving.value = false;
  emit("update:visible", false);
}

async function loadAllTasks() {
  const items: FactorTaskInfo[] = [];
  for (let page = 1; ; page += 1) {
    const rsp = await listFactorTasks({ page: { page, size: 200 } });
    items.push(...(rsp.factor_tasks || []));
    if (!rsp.page_result?.has_more || !(rsp.factor_tasks || []).length) return items;
  }
}

async function loadAllAccounts() {
  const items: LogicalAccount[] = [];
  for (let page = 1; ; page += 1) {
    const rsp = await listLogicalAccounts({ page, size: 200 });
    items.push(...(rsp.logical_accounts || []));
    if (!rsp.page_result?.has_more || !(rsp.logical_accounts || []).length) return items;
  }
}

async function loadTasksList() {
  const spaceId = props.spaceId;
  if (!spaceId) return;
  const requestId = ++tasksRequest;
  tasksLoading.value = true;
  tasksError.value = "";
  try {
    const infos = await loadAllTasks();
    if (requestId !== tasksRequest || props.spaceId !== spaceId) return;
    tasks.value = availableTasks(infos, spaceId);
  } catch (err) {
    if (requestId === tasksRequest) tasksError.value = err instanceof Error ? err.message : "因子任务加载失败";
  } finally {
    if (requestId === tasksRequest) tasksLoading.value = false;
  }
}

async function loadDatasetsList() {
  const spaceId = props.spaceId;
  if (!spaceId) return;
  const requestId = ++datasetsRequest;
  datasetsError.value = "";
  try {
    const datasets = await listAllDatasets(spaceId);
    if (requestId !== datasetsRequest || props.spaceId !== spaceId) return;
    unifiedDatasetIds.value = new Set(
      datasets.filter(item => isUnifiedMarketType(item.attributes?.market_type)).map(item => item.dataset_id)
    );
  } catch (err) {
    if (requestId === datasetsRequest) datasetsError.value = err instanceof Error ? err.message : "数据集加载失败";
  }
}

async function loadAccountsList() {
  const spaceId = props.spaceId;
  if (!spaceId) return;
  const requestId = ++accountsRequest;
  accountsLoading.value = true;
  accountsError.value = "";
  try {
    const [nextAccounts, members] = await Promise.all([
      loadAllAccounts(),
      listTradingAccounts({ page: { page: 1, size: 200 } })
        .then(rsp => new Map((rsp.accounts || []).map(item => [item.trading_account_id, item])))
        .catch(() => null)
    ]);
    if (requestId !== accountsRequest || props.spaceId !== spaceId) return;
    accounts.value = nextAccounts.filter(account => account.space_id === spaceId);
    tradingAccounts.value = members;
  } catch (err) {
    if (requestId === accountsRequest) accountsError.value = err instanceof Error ? err.message : "账户读取失败";
  } finally {
    if (requestId === accountsRequest) accountsLoading.value = false;
  }
}

function loadMetadata() {
  void loadTasksList();
  void loadDatasetsList();
  void loadAccountsList();
}

function onStrategyChange() {
  fieldErrors.strategy_id = "";
  form.input_mode = "split";
  form.tasks = {};
  resetCheck();
}
function onMarketChange() {
  form.tasks = {};
  onTaskChange();
}
function onInputModeChange() {
  form.tasks = {};
  onTaskChange();
}
function onTaskChange() {
  fieldErrors.factor_tasks = "";
  resetCheck();
}
function onModeChange() {
  fieldErrors.logical_account_id = "";
  if (form.mode === "compute") form.logical_account_id = "";
}

function resetCheck() {
  checkRequest += 1;
  check_result.value = null;
  checking.value = false;
}

async function check() {
  // 选中的定义不在目录里（目录是旧的）：说清楚原因，不能点了没反应
  if (form.strategy_id && !selectedStrategy.value) {
    fieldErrors.strategy_id = "策略目录里没有这条定义，请关闭抽屉、刷新实例列表后重试";
    return;
  }
  if (!selectedStrategy.value || !tasksSelected.value) return;
  const requestId = ++checkRequest;
  checking.value = true;
  try {
    const result = await validateStrategy(selectedStrategy.value.dsl_yaml, factorTasks());
    if (requestId === checkRequest) check_result.value = result;
  } catch (err) {
    if (requestId === checkRequest)
      check_result.value = {
        diagnostics: [err instanceof Error ? err.message : "预检失败"],
        resolved_json: "",
        trial: null,
        trial_items: []
      };
  } finally {
    if (requestId === checkRequest) checking.value = false;
  }
}

function resetForm() {
  submitRequest += 1;
  saving.value = false;
  form.instance_id = "";
  form.strategy_id = props.presetStrategyId || "";
  memberRequest += 1;
  for (const key of Object.keys(memberDefinitions)) delete memberDefinitions[key];
  form.single_market = "";
  form.input_mode = "split";
  form.tasks = {};
  form.logical_account_id = "";
  form.mode = "compute";
  tasks.value = [];
  unifiedDatasetIds.value = new Set();
  datasetsRequest += 1;
  datasetsError.value = "";
  accounts.value = [];
  tradingAccounts.value = null;
  tasksRequest += 1;
  accountsRequest += 1;
  tasksLoading.value = false;
  accountsLoading.value = false;
  tasksError.value = "";
  accountsError.value = "";
  fieldErrors.strategy_id = fieldErrors.factor_tasks = fieldErrors.logical_account_id = "";
  error.value = "";
  resetCheck();
  void loadMissingMembers();
}

async function submit() {
  error.value = "";
  fieldErrors.strategy_id = form.strategy_id ? "" : "请选择策略定义";
  if (poolRequirement.value?.problems.length) {
    error.value = `策略池的成员还不能用：${poolRequirement.value.problems.join("；")}`;
    return;
  }
  fieldErrors.factor_tasks = !taskKeys.value.length
    ? "先选择任务的市场"
    : tasksSelected.value
      ? ""
      : unifiedMode.value
        ? "请选择统一数据集上的因子任务"
        : `请为${activeMarkets.value.map(marketLabel).join("、")}各选一个因子任务`;
  // 一个任务关联实例后永久占住：必须先通过绑定预检（服务端会核对市场与任务是否对应），再创建。
  if (!fieldErrors.factor_tasks && !check_result.value?.resolved_json)
    fieldErrors.factor_tasks = "请先点击“解析列与成员契约”，预检通过后再创建（预检会核对市场与任务是否对应）";
  fieldErrors.logical_account_id = form.mode === "trade" && !form.logical_account_id ? "请选择组合账户，或改为“仅计算”" : "";
  if (fieldErrors.strategy_id || fieldErrors.factor_tasks || fieldErrors.logical_account_id) return;
  const spaceId = props.spaceId;
  const requestId = ++submitRequest;
  saving.value = true;
  try {
    const instance = await createInstance({
      instance_id: form.instance_id.trim() || undefined,
      strategy_id: form.strategy_id,
      factor_tasks: factorTasks(),
      logical_account_id: form.mode === "trade" ? form.logical_account_id : ""
    });
    if (requestId !== submitRequest || props.spaceId !== spaceId) {
      Message.info("实例已创建，但当前空间已切换；请在原空间实例列表中确认");
      return;
    }
    // 成功后由父组件统一导航：不再发 update:visible(false)，否则父组件的“关闭抽屉”导航会取消去详情页的导航。
    submitRequest += 1;
    saving.value = false;
    emit("created", instance.instance_id);
    Message.success("策略实例已创建并保持停用");
  } catch (err) {
    if (requestId !== submitRequest || props.spaceId !== spaceId) return;
    const instanceId = form.instance_id.trim();
    if (instanceId) {
      try {
        await getInstance(instanceId);
        if (requestId !== submitRequest || props.spaceId !== spaceId) return; // 补查期间空间已切换
        error.value = "创建请求结果未知，但实例 ID 已存在，请返回列表确认，不要重复创建";
        return;
      } catch (lookupErr) {
        // 补查被切换空间取消：当前表单属于新空间，不写入旧空间的错误，也不能当作“实例不存在”
        if (lookupErr instanceof StaleSpaceRequestError || requestId !== submitRequest || props.spaceId !== spaceId) return;
        // 实例不存在，展示原始错误。
      }
    }
    error.value = err instanceof Error ? err.message : "策略实例创建失败";
  } finally {
    if (requestId === submitRequest) saving.value = false;
  }
}

// immediate：深链接（?create=1）进入时组件首次收到的 visible 就是 true，也要初始化表单并读取元数据。
watch(
  () => props.visible,
  value => {
    if (value) {
      resetForm();
      loadMetadata();
    }
  },
  { immediate: true }
);

watch(
  () => props.spaceId,
  (value, previous) => {
    if (value !== previous && props.visible) {
      resetForm();
      loadMetadata();
    }
  }
);
</script>

<style scoped>
.form-alert {
  margin-bottom: 12px;
}
.preview {
  margin-bottom: 16px;
}
.check-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin: 4px 0 10px;
}
.meta-error {
  color: rgb(var(--red-6));
}
.mode-hint {
  margin: 0 0 12px;
}
.meta-hint {
  margin: 6px 0 0;
  color: var(--color-text-3);
  font-size: 12px;
}
.task-columns {
  margin-bottom: 16px;
  padding: 10px 12px;
  border: 1px solid var(--color-border-2);
  border-radius: 4px;
}
.task-columns__head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 8px;
}
.confirm-note {
  margin-top: 16px;
}
</style>
