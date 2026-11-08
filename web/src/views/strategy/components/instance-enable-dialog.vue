<template>
  <a-modal
    :visible="visible"
    width="min(560px, calc(100vw - 24px))"
    ok-text="确认启用"
    cancel-text="取消"
    :ok-button-props="{ status: 'warning', disabled: !canSubmit }"
    :on-before-ok="handleOk"
    unmount-on-close
    @cancel="onCancel"
  >
    <template #title>
      <span>启用实例「{{ instanceId }}」</span>
      <EnvMark v-if="logical && env !== 'paper'" :env="env" class="title-env" />
    </template>

    <p v-if="loading" class="muted">正在读取组合账户…</p>
    <template v-else-if="loadError">
      <a-alert type="error" class="block">组合账户读取失败：{{ loadError }}。读到账户之前不能启用。</a-alert>
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
        <a-descriptions-item label="账户状态">{{ logical.ready ? "就绪" : "未就绪" }}</a-descriptions-item>
        <a-descriptions-item label="计算任务">{{ factorTaskText }}</a-descriptions-item>
      </a-descriptions>
      <p class="line">
        启用会固化会话快照，并认领这个组合账户的会话（会撤掉上一个会话留下的目标挂单）：之后的策略目标由它执行下单。
      </p>
      <p class="line">计算任务 {{ factorTaskText }} 会与本实例永久关联（已关联的不变），实例不能删除。</p>
      <p v-if="paused" class="line">组合账户已暂停自动执行：启用后不会下单，直到在交易页恢复自动执行。</p>
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
import { getLogicalAccount, listTradingAccounts } from "@/api/trade";
import type { LogicalAccount, TradingAccount } from "@/api/trade/types";
import EnvMark from "@/views/trading/accounts/shared/env-mark.vue";
import {
  confirmTier,
  environmentLabel,
  logicalEnvironment,
  nameConfirmed,
  type Environment
} from "@/views/trading/accounts/shared/environment";
import { TOGGLE_CANCELED_TIP, toggleInstance, type EnableTarget, type InstanceToggleHooks } from "./instance-toggle";

const visible = ref(false);
const instanceId = ref("");
const logicalId = ref("");
const factorTasks = ref<string[]>([]);
const logical = ref<LogicalAccount | null>(null);
const accounts = ref<ReadonlyMap<string, TradingAccount>>(new Map());
const loading = ref(false);
const loadError = ref("");
const typedName = ref("");
let hooks: InstanceToggleHooks = {};
let loadSeq = 0;

const env = computed<Environment>(() => (logical.value ? logicalEnvironment(logical.value, accounts.value) : "prod"));
const tier = computed(() => confirmTier(env.value));
const comboName = computed(() => logical.value?.name || logicalId.value);
const paused = computed(() => logical.value?.automation_state !== "ACTIVE");
const factorTaskText = computed(() => factorTasks.value.join("、") || "（未绑定）");
// 没读到账户时不能确认：不能把读取失败当成模拟盘放行。
const canSubmit = computed(
  () => Boolean(logical.value) && !loading.value && nameConfirmed(tier.value, typedName.value, comboName.value)
);

async function load() {
  const seq = ++loadSeq;
  loading.value = true;
  loadError.value = "";
  logical.value = null;
  try {
    const [logicalRsp, accountsRsp] = await Promise.all([
      getLogicalAccount(logicalId.value),
      listTradingAccounts({ page: { page: 1, size: 200 } })
    ]);
    if (seq !== loadSeq) return;
    accounts.value = new Map((accountsRsp.accounts || []).map(item => [item.trading_account_id, item]));
    logical.value = logicalRsp.logical_account;
  } catch (error) {
    if (seq === loadSeq) loadError.value = error instanceof Error && error.message ? error.message : "请稍后重试";
  } finally {
    if (seq === loadSeq) loading.value = false;
  }
}

/** 打开启用确认：先读组合账户和执行账户，算出环境再让用户确认。 */
function open(target: EnableTarget, toggleHooks: InstanceToggleHooks = {}) {
  hooks = toggleHooks;
  instanceId.value = target.instance_id;
  logicalId.value = target.logical_account_id;
  factorTasks.value = [...new Set(Object.values(target.factor_tasks || {}).filter(Boolean))];
  typedName.value = "";
  visible.value = true;
  void load();
}

function handleOk() {
  if (!canSubmit.value) return false;
  visible.value = false;
  void toggleInstance(instanceId.value, true, hooks);
  return true;
}

function onCancel() {
  loadSeq += 1;
  visible.value = false;
  Message.info(TOGGLE_CANCELED_TIP);
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

.muted {
  color: var(--color-text-3);
}
</style>
