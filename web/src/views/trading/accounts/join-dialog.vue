<template>
  <a-modal v-model:visible="visible" width="min(560px, calc(100vw - 24px))" :footer="false" unmount-on-close>
    <template #title>
      <span>把「{{ joining?.name }}」加入组合</span>
      <EnvMark v-if="env !== 'paper'" :env="env" class="title-env" />
    </template>

    <template v-if="joining">
      <a-alert v-if="error" type="error" class="block">{{ error }}</a-alert>
      <a-form :model="joinForm" layout="vertical">
        <a-form-item
          label="加入哪个组合"
          :extra="options.length ? '只列出环境和结算资产一致、已暂停自动执行的组合；运行中的组合先去暂停' : ''"
        >
          <a-select v-model="joinForm.logicalId" placeholder="选择组合">
            <a-option v-for="item in options" :key="item.logical_account_id" :value="item.logical_account_id">{{
              item.name
            }}</a-option>
          </a-select>
          <template v-if="!options.length" #extra
            ><span class="tx-neg">没有可以加入的组合，先新建一个或暂停运行中的组合</span></template
          >
        </a-form-item>
        <a-form-item label="优先级" extra="数字越小越先使用">
          <a-input-number v-model="joinForm.priority" :min="0" />
        </a-form-item>
        <a-form-item :extra="ADOPT_EFFECT_TEXT">
          <a-checkbox v-model="joinForm.adopt">接管账户里已有的持仓</a-checkbox>
        </a-form-item>
      </a-form>
      <div class="footer">
        <a-button @click="visible = false">取消</a-button>
        <a-button type="primary" :loading="saving" :disabled="!joinForm.logicalId" @click="requestJoin">加入</a-button>
      </div>
    </template>

    <ConfirmDialog
      v-model:visible="confirmVisible"
      :title="pending?.title ?? ''"
      :env="env"
      :combo-name="pending?.name ?? ''"
      name-label="组合名称"
      :need-reason="false"
      :name-required="pending?.nameRequired ?? true"
      :danger="pending?.danger ?? false"
      :confirm-text="pending?.confirmText"
      :submit="join"
    >
      <p v-for="line in pending?.lines ?? []" :key="line" class="confirm-line">{{ line }}</p>
    </ConfirmDialog>
  </a-modal>
</template>

<script setup lang="ts">
import { computed, reactive, ref, watch } from "vue";
import { Message } from "@arco-design/web-vue";
import { addLogicalAccountMember } from "@/api/trade";
import type { LogicalAccount, TradingAccount } from "@/api/trade";
import { createSubmitScope } from "@/utils/submit-scope";
import { accountMapOf } from "./account-list";
import ConfirmDialog from "@/components/trade-confirm/confirm-dialog.vue";
import EnvMark from "@/components/trade-confirm/env-mark.vue";
import type { Environment } from "@/components/trade-confirm/environment";
import { joinableLogicals, nextPriority } from "./shared/membership";
import { ADOPT_EFFECT_TEXT, memberChangeConfirm, type RiskyConfirm } from "./shared/risky-actions";

/** 把一个未加入组合的执行账户加入组合；从执行账户子页签的行上打开。 */
const props = defineProps<{
  env: Environment;
  logicals: LogicalAccount[];
  accounts: TradingAccount[];
}>();
const emit = defineEmits<{ changed: [] }>();

const visible = ref(false);
const joining = ref<TradingAccount | null>(null);
const joinForm = reactive({ logicalId: "", priority: 1, adopt: false });
const saving = ref(false);
const error = ref("");
const scope = createSubmitScope();
// 测试网、生产环境下接管已有持仓要先确认，模拟盘直接加入
const confirmVisible = ref(false);
const pending = ref<(RiskyConfirm & { name: string }) | null>(null);

const options = computed(() =>
  joining.value ? joinableLogicals(props.logicals, accountMapOf(props.accounts), props.env, joining.value.settlement_asset) : []
);

watch(visible, () => scope.invalidate());

function open(account: TradingAccount) {
  joining.value = account;
  const first = options.value[0];
  Object.assign(joinForm, {
    logicalId: first?.logical_account_id ?? "",
    priority: first ? nextPriority(first) : 1,
    adopt: false
  });
  error.value = "";
  visible.value = true;
}

/** 勾了接管已有持仓时先确认（模拟盘直接加入）；否则直接加入。 */
function requestJoin() {
  if (!joining.value || !joinForm.logicalId) return;
  const target = options.value.find(item => item.logical_account_id === joinForm.logicalId);
  const confirm = joinForm.adopt
    ? memberChangeConfirm({
        accountName: joining.value.name,
        comboName: target?.name ?? joinForm.logicalId,
        original: null,
        next: { enabled: true, priority: joinForm.priority },
        adopt: true
      })
    : null;
  if (!confirm || props.env === "paper") {
    void join();
    return;
  }
  pending.value = { ...confirm, name: target?.name ?? joinForm.logicalId };
  confirmVisible.value = true;
}

/** 确认框里只关掉确认框；加入失败的原因显示在加入表单上。 */
async function join() {
  if (!joining.value || !joinForm.logicalId) return;
  const submission = scope.begin();
  saving.value = true;
  error.value = "";
  try {
    await addLogicalAccountMember({
      logical_account_id: joinForm.logicalId,
      trading_account_id: joining.value.trading_account_id,
      enabled: true,
      priority: joinForm.priority,
      adopt_existing_exposure: joinForm.adopt
    });
  } catch (cause) {
    if (submission.isCurrent()) error.value = cause instanceof Error ? cause.message : "请求失败，请稍后重试";
    saving.value = false;
    return;
  }
  saving.value = false;
  if (!submission.isCurrent()) return;
  Message.success("已加入组合");
  visible.value = false;
  emit("changed");
}

defineExpose({ open });
</script>

<style src="./shared/accounts.css"></style>
<style scoped>
.title-env {
  margin-left: var(--moox-space-3);
  font-size: 13px;
}

.block {
  margin-bottom: var(--moox-space-3);
}

.confirm-line {
  margin: 0 0 var(--moox-space-2);
}

.footer {
  display: flex;
  justify-content: flex-end;
  gap: var(--moox-space-2);
  margin-top: var(--moox-space-4);
}
</style>
