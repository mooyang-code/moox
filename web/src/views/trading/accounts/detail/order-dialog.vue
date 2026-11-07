<template>
  <a-modal
    v-model:visible="visible"
    :modal-class="`tx-confirm tx-confirm--${tier.env}`"
    width="min(560px, calc(100vw - 24px))"
    :ok-text="pendingSubmission ? '使用原请求重试' : orderEntryLabel"
    :ok-button-props="{ status: tier.env === 'prod' ? 'danger' : 'normal', disabled: !nameOk }"
    :on-before-ok="submitManual"
    :ok-loading="submitting"
  >
    <template #title>
      <span>{{ orderEntryLabel }}</span>
      <EnvMark v-if="tier.env !== 'paper'" :env="tier.env" class="order-env" />
    </template>
    <a-alert v-if="tier.banner" :type="tier.env === 'prod' ? 'error' : 'warning'" class="section">{{ tier.banner }}</a-alert>
    <a-alert v-if="capabilities && !capabilities.can_place_order" type="error" class="section">
      {{ capabilities.unavailable_reason || "当前账户不可下单" }}
    </a-alert>
    <a-alert v-if="logical.control_mode === 1" type="warning" class="section">
      这是策略驱动的组合，提交前会暂停整个组合账户并取消活动目标订单，恢复前策略目标不会执行。
    </a-alert>
    <a-alert v-if="submissionError" type="warning" class="section">{{ submissionError }}</a-alert>
    <a-form :model="manualForm" auto-label-width :disabled="!!pendingSubmission">
      <a-form-item v-if="memberOptions.length > 1" label="执行账户" required>
        <a-select v-model="memberId" @change="loadMember">
          <a-option v-for="item in memberOptions" :key="item.id" :value="item.id">{{ item.label }}</a-option>
        </a-select>
      </a-form-item>
      <a-form-item label="交易标的" required :extra="instrumentRuleHint(selectedInstrument)">
        <a-select v-model="manualForm.instrument_id" allow-search placeholder="搜索交易标的">
          <a-option v-for="item in instruments" :key="item.instrument_id" :value="item.instrument_id">
            {{ instrumentName(item.instrument_id) }} · {{ marketLabel(marketOfInstrument(item.instrument_id)) }}
          </a-option>
        </a-select>
      </a-form-item>
      <a-form-item label="方向" required>
        <a-radio-group v-model="manualForm.side" type="button">
          <a-radio :value="1">买入</a-radio><a-radio :value="2">卖出</a-radio>
        </a-radio-group>
      </a-form-item>
      <a-form-item label="订单类型">
        <a-radio-group v-model="manualForm.order_type" type="button">
          <a-radio v-if="capabilities?.order_types?.includes(1)" :value="1">市价</a-radio>
          <a-radio v-if="capabilities?.order_types?.includes(2)" :value="2">限价</a-radio>
        </a-radio-group>
      </a-form-item>
      <a-form-item v-if="manualForm.order_type === 2" label="成交策略">
        <a-select v-model="manualForm.fill_policy">
          <a-option v-for="policy in capabilities?.fill_policies || [1, 2, 3]" :key="policy" :value="policy">{{
            ["", "一直有效（GTC）", "立即成交（IOC）", "全部成交（FOK）"][policy]
          }}</a-option>
        </a-select>
      </a-form-item>
      <a-form-item label="数量" required><a-input v-model="manualForm.quantity" /></a-form-item>
      <a-form-item v-if="manualForm.order_type === 2" label="限价" required>
        <a-input v-model="manualForm.limit_price" />
      </a-form-item>
      <a-form-item label="原因" required><a-input v-model="manualForm.reason" /></a-form-item>
      <a-form-item label="提交截止时间"><a-input v-model="manualForm.deadline_at" type="datetime-local" /></a-form-item>
    </a-form>
    <a-form v-if="tier.requiresName" :model="{}" layout="vertical">
      <a-form-item :label="`输入组合名称「${logical.name}」确认`">
        <a-input v-model="typedName" autocomplete="off" />
      </a-form-item>
    </a-form>
  </a-modal>
</template>

<script setup lang="ts">
import { computed, reactive, ref, watch } from "vue";
import { Message } from "@arco-design/web-vue";
import {
  getExecutionCapabilities,
  listTradableInstruments,
  placeManualOrder,
  submitOrder,
  TradeResponseError
} from "@/api/trade";
import type { ExecutionCapabilities, LogicalAccount, SubmitOrderReq, TradableInstrument, TradingAccount } from "@/api/trade";
import { createClientId } from "@/utils/client-id";
import { createLatestRequestGuard } from "@/utils/latest-request";
import EnvMark from "../shared/env-mark.vue";
import { confirmTier, nameConfirmed, type Environment } from "../shared/environment";
import { instrumentName, marketLabel, marketOfInstrument } from "../shared/markets";
import { instrumentRuleHint, orderRuleError } from "../shared/order-rules";
import type { ActionTracker } from "./action-tracker";

