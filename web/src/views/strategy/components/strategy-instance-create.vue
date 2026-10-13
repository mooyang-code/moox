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
        label="输入 View"
        required
        :validate-status="fieldErrors.view_id ? 'error' : undefined"
        :help="fieldErrors.view_id"
      >
        <a-select
          v-model="form.view_id"
          allow-search
          :loading="viewsLoading"
          placeholder="选择一个 View（通常是因子结果 View）"
          @change="onViewChange"
        >
          <a-option v-for="view in views" :key="view.view_id" :value="view.view_id">
            {{ view.name || view.view_id }}（{{ view.view_id }} · {{ view.freq || "无周期" }}）<template
              v-if="isFactorView(view)"
            >
              · 因子结果</template
            >
          </a-option>
        </a-select>
        <template #extra>
          <span v-if="viewsError" class="meta-error"
            >View 列表读取失败：{{ viewsError }}<a-link @click="loadViewsList">重试</a-link></span
          >
          <span v-else-if="!viewsLoading && !views.length">当前空间没有可用 View，请先在存储模块创建或发布 View。</span>
        </template>
      </a-form-item>
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
      <a-button size="small" :loading="checking" :disabled="!form.strategy_id || !form.view_id" @click="check"
        >解析列与因子指纹</a-button
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
      >创建后实例保持停用。启用时后台会重新解析
      View、列与因子指纹并固化为会话快照；绑定组合账户的实例在启用时认领账户会话。</a-alert
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
import { listViews } from "@/api/storage/metadata";
import type { View } from "@/api/storage/types";
import { listLogicalAccounts } from "@/api/trade";
import type { LogicalAccount } from "@/api/trade/types";
import ResolvedTable from "@/views/strategy/components/strategy-resolved-table.vue";
import { parseDSL } from "@/views/strategy/dsl";

const props = defineProps<{ visible: boolean; strategies: Strategy[]; spaceId: string; presetStrategyId?: string }>();
const emit = defineEmits<{ "update:visible": [boolean]; created: [string] }>();
const saving = ref(false);
const checking = ref(false);
const viewsLoading = ref(false);
const accountsLoading = ref(false);
const viewsError = ref("");
const accountsError = ref("");
const error = ref("");
const fieldErrors = reactive<Record<"strategy_id" | "view_id" | "logical_account_id", string>>({
  strategy_id: "",
  view_id: "",
  logical_account_id: ""
});
// mode 决定是否发送目标：账户读取失败只影响“发送至交易模块”，不阻断仅计算；切换模式不会悄悄改选账户。
const form = reactive({
  instance_id: "",
  strategy_id: "",
  view_id: "",
  logical_account_id: "",
  mode: "compute" as "compute" | "trade"
});
const views = ref<View[]>([]);
const accounts = ref<LogicalAccount[]>([]);
const check_result = ref<ValidateStrategyResult | null>(null);
let viewsRequest = 0;
let accountsRequest = 0;
let submitRequest = 0;
let checkRequest = 0;

const selectedStrategy = computed(() => props.strategies.find(item => item.strategy_id === form.strategy_id));
const preview = computed(() => (selectedStrategy.value ? parseDSL(selectedStrategy.value.dsl_yaml).preview : null));

function isFactorView(view: View) {
  return view.attributes?.view_role === "factor_result" || view.attributes?.owner_module === "factor";
}

function close() {
  submitRequest += 1;
  saving.value = false;
  emit("update:visible", false);
}

async function loadAllViews(spaceId: string) {
  const items: View[] = [];
  for (let page = 1; ; page += 1) {
    const rsp = await listViews({ space_id: spaceId, status: "active", page: { page, size: 200 } });
    items.push(...(rsp.views || []));
    if (!rsp.page_result?.has_more || !(rsp.views || []).length) return items;
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

async function loadViewsList() {
  const spaceId = props.spaceId;
  if (!spaceId) return;
  const requestId = ++viewsRequest;
  viewsLoading.value = true;
  viewsError.value = "";
  try {
    const nextViews = await loadAllViews(spaceId);
    if (requestId !== viewsRequest || props.spaceId !== spaceId) return;
    views.value = nextViews
      .filter(view => view.space_id === spaceId)
      .sort((a, b) => Number(isFactorView(b)) - Number(isFactorView(a)) || a.view_id.localeCompare(b.view_id));
  } catch (err) {
    if (requestId === viewsRequest) viewsError.value = err instanceof Error ? err.message : "元数据加载失败";
  } finally {
    if (requestId === viewsRequest) viewsLoading.value = false;
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
  void loadViewsList();
  void loadAccountsList();
}

function onStrategyChange() {
  fieldErrors.strategy_id = "";
  resetCheck();
}
function onViewChange() {
  fieldErrors.view_id = "";
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
  if (!selectedStrategy.value || !form.view_id) return;
  const requestId = ++checkRequest;
  checking.value = true;
  try {
    const result = await validateStrategy(selectedStrategy.value.dsl_yaml, form.view_id);
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
  form.view_id = "";
  form.logical_account_id = "";
  form.mode = "compute";
  views.value = [];
  accounts.value = [];
  viewsRequest += 1;
  accountsRequest += 1;
  viewsLoading.value = false;
  accountsLoading.value = false;
  viewsError.value = "";
  accountsError.value = "";
  fieldErrors.strategy_id = fieldErrors.view_id = fieldErrors.logical_account_id = "";
  error.value = "";
  resetCheck();
}

async function submit() {
  error.value = "";
  fieldErrors.strategy_id = form.strategy_id ? "" : "请选择策略定义";
  fieldErrors.view_id = form.view_id ? "" : "请选择输入 View";
  fieldErrors.logical_account_id = form.mode === "trade" && !form.logical_account_id ? "请选择组合账户，或改为“仅计算”" : "";
  if (fieldErrors.strategy_id || fieldErrors.view_id || fieldErrors.logical_account_id) return;
  const spaceId = props.spaceId;
  const requestId = ++submitRequest;
  saving.value = true;
  try {
    const instance = await createInstance({
      instance_id: form.instance_id.trim() || undefined,
      strategy_id: form.strategy_id,
      view_id: form.view_id,
      logical_account_id: form.mode === "trade" ? form.logical_account_id : ""
    });
    if (requestId !== submitRequest || props.spaceId !== spaceId) {
      Message.info("实例已创建，但当前空间已切换；请在原空间实例列表中确认");
      return;
    }
    emit("created", instance.instance_id);
    close();
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

watch(
  () => props.visible,
  value => {
    if (value) {
      resetForm();
      loadMetadata();
    }
  }
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
.confirm-note {
  margin-top: 16px;
}
</style>
