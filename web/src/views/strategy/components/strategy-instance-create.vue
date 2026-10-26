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
        <a-descriptions-item label="规则">{{
          preview.rules.map(rule => `${rule.name || rule.id}（${rule.type}）`).join("、")
        }}</a-descriptions-item>
        <a-descriptions-item label="标的池">{{ preview.universe }}</a-descriptions-item>
        <a-descriptions-item label="bar 断言">{{ preview.bar }}</a-descriptions-item>
      </a-descriptions>
      <a-form-item
        label="因子任务"
        required
        :validate-status="fieldErrors.factor_task_id ? 'error' : undefined"
        :help="fieldErrors.factor_task_id"
      >
        <a-select
          v-model="form.factor_task_id"
          allow-search
          :loading="tasksLoading"
          placeholder="选择一个运行中、还没有关联实例的因子任务"
          @change="onTaskChange"
        >
          <a-option v-for="task in tasks" :key="task.taskId" :value="task.taskId">
            {{ task.name }}（{{ task.taskId }} · {{ task.freq || "无周期" }}）
          </a-option>
        </a-select>
        <template #extra>
          <span v-if="tasksError" class="meta-error"
            >因子任务列表读取失败：{{ tasksError }}<a-link @click="loadTasksList">重试</a-link></span
          >
          <span v-else-if="!tasksLoading && !tasks.length"
            >当前空间没有可用的因子任务：任务必须处于运行中，且还没有关联策略实例。请先到「因子」模块创建并启用任务。</span
          >
          <span v-else>一个因子任务只能被一个实例独占使用，关联创建后不能修改，也不能删除实例。</span>
        </template>
      </a-form-item>
      <div v-if="selectedTask" class="task-columns">
        <div class="task-columns__head">
          <strong>任务的列（DSL 里直接写这些列名）</strong>
          <a-button size="mini" @click="copyColumns">复制全部列名</a-button>
        </div>
        <a-space wrap size="mini">
          <a-tag v-for="column in selectedTask.columns" :key="column" size="small">{{ column }}</a-tag>
          <span v-if="!selectedTask.columns.length" class="meta-hint">这个任务还没有成员，先到「因子」模块给任务添加因子。</span>
        </a-space>
        <p v-if="selectedTask.varMembers.length" class="meta-hint">
          可变成员 {{ selectedTask.varMembers.join("、") }}：DSL 的 factor_params 要为每个可变成员给出参数（策略逐期提交），可用
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
          <a-option v-for="account in accounts" :key="account.logical_account_id" :value="account.logical_account_id">{{
            account.name || account.logical_account_id
          }}</a-option>
        </a-select>
        <template #extra>
          <span v-if="accountsError" class="meta-error"
            >账户列表读取失败：{{ accountsError }}<a-link @click="loadAccountsList">重试</a-link>；也可以改为“仅计算”。</span
          >
          <span v-else-if="!accountsLoading && !accounts.length">当前空间没有可用的组合账户，请先在交易模块创建。</span>
        </template>
      </a-form-item>
      <a-form-item label="实例 ID（可选）"><a-input v-model="form.instance_id" placeholder="留空自动生成" /></a-form-item>
    </a-form>
    <div class="check-head">
      <strong>绑定预检</strong>
      <a-button size="small" :loading="checking" :disabled="!form.strategy_id || !form.factor_task_id" @click="check"
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
import { createInstance, getInstance, validateStrategy } from "@/api/strategy";
import type { Strategy, ValidateStrategyResult } from "@/api/strategy-types";
import { listFactorTasks } from "@/api/factor";
import { listLogicalAccounts } from "@/api/trade";
import type { LogicalAccount } from "@/api/trade/types";
import type { FactorTaskInfo } from "@/api/factor/types";
import { availableTasks, type TaskOption } from "@/views/strategy/components/task-options";
import ResolvedTable from "@/views/strategy/components/strategy-resolved-table.vue";
import { parseDSL } from "@/views/strategy/dsl";

const props = defineProps<{ visible: boolean; strategies: Strategy[]; spaceId: string; presetStrategyId?: string }>();
const emit = defineEmits<{ "update:visible": [boolean]; created: [string] }>();
const saving = ref(false);
const checking = ref(false);
const tasksLoading = ref(false);
const accountsLoading = ref(false);
const tasksError = ref("");
const accountsError = ref("");
const error = ref("");
const fieldErrors = reactive<Record<"strategy_id" | "factor_task_id" | "logical_account_id", string>>({
  strategy_id: "",
  factor_task_id: "",
  logical_account_id: ""
});
// mode 决定是否发送目标：账户读取失败只影响“发送至交易模块”，不阻断仅计算；切换模式不会悄悄改选账户。
const form = reactive({
  instance_id: "",
  strategy_id: "",
  factor_task_id: "",
  logical_account_id: "",
  mode: "compute" as "compute" | "trade"
});
const tasks = ref<TaskOption[]>([]);
const accounts = ref<LogicalAccount[]>([]);
const check_result = ref<ValidateStrategyResult | null>(null);
let tasksRequest = 0;
let accountsRequest = 0;
let submitRequest = 0;
let checkRequest = 0;

const selectedStrategy = computed(() => props.strategies.find(item => item.strategy_id === form.strategy_id));
const preview = computed(() => (selectedStrategy.value ? parseDSL(selectedStrategy.value.dsl_yaml).preview : null));

const selectedTask = computed(() => tasks.value.find(item => item.taskId === form.factor_task_id));

async function copyColumns() {
  const text = selectedTask.value?.columns.join("\n") ?? "";
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

async function loadAccountsList() {
  const spaceId = props.spaceId;
  if (!spaceId) return;
  const requestId = ++accountsRequest;
  accountsLoading.value = true;
  accountsError.value = "";
  try {
    const nextAccounts = await loadAllAccounts();
    if (requestId !== accountsRequest || props.spaceId !== spaceId) return;
    accounts.value = nextAccounts.filter(account => account.space_id === spaceId);
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
  resetCheck();
}
function onTaskChange() {
  fieldErrors.factor_task_id = "";
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
  if (!selectedStrategy.value || !form.factor_task_id) return;
  const requestId = ++checkRequest;
  checking.value = true;
  try {
    const result = await validateStrategy(selectedStrategy.value.dsl_yaml, form.factor_task_id);
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
  form.factor_task_id = "";
  form.logical_account_id = "";
  form.mode = "compute";
  tasks.value = [];
  accounts.value = [];
  tasksRequest += 1;
  accountsRequest += 1;
  tasksLoading.value = false;
  accountsLoading.value = false;
  tasksError.value = "";
  accountsError.value = "";
  fieldErrors.strategy_id = fieldErrors.factor_task_id = fieldErrors.logical_account_id = "";
  error.value = "";
  resetCheck();
}

async function submit() {
  error.value = "";
  fieldErrors.strategy_id = form.strategy_id ? "" : "请选择策略定义";
  fieldErrors.factor_task_id = form.factor_task_id ? "" : "请选择因子任务";
  fieldErrors.logical_account_id = form.mode === "trade" && !form.logical_account_id ? "请选择组合账户，或改为“仅计算”" : "";
  if (fieldErrors.strategy_id || fieldErrors.factor_task_id || fieldErrors.logical_account_id) return;
  const spaceId = props.spaceId;
  const requestId = ++submitRequest;
  saving.value = true;
  try {
    const instance = await createInstance({
      instance_id: form.instance_id.trim() || undefined,
      strategy_id: form.strategy_id,
      factor_task_id: form.factor_task_id,
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