defineOptions({ name: "trading-order-dialog" });

const props = defineProps<{
  logical: LogicalAccount;
  accounts: ReadonlyMap<string, TradingAccount>;
  env: Environment;
  tracker: ActionTracker;
}>();
const emit = defineEmits<{ submitted: [] }>();

const visible = ref(false);
const submitting = ref(false);
const submissionError = ref("");
const typedName = ref("");
const memberId = ref("");
const capabilities = ref<ExecutionCapabilities | null>(null);
const instruments = ref<TradableInstrument[]>([]);
const pendingSubmission = ref<{ mode: 1 | 2; request: SubmitOrderReq } | null>(null);
const capabilityRequests = createLatestRequestGuard();
const manualForm = reactive({
  action_id: "",
  trading_account_id: "",
  client_order_id: "",
  instrument_id: "",
  order_type: 1 as 1 | 2,
  fill_policy: 0 as 0 | 1 | 2 | 3,
  side: 1 as 1 | 2,
  quantity: "",
  limit_price: "",
  reason: "",
  deadline_at: ""
});

const tier = computed(() => confirmTier(props.env));
const nameOk = computed(() => nameConfirmed(tier.value, typedName.value, props.logical.name));
const orderEntryLabel = computed(() => (props.logical.control_mode === 2 ? "下单" : "接管并下单"));
const selectedInstrument = computed(() => instruments.value.find(item => item.instrument_id === manualForm.instrument_id));
const memberOptions = computed(() =>
  (props.logical.members || [])
    .filter(member => member.enabled)
    .map(member => ({
      id: member.trading_account_id,
      label: props.accounts.get(member.trading_account_id)?.name || member.trading_account_id
    }))
);

watch(
  () => manualForm.order_type,
  type => {
    if (type === 2 && !manualForm.fill_policy) manualForm.fill_policy = 1;
  }
);
// 关闭或切换组合时放弃未确认的请求之外的草稿状态：结果不明的请求保留，不能丢掉它的编号
watch(
  () => props.logical.logical_account_id,
  () => {
    visible.value = false;
    pendingSubmission.value = null;
    submissionError.value = "";
  }
);

function loadMember() {
  const accountId = memberId.value;
  manualForm.trading_account_id = accountId;
  manualForm.instrument_id = "";
  capabilities.value = null;
  instruments.value = [];
  const request = capabilityRequests.begin();
  getExecutionCapabilities(accountId)
    .then(response => {
      if (request.isLatest()) capabilities.value = response.capabilities;
    })
    .catch(() => {
      if (request.isLatest()) capabilities.value = null;
    });
  listTradableInstruments(accountId)
    .then(response => {
      if (request.isLatest()) instruments.value = response.instruments || [];
    })
    .catch(() => {
      if (request.isLatest()) instruments.value = [];
    });
}

/** 打开下单对话框。有结果不明的旧请求时，只能对同一个执行账户重试原请求。 */
function openManual(accountId: string, instrumentId = "") {
  if (![1, 2].includes(props.logical.control_mode)) return;
  if (pendingSubmission.value) {
    const pending = pendingSubmission.value.request;
    if (pending.trading_account_id !== accountId || pending.logical_account_id !== props.logical.logical_account_id) {
      Message.warning("另一个账户的请求仍待确认，请先查询原操作");
      return;
    }
    visible.value = true;
    props.tracker.start(pending.action_id, observed => {
      if (pendingSubmission.value?.request.action_id === observed.action_id) {
        pendingSubmission.value = null;
        visible.value = false;
      }
    });
    return;
  }
  submissionError.value = "";
  typedName.value = "";
  props.tracker.reset();
  Object.assign(manualForm, {
    action_id: createClientId(),
    client_order_id: createClientId().replace(/-/g, ""),
    order_type: 1,
    fill_policy: 0,
    side: 1,
    quantity: "",
    limit_price: "",
    reason: "",
    deadline_at: ""
  });
  memberId.value = accountId;
  loadMember();
  manualForm.instrument_id = instrumentId;
  visible.value = true;
}

/** 从页头进入：只有一个启用成员就直接用它，否则用第一个。 */
function open(instrumentId = "", accountId = "") {
  const first = accountId || memberOptions.value[0]?.id;
  if (!first) {
    Message.warning("组合里没有启用的执行账户");
    return;
  }
  openManual(first, instrumentId);
}

