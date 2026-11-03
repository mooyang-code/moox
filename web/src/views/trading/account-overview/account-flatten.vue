<template>
  <div class="account-flatten">
    <a-button status="danger" @click="open">平仓该账户</a-button>
    <a-alert v-if="action" :type="alertType" show-icon class="flatten-status">
      {{ statusText }}
      <div v-for="item in remaining" :key="(item.instrument_id || '') + (item.asset || '')" class="flatten-line">
        剩余 {{ item.instrument_id || item.asset }}：{{ item.quantity }}（{{ item.reason }}）
      </div>
      <div v-if="accountResult?.error" class="flatten-line">{{ accountResult.error }}</div>
    </a-alert>
    <a-modal
      v-model:visible="visible"
      title="平仓该执行账户"
      :ok-loading="submitting"
      ok-text="确认平仓"
      :ok-button-props="{ status: 'danger' }"
      :on-before-ok="submit"
    >
      <p>将撤销该账户的挂单并市价平掉它的全部仓位，所在组合账户「{{ logicalName }}」会被暂停自动执行，平仓后需手动恢复。</p>
      <a-textarea v-model="reason" placeholder="平仓原因（必填）" :max-length="200" />
      <a-alert v-if="dialogError" type="error" class="flatten-status">{{ dialogError }}</a-alert>
    </a-modal>
  </div>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from "vue";
import { Message } from "@arco-design/web-vue";
import { flattenTradingAccount, getOperatorAction, parseFlattenResult, type OperatorAction } from "@/api/trade";
import { createClientId } from "@/utils/client-id";
import { flattenStatusText } from "./account-flatten";

const props = defineProps<{ tradingAccountId: string; logicalName: string }>();

const visible = ref(false);
const reason = ref("");
const actionId = ref("");
const submitting = ref(false);
const dialogError = ref("");
const action = ref<OperatorAction | null>(null);
let poller: ReturnType<typeof setInterval> | undefined;

const accountResult = computed(() =>
  parseFlattenResult(action.value || undefined)?.accounts.find(item => item.trading_account_id === props.tradingAccountId)
);
const remaining = computed(() => accountResult.value?.remaining_positions || []);
const statusText = computed(() => flattenStatusText(action.value?.status || ""));
const alertType = computed(() => {
  switch (action.value?.status) {
    case "COMPLETED":
      return "success";
    case "RUNNING":
      return "info";
    default:
      return "warning";
  }
});

function open() {
  reason.value = "";
  dialogError.value = "";
  // 每次打开生成新的操作编号；同一次弹窗内重试复用它，保证幂等。
  actionId.value = createClientId();
  visible.value = true;
}

async function submit() {
  if (!reason.value.trim()) {
    dialogError.value = "请填写平仓原因";
    return false;
  }
  submitting.value = true;
  dialogError.value = "";
  try {
    const rsp = await flattenTradingAccount(actionId.value, props.tradingAccountId, reason.value.trim());
    action.value = rsp.action;
    if (rsp.action.action_id !== actionId.value) Message.info("该组合账户已有正在执行的平仓，显示的是那次平仓的进度");
    startPolling(rsp.action.action_id);
    return true;
  } catch (error) {
    // 接口报错时平仓可能已经在后台继续（例如某个账户暂时异常、网关超时）：按操作编号查一次，查到就显示进度。
    const message = error instanceof Error ? error.message : String(error);
    const recovered = await recoverAction(actionId.value);
    if (recovered) {
      // 动作已经结束（例如失败）时，把接口返回的原因也显示出来。
      if (action.value?.status !== "RUNNING") Message.warning(message.length > 200 ? `${message.slice(0, 200)}…` : message);
      return true;
    }
    dialogError.value = message;
    return false;
  } finally {
    submitting.value = false;
  }
}

async function recoverAction(id: string): Promise<boolean> {
  try {
    const rsp = await getOperatorAction(id);
    action.value = rsp.action;
    startPolling(id);
    return true;
  } catch {
    return false;
  }
}

function stopPolling() {
  if (poller) clearInterval(poller);
  poller = undefined;
}

function startPolling(id: string) {
  stopPolling();
  if (action.value?.status !== "RUNNING") return;
  const accountId = props.tradingAccountId;
  poller = setInterval(async () => {
    try {
      const rsp = await getOperatorAction(id);
      if (props.tradingAccountId !== accountId) return;
      action.value = rsp.action;
      if (rsp.action.status !== "RUNNING") stopPolling();
    } catch {
      // 查询失败下一轮再试。
    }
  }, 2000);
}

watch(
  () => props.tradingAccountId,
  () => {
    stopPolling();
    action.value = null;
  }
);

onBeforeUnmount(stopPolling);
</script>

<style scoped>
.flatten-status {
  margin-top: var(--moox-space-2);
}

.flatten-line {
  margin-top: 4px;
}
</style>
