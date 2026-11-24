<template>
  <a-modal
    :visible="visible"
    width="min(720px, calc(100vw - 24px))"
    ok-text="确认实盘"
    cancel-text="取消"
    :ok-button-props="{ status: 'warning', disabled: !canSubmit }"
    :ok-loading="submitting"
    :on-before-ok="handleOk"
    unmount-on-close
    @cancel="onCancel"
  >
    <template #title>
      <span>确认实盘「{{ instanceId }}」</span>
      <EnvMark v-if="logical && env !== 'paper'" :env="env" class="title-env" />
    </template>

    <p v-if="loading" class="muted">正在读取组合账户和影子目标…</p>
    <template v-else-if="loadError">
      <a-alert type="error" class="block">读取失败：{{ loadError }}。读到组合账户和影子目标之前不能确认。</a-alert>
      <a-button size="small" @click="load">重试</a-button>
    </template>
    <template v-else-if="logical">
      <a-alert v-if="tier.banner" :type="env === 'prod' ? 'error' : 'warning'" class="block">{{ tier.banner }}</a-alert>
      <a-descriptions :column="1" size="small" :label-style="{ width: '88px' }" class="block">
        <a-descriptions-item label="组合账户">{{ comboName }}</a-descriptions-item>
        <a-descriptions-item label="环境">{{ environmentLabel(env) }}</a-descriptions-item>
        <a-descriptions-item label="自动执行">{{
          paused ? `已暂停（${logical.pause_reason || "手动暂停"}）` : "运行中"
        }}</a-descriptions-item>
      </a-descriptions>
      <p class="line">{{ CONFIRM_COPY.effect }}</p>
      <p v-if="paused" class="line">组合账户已暂停自动执行：确认后也不会下单，直到在交易页恢复自动执行。</p>
      <div class="section-title">本次启用最近的影子目标（只换算、未下单）</div>
      <a-table
        v-if="targets.length"
        :data="targets"
        :pagination="false"
        size="small"
        row-key="target_id"
        :scroll="{ y: 260 }"
        class="block"
      >
        <template #columns>
          <a-table-column title="周期" :width="150"
            ><template #cell="{ record }">{{
              formatInstant(record.bar_end_time, displayZone, "minute")
            }}</template></a-table-column
          >
          <a-table-column title="组合权益" :width="110"
            ><template #cell="{ record }">{{ record.equity || "-" }}</template></a-table-column
          >
          <a-table-column title="目标（权重与换算数量）"
            ><template #cell="{ record }">{{ shadowTargetSummary(record) }}</template></a-table-column
          >
        </template>
      </a-table>
      <p v-else class="muted block">{{ CONFIRM_COPY.noTargets }}</p>
      <a-alert v-if="submitError" type="error" class="block">确认失败：{{ submitError }}</a-alert>
      <a-form v-if="tier.requiresName" :model="{}" layout="vertical" class="block">
        <a-form-item :label="`输入组合名称「${comboName}」确认`">
          <a-input v-model="typedName" autocomplete="off" />
        </a-form-item>
      </a-form>
    </template>
  </a-modal>
</template>

<script setup lang="ts">
import { computed, ref } from "vue";
import { Message } from "@arco-design/web-vue";
import { confirmInstance } from "@/api/strategy";
import type { StrategyInstance } from "@/api/strategy-types";
import { getLogicalAccount, listShadowTargets, listTradingAccounts } from "@/api/trade";
import type { LogicalAccount, ShadowTarget, TradingAccount } from "@/api/trade/types";
import EnvMark from "@/components/trade-confirm/env-mark.vue";
import {
  confirmTier,
  environmentLabel,
  logicalEnvironment,
  nameConfirmed,
  type Environment
} from "@/components/trade-confirm/environment";
import { useDisplayZone } from "@/hooks/useDisplayZone";
import { formatInstant } from "@/utils/time-zone";
import { CONFIRM_COPY, currentSessionTargets, shadowTargetSummary, type ConfirmHooks } from "./instance-confirm";