async function submitManual() {
  if (submitting.value || ![1, 2].includes(props.logical.control_mode)) return false;
  if (
    !manualForm.action_id.trim() ||
    !manualForm.client_order_id.trim() ||
    !manualForm.instrument_id.trim() ||
    !manualForm.quantity.trim() ||
    !manualForm.reason.trim() ||
    (manualForm.order_type === 2 && !manualForm.limit_price.trim())
  ) {
    Message.warning("请填写所有必填字段");
    return false;
  }
  if (!pendingSubmission.value && !capabilities.value?.can_place_order) {
    Message.warning(capabilities.value?.unavailable_reason || "账户下单能力尚未确认");
    return false;
  }
  if (!pendingSubmission.value) {
    const limitPrice = manualForm.order_type === 2 ? manualForm.limit_price : undefined;
    const ruleError = orderRuleError(manualForm.quantity, limitPrice, selectedInstrument.value);
    if (ruleError) {
      Message.warning(ruleError);
      return false;
    }
    const deadline = manualForm.deadline_at ? new Date(manualForm.deadline_at).getTime() : undefined;
    if (deadline !== undefined && (!Number.isFinite(deadline) || deadline <= Date.now())) {
      Message.warning("提交截止时间必须晚于当前时间");
      return false;
    }
    const instrumentId = manualForm.instrument_id.trim().toUpperCase();
    const market = marketOfInstrument(instrumentId);
    pendingSubmission.value = {
      mode: props.logical.control_mode as 1 | 2,
      request: {
        ...manualForm,
        logical_account_id: props.logical.logical_account_id,
        action_id: manualForm.action_id.trim(),
        client_order_id: manualForm.client_order_id.trim(),
        instrument_id: instrumentId,
        market_type: market,
        // 只有永续有持仓方向，现货不传
        position_side: market === 2 ? 1 : undefined,
        quantity: manualForm.quantity.trim(),
        fill_policy: manualForm.order_type === 1 ? 0 : manualForm.fill_policy,
        limit_price: manualForm.order_type === 2 ? manualForm.limit_price.trim() : undefined,
        reason: manualForm.reason.trim(),
        deadline_at: deadline === undefined ? undefined : String(deadline)
      }
    };
  }
  const pending = pendingSubmission.value;
  const tracker = props.tracker;
  submitting.value = true;
  submissionError.value = "";
  try {
    const { logical_account_id: logicalId, ...manualRequest } = pending.request;
    const rsp = pending.mode === 2 ? await submitOrder(pending.request) : await placeManualOrder(manualRequest);
    if (props.logical.logical_account_id !== logicalId) return true;
    pendingSubmission.value = null;
    tracker.action.value = rsp.action;
    tracker.submittedOrder.value = rsp.order?.order_id ? rsp.order : null;
    visible.value = false;
    tracker.start(rsp.action.action_id);
    emit("submitted");
    return true;
  } catch (error) {
    if (props.logical.logical_account_id !== pending.request.logical_account_id) return false;
    submissionError.value = error instanceof Error ? error.message : "请求未确认";
    if (error instanceof TradeResponseError) {
      if (error.response.action?.action_id === pending.request.action_id) {
        tracker.action.value = error.response.action;
        tracker.submittedOrder.value = error.response.order?.order_id ? error.response.order : null;
        pendingSubmission.value = null;
        visible.value = false;
        emit("submitted");
      } else if (
        !error.response.action &&
        [
          "1",
          "2",
          "3",
          "5",
          "14",
          "INVALID_PARAM",
          "NO_AUTH",
          "NO_PERMISSION",
          "NOT_FOUND",
          "SPACE_NOT_FOUND",
          "CONFLICT"
        ].includes(String(error.response.ret_info.code))
      ) {
        // 只有明确的受理前错误才允许修改后重填；编号保留。缺失、系统错误和传输错误都可能已被受理。
        pendingSubmission.value = null;
        tracker.stop();
        return false;
      }
    }
    tracker.start(pending.request.action_id, observed => {
      if (pendingSubmission.value?.request.action_id === observed.action_id) {
        pendingSubmission.value = null;
        visible.value = false;
      }
    });
    return false;
  } finally {
    submitting.value = false;
  }
}

defineExpose({ open, openManual, submitManual, manualForm, visible, pendingSubmission });
</script>

<style src="../shared/accounts.css"></style>
<style scoped>
.section {
  margin-bottom: var(--moox-space-3);
}

.order-env {
  margin-left: var(--moox-space-3);
  font-size: 13px;
}
</style>
