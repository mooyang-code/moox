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
      <a-alert v-if="requirementError" type="error" show-icon class="form-alert"
        >读取这份定义要绑定的计算任务失败：{{ requirementError }}<a-link @click="reloadRequirement">重试</a-link></a-alert
      >
      <a-alert v-if="requirement?.problems.length" type="warning" show-icon class="form-alert"
        ><div>这份定义暂不能建实例：</div>
        <div v-for="item in requirement.problems" :key="item">{{ item }}</div></a-alert
      >
      <a-form-item
        v-if="requirement && !requirement.marketDeclared"
        label="市场"
        :validate-status="!activeMarkets.length && fieldErrors.factor_tasks ? 'error' : undefined"
        :help="!activeMarkets.length ? fieldErrors.factor_tasks : undefined"
      >
        <a-radio-group v-model="form.single_market" type="button" @change="onMarketChange">
          <a-radio v-for="item in singleMarkets" :key="item" :value="item">{{ marketLabel(item) }}</a-radio>
        </a-radio-group>
        <template #extra
          >策略没有写
          market，只绑一个计算任务：先选任务的数据所属的市场（必须与任务源数据集的市场一致，创建前的预检会核对）。</template
        >
      </a-form-item>
      <p v-else-if="requirement && activeMarkets.length" class="meta-hint mode-hint">
        {{ requirement.pool ? "策略池要绑定各成员所需市场的并集：" : "策略的市场模式需要绑定："
        }}{{ activeMarkets.map(marketLabel).join("、") }} 的计算任务{{
          activeMarkets.length > 1 ? "（每个市场各一个，周期与日历要一致）" : ""
        }}。
      </p>
      <p v-if="extraKeys.length" class="meta-hint mode-hint">
        {{ extraHint }}
      </p>
      <a-form-item
        v-for="market in taskKeys"
        :key="market"
        :label="`${taskLabel(market)}计算任务`"
        required
        :validate-status="fieldErrors.factor_tasks ? 'error' : undefined"
        :help="market === taskKeys[0] ? fieldErrors.factor_tasks : undefined"
      >
        <a-select
          v-model="form.tasks[market]"
          allow-search
          allow-clear
          :loading="tasksLoading"
          placeholder="选择一个运行中的计算任务"
          @change="onTaskChange"
        >
          <a-option
            v-for="task in taskChoices(market)"
            :key="task.taskId"
            :value="task.taskId"
            :disabled="Boolean(varBlocked(market) && task.varMembers.length)"
          >
            {{ task.name }}（{{ task.taskId }} · {{ task.freq || "无周期"
            }}{{ varBlocked(market) && task.varMembers.length ? " · 有可变成员，不可选" : "" }}）
          </a-option>
        </a-select>
      </a-form-item>
      <p v-if="tasksError" class="meta-hint meta-error">
        计算任务列表读取失败：{{ tasksError }}<a-link @click="loadTasksList">重试</a-link>
      </p>
      <p v-if="!tasksLoading && !tasks.length" class="meta-hint">
        当前空间没有可用的计算任务：任务必须处于运行中。请先到「因子」模块创建并启用任务。
      </p>
      <p v-else-if="tuned" class="meta-hint">
        这份定义写了 factor_params：启用时主帧任务与本实例永久关联、由本实例提交可变成员的参数，已关联到别的实例的任务不能选；
        删除已停用的实例时，关联到它的任务和结果数据会一起删除（还有别的实例引用这个任务时先删它们）。
      </p>
      <p v-else class="meta-hint">
        这份定义没有
        factor_params：实例只读任务的结果列、不关联任务，多个实例可以共用同一个任务；删除实例时任务保留，不用了到「因子」模块手动停用。
      </p>
      <p v-if="hasBlockedVarTasks" class="meta-hint">标为“有可变成员，不可选”的任务：{{ SOURCE_VAR_MEMBER_REASON }}。</p>
      <div v-for="item in selectedTasks" :key="item.task.taskId" class="task-columns">
        <div class="task-columns__head">
          <strong>{{ taskLabel(item.market) }}任务的列（{{ columnsHint(item.market) }}）</strong>
          <a-button size="mini" @click="copyColumns(item.task)">复制全部列名</a-button>
        </div>
        <a-space wrap size="mini">
          <a-tag v-for="column in item.task.columns" :key="column" size="small">{{ column }}</a-tag>
          <span v-if="!item.task.columns.length" class="meta-hint">这个任务还没有成员，先到「因子」模块给任务添加因子。</span>
        </a-space>
        <p v-if="item.task.varMembers.length && varBlocked(item.market)" class="meta-hint meta-error">
          可变成员 {{ item.task.varMembers.join("、") }}：{{ varBlocked(item.market) }}，请换一个没有可变成员的任务。
        </p>
        <p v-else-if="item.task.varMembers.length && tuned && !splitTaskKey(item.market).source" class="meta-hint">
          可变成员 {{ item.task.varMembers.join("、") }}：DSL 的 factor_params 要为每个可变成员给出参数（策略逐期提交），可用
          params("成员键", "参数名") 读取当期实际参数。
        </p>
        <p v-else-if="item.task.varMembers.length" class="meta-hint">
          可变成员 {{ item.task.varMembers.join("、") }}：本策略只读它的列，参数由关联这个任务的实例提交{{
            item.task.boundInstanceId ? `（${item.task.boundInstanceId}）` : "（还没有关联，用默认参数）"
          }}，可用 params("成员键", "参数名") 读取当期实际参数。
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
        <a-select
          v-model="form.logical_account_id"
          allow-search
          :loading="accountsLoading"
          placeholder="选择接收目标的组合账户"
          @change="accountCleared = false"
        >
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
          <span v-else-if="!accountsLoading && !accounts.length">当前空间没有可用的组合账户，请先在交易模块创建。</span>
          <span v-else-if="!accountsLoading && !accountOptions.length"
            >没有可交易{{ marketNames }}的策略驱动组合账户，请先在交易模块创建，或改为“仅计算”。</span
          >
          <span v-else-if="accountCleared">之前选的组合账户不支持策略的市场，已清空，请重新选择。</span>
        </template>
      </a-form-item>
      <a-form-item label="实例 ID（可选）"><a-input v-model="form.instance_id" placeholder="留空自动生成" /></a-form-item>
    </a-form>
    <div class="check-head">
      <strong>绑定预检</strong>
      <a-button size="small" :loading="checking" :disabled="!form.strategy_id || !tasksSelected" @click="check()"
        >先预检</a-button
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
    <section v-if="check_result?.resolved_json && check_result.trial" class="check-trial">
      <strong>按当前数据试算的第一期目标</strong>
      <ResultItems :result="check_result.trial" :items="check_result.trial_items" />
    </section>
    <a-alert type="info" show-icon class="confirm-note"
      >创建后实例保持停用。启用时后台重新解析结果 View、列与成员契约，固化为会话快照；写了 factor_params
      的实例还会把主帧任务与实例永久关联；绑定组合账户的实例启用后，之后的策略目标由它执行下单。</a-alert
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
import { createInstance, getInstance, listInstances, validateStrategy } from "@/api/strategy";
import type { Strategy, ValidateStrategyResult } from "@/api/strategy-types";
import { listAllFactorTasks } from "@/api/factor";
import { readAllPages } from "@/api/read-all-pages";
import { listLogicalAccounts, listTradingAccounts } from "@/api/trade";
import type { LogicalAccount, TradingAccount } from "@/api/trade/types";
import {
  declaresFactorParams,
  enabledTasks,
  selectableTask,
  varMemberBlockReason,
  SOURCE_VAR_MEMBER_REASON,
  type TaskOption
} from "@/views/strategy/components/task-options";
import ResolvedTable from "@/views/strategy/components/strategy-resolved-table.vue";
import ResultItems from "@/views/strategy/components/strategy-result-items.vue";
import {
  enabledInstanceAccounts,
  logicalAccountOptions,
  marketsToResolved,
  resolvedMarkets
} from "@/views/strategy/components/logical-account-options";
import {
  mainMarkets,
  marketLabel,
  requirementReferenceMarkets,
  requirementTaskKeys,
  splitTaskKey,
  taskKeyLabel
} from "@/views/strategy/market-mode";
import { useTaskRequirement } from "@/views/strategy/use-task-requirement";
import { describePool, parseDSL } from "@/views/strategy/dsl";