const displayZone = useDisplayZone();
const visible = ref(false);
const instanceId = ref("");
const sessionId = ref("");
const logicalId = ref("");
const logical = ref<LogicalAccount | null>(null);
const accounts = ref<ReadonlyMap<string, TradingAccount>>(new Map());
const targets = ref<ShadowTarget[]>([]);
const loading = ref(false);
const loadError = ref("");
const submitting = ref(false);
const submitError = ref("");
const typedName = ref("");
let hooks: ConfirmHooks = {};
let loadSeq = 0;

const env = computed<Environment>(() => (logical.value ? logicalEnvironment(logical.value, accounts.value) : "prod"));
const tier = computed(() => confirmTier(env.value));
const comboName = computed(() => logical.value?.name || logicalId.value);
const paused = computed(() => logical.value?.automation_state !== "ACTIVE");
// 读不到组合账户或影子目标时不能确认：不能把读取失败当成模拟盘或“没有影子目标”放行。
const canSubmit = computed(
  () =>
    Boolean(logical.value) &&
    !loading.value &&
    !loadError.value &&
    !submitting.value &&
    nameConfirmed(tier.value, typedName.value, comboName.value)
);

async function load() {
  const seq = ++loadSeq;
  loading.value = true;
  loadError.value = "";
  logical.value = null;
  targets.value = [];
  try {
    const [logicalRsp, accountsRsp, shadowRsp] = await Promise.all([
      getLogicalAccount(logicalId.value),
      listTradingAccounts({ page: { page: 1, size: 200 } }),
      listShadowTargets(instanceId.value, 20)
    ]);
    if (seq !== loadSeq) return;
    accounts.value = new Map((accountsRsp.accounts || []).map(item => [item.trading_account_id, item]));
    targets.value = currentSessionTargets(shadowRsp.targets || [], sessionId.value);
    logical.value = logicalRsp.logical_account;
  } catch (error) {
    if (seq === loadSeq) loadError.value = error instanceof Error && error.message ? error.message : "请稍后重试";
  } finally {
    if (seq === loadSeq) loading.value = false;
  }
}

/** 打开确认实盘：先读组合账户、执行账户和本次启用的影子目标，算出环境再让用户确认。 */
function open(
  instance: Pick<StrategyInstance, "instance_id" | "session_id" | "logical_account_id">,
  confirmHooks: ConfirmHooks = {}
) {
  hooks = confirmHooks;
  instanceId.value = instance.instance_id;
  sessionId.value = instance.session_id;
  logicalId.value = instance.logical_account_id;
  typedName.value = "";
  submitError.value = "";
  submitting.value = false;
  visible.value = true;
  void load();
}

/** 发确认请求：成功才关闭；失败（例如策略池预热没完成、实例已重新启用）把原因留在对话框里。 */
async function handleOk() {
  if (!canSubmit.value) return false;
  submitting.value = true;
  submitError.value = "";
  try {
    await confirmInstance(instanceId.value, sessionId.value);
  } catch (error) {
    submitError.value = error instanceof Error && error.message ? error.message : "请稍后重试";
    return false;
  } finally {
    submitting.value = false;
  }
  visible.value = false;
  Message.success(CONFIRM_COPY.done);
  hooks.onSettled?.(true);
  return true;
}

function onCancel() {
  loadSeq += 1;
  visible.value = false;
  Message.info(CONFIRM_COPY.canceled);
  if (submitError.value) hooks.onSettled?.(false);
}

defineExpose({ open });
</script>

<style scoped>
.title-env {
  margin-left: var(--moox-space-3);
  font-size: 13px;
}

.block {
  margin-bottom: var(--moox-space-3);
}

.line {
  margin: 0 0 var(--moox-space-2);
}

.section-title {
  margin: var(--moox-space-3) 0 var(--moox-space-2);
  color: var(--color-text-2);
  font-size: 13px;
}

.muted {
  color: var(--color-text-3);
}
</style>
