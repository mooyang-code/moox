<template>
  <a-drawer :visible="visible" :width="'min(760px, 100vw)'" title="新建策略实例" unmount-on-close @cancel="close">
    <a-alert v-if="error" type="error" show-icon class="form-alert">{{ error }}</a-alert>
    <a-form layout="vertical">
      <a-form-item label="策略定义" required>
        <a-select v-model="form.strategy_id" allow-search placeholder="选择一份 DSL 定义" @change="resetCheck">
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
      <a-form-item label="输入 View" required>
        <a-select
          v-model="form.view_id"
          allow-search
          :loading="metadataLoading"
          placeholder="选择一个 View（通常是因子结果 View）"
          @change="resetCheck"
        >
          <a-option v-for="view in views" :key="view.view_id" :value="view.view_id">
            {{ view.name || view.view_id }}（{{ view.view_id }} · {{ view.freq || "无周期" }}）<template
              v-if="isFactorView(view)"
            >
              · 因子结果</template
            >
          </a-option>
        </a-select>
      </a-form-item>
      <a-form-item label="运行模式">
        <a-radio-group v-model="form.logical_account_id" direction="vertical">
          <a-radio value="">仅计算（观察实例，不向交易模块发送目标）</a-radio>
          <a-radio v-for="account in accounts" :key="account.logical_account_id" :value="account.logical_account_id"
            >发送给交易模块：{{ account.name || account.logical_account_id }}</a-radio
          >
        </a-radio-group>
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
const metadataLoading = ref(false);
const error = ref("");
const form = reactive({ instance_id: "", strategy_id: "", view_id: "", logical_account_id: "" });
const views = ref<View[]>([]);
const accounts = ref<LogicalAccount[]>([]);
const check_result = ref<ValidateStrategyResult | null>(null);
let metadataRequest = 0;
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

async function loadMetadata() {
  const spaceId = props.spaceId;
  if (!spaceId) return;
  const requestId = ++metadataRequest;
  metadataLoading.value = true;
  error.value = "";
  try {
    const [nextViews, nextAccounts] = await Promise.all([
      loadAllViews(spaceId),
      loadAllAccounts().catch(() => [] as LogicalAccount[])
    ]);
    if (requestId !== metadataRequest || props.spaceId !== spaceId) return;
    views.value = nextViews
      .filter(view => view.space_id === spaceId)
      .sort((a, b) => Number(isFactorView(b)) - Number(isFactorView(a)) || a.view_id.localeCompare(b.view_id));
    accounts.value = nextAccounts.filter(account => account.space_id === spaceId);
  } catch (err) {
    if (requestId === metadataRequest) error.value = err instanceof Error ? err.message : "元数据加载失败";
  } finally {
    if (requestId === metadataRequest) metadataLoading.value = false;
  }
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
  views.value = [];
  accounts.value = [];
  error.value = "";
  resetCheck();
}

async function submit() {
  error.value = "";
  if (!form.strategy_id || !form.view_id) {
    error.value = "请选择策略定义与输入 View";
    return;
  }
  const spaceId = props.spaceId;
  const requestId = ++submitRequest;
  saving.value = true;
  try {
    const instance = await createInstance({
      instance_id: form.instance_id.trim() || undefined,
      strategy_id: form.strategy_id,
      view_id: form.view_id,
      logical_account_id: form.logical_account_id
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
        error.value = "创建请求结果未知，但实例 ID 已存在，请返回列表确认，不要重复创建";
        return;
      } catch {
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
.confirm-note {
  margin-top: 16px;
}
</style>