const props = defineProps<{ visible: boolean; strategies: Strategy[]; spaceId: string; presetStrategyId?: string }>();
const emit = defineEmits<{ "update:visible": [boolean]; created: [string] }>();
const saving = ref(false);
const checking = ref(false);
const tasksLoading = ref(false);
const accountsLoading = ref(false);
const tasksError = ref("");
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
  // 任务键（市场，或附加来源的 来源名:市场）→ 计算任务。
  tasks: {} as Record<string, string>,
  logical_account_id: "",
  mode: "compute" as "compute" | "trade"
});
const tasks = ref<TaskOption[]>([]);
const accounts = ref<LogicalAccount[]>([]);
// 成员执行账户：用来在账户选项前写测试网还是生产；读不到时为 null，选项只写“实盘”。
const tradingAccounts = ref<ReadonlyMap<string, TradingAccount> | null>(null);
// 组合账户 → 绑着它的启用实例（取自 Strategy 的实例列表，Trade 不存归属）：这些账户显示但不可选；读不到时为 null，不置灰，启用时由 Strategy 拒绝。
const boundInstances = ref<ReadonlyMap<string, string> | null>(null);
const check_result = ref<ValidateStrategyResult | null>(null);
let tasksRequest = 0;
let accountsRequest = 0;
let submitRequest = 0;
let checkRequest = 0;

const selectedStrategy = computed(() => props.strategies.find(item => item.strategy_id === form.strategy_id));
const preview = computed(() => (selectedStrategy.value ? parseDSL(selectedStrategy.value.dsl_yaml).preview : null));
// 定义写了 factor_params：启用时独占关联主帧任务，已关联到别的实例的主帧任务不能选。
const tuned = computed(() => declaresFactorParams(selectedStrategy.value?.dsl_yaml ?? ""));

// 策略的市场模式决定要绑定哪些市场的任务；没写 market 时只绑一个，市场由用户选。
const singleMarkets = ["spot", "swap", "equity"];
// 要绑定的任务由后端按定义的 DSL 推出（GetStrategy 的 requirement）：任务键、是否策略池、建不了实例的问题。
const {
  requirement,
  error: requirementError,
  reload: reloadRequirement
} = useTaskRequirement(computed(() => ({ strategyId: form.strategy_id })));
// 要绑定的任务键：主帧市场、参考市场、附加来源（来源名:市场）；没写 market 时按选的市场拼出。
const taskKeys = computed(() => requirementTaskKeys(requirement.value, form.single_market));
// 主帧市场：不带来源、也不是参考市场的键；参考市场只用于 age_markets: all 的年龄判定。
const activeMarkets = computed(() => mainMarkets(requirement.value, form.single_market));
const referenceKeys = computed(() => requirementReferenceMarkets(requirement.value, taskKeys.value));
// 只列策略驱动且可交易市场包含策略输出市场的组合账户（A 股要开通了股票市场），已被其他实例占用的显示但不可选。
// 预检之后以服务端解析出的市场为准；预检之前按要绑定的市场（没写 market 时按选的市场）先筛。
const market = computed(() => {
  if (check_result.value?.resolved_json) return resolvedMarkets(check_result.value.resolved_json);
  return activeMarkets.value.length ? marketsToResolved(activeMarkets.value) : {};
});
const marketNames = computed(() => (activeMarkets.value.length ? activeMarkets.value.map(marketLabel).join("、") : "策略市场"));
// 已选的账户因为市场收窄被清空：提示一句，用户重新选择后消失。
const accountCleared = ref(false);
const accountOptions = computed(() =>
  logicalAccountOptions(accounts.value, market.value, tradingAccounts.value, boundInstances.value)
);
// 预检或成员定义读到后账户列表会收窄：已选的账户不在新列表里（或变成不可选）时清掉，不能悄悄提交不符合的账户。
watch(accountOptions, options => {
  if (!form.logical_account_id) return;
  const kept = options.find(option => option.account.logical_account_id === form.logical_account_id);
  if (!kept || kept.disabledReason) {
    form.logical_account_id = "";
    accountCleared.value = true;
  }
});
// 主帧之外的任务键：参考市场与附加来源（来源名:市场）
const extraKeys = computed(() => taskKeys.value.filter(key => splitTaskKey(key).source || referenceKeys.value.includes(key)));
const extraHint = computed(() => {
  const parts: string[] = [];
  if (referenceKeys.value.length)
    parts.push(
      `参考市场 ${referenceKeys.value.map(marketLabel).join("、")} 的计算任务只用于 age_markets: all 的年龄判定，不参与选择与下单`
    );
  const sources = requirement.value?.sources ?? [];
  if (sources.length)
    parts.push(
      `附加来源 ${sources.map(source => `${source.name}（${source.frequency || "?"}）`).join("、")} 按主帧的市场各绑一个计算任务：周期要与来源的 bar 一致、是主帧周期的整数倍，任务不能有可变成员`
    );
  return parts.join("；") + "。";
});
function taskLabel(key: string) {
  return referenceKeys.value.includes(key) ? `参考市场（${marketLabel(key)}）` : taskKeyLabel(key);
}
function columnsHint(key: string) {
  const { source } = splitTaskKey(key);
  if (source) return `DSL 里写成 ${source}.列名`;
  if (referenceKeys.value.includes(key)) return "只用于年龄判定";
  return "DSL 里直接写这些列名";
}
// 带可变成员的任务能不能选：只有附加来源的任务不能有可变成员，主帧任务都能选（不调参的实例只读可变成员的列）。
function varBlocked(key: string) {
  return varMemberBlockReason(splitTaskKey(key).source);
}
const selectedTasks = computed(() =>
  taskKeys.value.flatMap(market => {
    const task = tasks.value.find(item => item.taskId === form.tasks[market]);
    return task ? [{ market, task }] : [];
  })
);
// 有没有因为“附加来源不能有可变成员”被置灰的候选任务。
const hasBlockedVarTasks = computed(() =>
  taskKeys.value.some(key => varBlocked(key) && taskChoices(key).some(task => task.varMembers.length))
);
const tasksSelected = computed(() => taskKeys.value.length > 0 && taskKeys.value.every(market => Boolean(form.tasks[market])));
// 另一个键已选的任务不再出现：一个任务只能登记一次；写了 factor_params 的定义不能选已关联到别的实例的主帧任务。
// 任务的市场是否与键对应由绑定预检核对。
function taskChoices(market: string) {
  const taken = new Set(taskKeys.value.filter(item => item !== market).map(item => form.tasks[item]));
  const source = splitTaskKey(market).source;
  return tasks.value.filter(task => !taken.has(task.taskId) && selectableTask(task, tuned.value, source));
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

function loadAllAccounts() {
  return readAllPages(async page => {
    const rsp = await listLogicalAccounts({ page, size: 200 });
    return { items: rsp.logical_accounts || [], hasMore: Boolean(rsp.page_result?.has_more) };
  });
}

async function loadTasksList() {
  const spaceId = props.spaceId;
  if (!spaceId) return;
  const requestId = ++tasksRequest;
  tasksLoading.value = true;
  tasksError.value = "";
  try {
    const infos = await listAllFactorTasks();
    if (requestId !== tasksRequest || props.spaceId !== spaceId) return;
    tasks.value = enabledTasks(infos, spaceId);
  } catch (err) {
    if (requestId === tasksRequest) tasksError.value = err instanceof Error ? err.message : "计算任务加载失败";
  } finally {
    if (requestId === tasksRequest) tasksLoading.value = false;
  }
}

async function loadAccountsList() {
  const spaceId = props.spaceId;
  if (!spaceId) return;
  const requestId = ++accountsRequest;
  accountsLoading.value = true;
  accountsError.value = "";
  try {
    const [nextAccounts, members, bound] = await Promise.all([
      loadAllAccounts(),
      listTradingAccounts({ page: { page: 1, size: 200 } })
        .then(rsp => new Map((rsp.accounts || []).map(item => [item.trading_account_id, item])))
        .catch(() => null),
      listInstances({ page: 1, size: 500, enabled: true })
        .then(rsp => enabledInstanceAccounts(rsp.items))
        .catch(() => null)
    ]);
    if (requestId !== accountsRequest || props.spaceId !== spaceId) return;
    accounts.value = nextAccounts.filter(account => account.space_id === spaceId);
    tradingAccounts.value = members;
    boundInstances.value = bound;
  } catch (err) {
    if (requestId === accountsRequest) accountsError.value = err instanceof Error ? err.message : "账户读取失败";
  } finally {
    if (requestId === accountsRequest) accountsLoading.value = false;
  }
}

function loadMetadata() {
  void loadTasksList();
  void loadAccountsList();
}

function onStrategyChange() {
  fieldErrors.strategy_id = "";
  // 预检作废了，“预检没有通过”的提示也不再成立
  error.value = "";
  form.tasks = {};
  resetCheck();
}
function onMarketChange() {
  form.tasks = {};
  onTaskChange();
}
function onTaskChange() {
  fieldErrors.factor_tasks = "";
  error.value = "";
  resetCheck();
}
function onModeChange() {
  fieldErrors.logical_account_id = "";
  accountCleared.value = false;
  if (form.mode === "compute") form.logical_account_id = "";
}

function resetCheck() {
  checkRequest += 1;
  check_result.value = null;
  checking.value = false;
}

// 进行中的预检：预检还没回来时再点“创建”，等这一次的结果，不重复发请求。
let checkPromise: Promise<ValidateStrategyResult | null> | null = null;

/** 预检：返回本次结果；被作废（改了定义、任务、市场或切了空间）时返回 null。关掉抽屉不作废预检，由 submitRequest 挡住创建。 */
function check(): Promise<ValidateStrategyResult | null> {
  // 选中的定义不在目录里（目录是旧的）：说清楚原因，不能点了没反应
  if (form.strategy_id && !selectedStrategy.value) {
    fieldErrors.strategy_id = "策略目录里没有这条定义，请关闭抽屉、刷新实例列表后重试";
    return Promise.resolve(null);
  }
  if (!selectedStrategy.value || !tasksSelected.value) return Promise.resolve(null);
  if (checking.value && checkPromise) return checkPromise;
  const requestId = ++checkRequest;
  const dsl = selectedStrategy.value.dsl_yaml;
  checking.value = true;
  checkPromise = (async () => {
    try {
      const result = await validateStrategy(dsl, factorTasks());
      if (requestId !== checkRequest) return null;
      check_result.value = result;
      return result;
    } catch (err) {
      if (requestId !== checkRequest) return null;
      check_result.value = {
        diagnostics: [err instanceof Error ? err.message : "预检失败"],
        resolved_json: "",
        trial: null,
        trial_items: [],
        requirement: null
      };
      return check_result.value;
    } finally {
      if (requestId === checkRequest) checking.value = false;
    }
  })();
  return checkPromise;
}

function resetForm() {
  submitRequest += 1;
  saving.value = false;
  form.instance_id = "";
  form.strategy_id = props.presetStrategyId || "";
  form.single_market = "";
  form.tasks = {};
  form.logical_account_id = "";
  form.mode = "compute";
  tasks.value = [];
  accounts.value = [];
  tradingAccounts.value = null;
  boundInstances.value = null;
  tasksRequest += 1;
  accountsRequest += 1;
  tasksLoading.value = false;
  accountsLoading.value = false;
  tasksError.value = "";
  accountsError.value = "";
  fieldErrors.strategy_id = fieldErrors.factor_tasks = fieldErrors.logical_account_id = "";
  error.value = "";
  accountCleared.value = false;
  resetCheck();
}

async function submit() {
  error.value = "";
  fieldErrors.strategy_id = form.strategy_id ? "" : "请选择策略定义";
  if (requirement.value?.problems.length) {
    error.value = `这份定义暂不能建实例：${requirement.value.problems.join("；")}`;
    return;
  }
  fieldErrors.factor_tasks = !taskKeys.value.length
    ? "先选择任务的市场"
    : tasksSelected.value
      ? ""
      : `请为${taskKeys.value.map(taskLabel).join("、")}各选一个计算任务`;
  fieldErrors.logical_account_id = form.mode === "trade" && !form.logical_account_id ? "请选择组合账户，或改为“仅计算”" : "";
  if (fieldErrors.strategy_id || fieldErrors.factor_tasks || fieldErrors.logical_account_id) return;
  const spaceId = props.spaceId;
  const requestId = ++submitRequest;
  saving.value = true;
  try {
    // 写了 factor_params 的实例启用后永久占住任务：没有当前有效的预检结果就先预检（服务端会核对市场与任务是否对应），通过了才创建。
    if (!check_result.value?.resolved_json) {
      const result = await check();
      // 预检被作废（等待期间改了定义、任务或市场）：不创建，按新的选择再点创建
      if (!result || requestId !== submitRequest || props.spaceId !== spaceId) return;
      if (!result.resolved_json) {
        error.value = "预检没有通过（原因见下方），没有创建实例";
        return;
      }
    }
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
.check-trial {
  display: flex;
  flex-direction: column;
  gap: 8px;
  margin: 12px 0;
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
