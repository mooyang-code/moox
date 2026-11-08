<template>
  <a-modal
    v-model:visible="visible"
    width="min(520px, calc(100vw - 24px))"
    :title="editing ? `编辑「${editingName}」在组合里的设置` : '添加执行账户'"
    :on-before-ok="save"
    unmount-on-close
  >
    <a-alert v-if="error" type="error" class="block">{{ error }}</a-alert>
    <a-form :model="form" layout="vertical">
      <a-form-item
        v-if="!editing"
        label="执行账户"
        required
        :extra="candidates.length ? '只列出环境、结算资产都和组合一致、已启用且不在任何组合里的账户' : ''"
      >
        <a-select v-model="form.trading_account_id" allow-search placeholder="选择执行账户">
          <a-option v-for="item in candidates" :key="item.trading_account_id" :value="item.trading_account_id">
            {{ item.name }}（{{ exchangeLabels[item.exchange] || "-" }} · {{ marketsText(item.markets) }}）
          </a-option>
        </a-select>
        <template v-if="!candidates.length" #extra
          ><span class="tx-neg">没有能加入这个组合的执行账户，</span
          ><a-link @click="openExecutionList">{{
            env === "paper" ? "去执行账户页签新建一个" : "去执行账户页签新建或启用一个"
          }}</a-link></template
        >
      </a-form-item>
      <a-form-item label="优先级" extra="数字越小越先使用，当前账户资金用完才用下一个账户">
        <a-input-number v-model="form.priority" :min="0" />
      </a-form-item>
      <a-form-item v-if="editing" label="启用" extra="停用后策略不再用它下单">
        <a-switch v-model="form.enabled" />
      </a-form-item>
      <a-form-item v-if="!editing || adoptionNeeded" :extra="ADOPT_EFFECT_TEXT">
        <a-checkbox v-model="form.adopt_existing_exposure">接管账户里已有的持仓</a-checkbox>
      </a-form-item>
    </a-form>

    <ConfirmDialog
      v-model:visible="confirmVisible"
      :title="pending?.title ?? ''"
      :env="env"
      :combo-name="logical.name"
      :need-reason="false"
      :name-required="pending?.nameRequired ?? true"
      :danger="pending?.danger ?? false"
      :confirm-text="pending?.confirmText"
      :submit="runPending"
    >
      <p v-for="line in pending?.lines ?? []" :key="line" class="confirm-line">{{ line }}</p>
    </ConfirmDialog>
  </a-modal>
</template>

<script setup lang="ts">
import { reactive, ref } from "vue";
import { useRouter } from "vue-router";
import { Message } from "@arco-design/web-vue";
import { addLogicalAccountMember, exchangeLabels, isTradeAdoptionRequired } from "@/api/trade";
import type { LogicalAccount, TradingAccount } from "@/api/trade";
import { createSubmitScope } from "@/utils/submit-scope";
import ConfirmDialog from "@/components/trade-confirm/confirm-dialog.vue";
import type { Environment } from "@/components/trade-confirm/environment";
import { marketsText } from "../shared/markets";
import { nextPriority } from "../shared/membership";
import { ADOPT_EFFECT_TEXT, memberChangeConfirm, type MemberSettings, type RiskyConfirm } from "../shared/risky-actions";

const props = defineProps<{ logical: LogicalAccount; candidates: TradingAccount[]; env: Environment }>();
const router = useRouter();

function openExecutionList() {
  visible.value = false;
  void router.push({
    path: "/trading/accounts",
    query: { env: props.env === "paper" ? undefined : props.env, list: "execution" }
  });
}
const emit = defineEmits<{ saved: [] }>();

const visible = ref(false);
const editing = ref(false);
const editingName = ref("");
const error = ref("");
// 重新启用已停用的成员时，账户上已有持仓或挂单，后端要求确认接管（TRADE_ADOPTION_REQUIRED）：这时才在编辑里显示“接管”勾选。
const adoptionNeeded = ref(false);
const form = reactive({ trading_account_id: "", enabled: true, priority: 1, adopt_existing_exposure: false });
// 打开和关闭都使进行中的保存失效：迟到的响应不能关闭用户重新打开的对话框。
const scope = createSubmitScope();
// 编辑时打开对话框那一刻的设置，用来判断改了什么；新增时为空。
const original = ref<MemberSettings | null>(null);
// 测试网、生产环境下改启停、优先级或接管已有持仓要先确认；确认后才真正保存。
const confirmVisible = ref(false);
const pending = ref<RiskyConfirm | null>(null);

function openAdd() {
  scope.invalidate();
  Object.assign(form, {
    trading_account_id: props.candidates[0]?.trading_account_id ?? "",
    enabled: true,
    priority: nextPriority(props.logical),
    adopt_existing_exposure: false
  });
  original.value = null;
  editing.value = false;
  error.value = "";
  adoptionNeeded.value = false;
  visible.value = true;
}

function openEdit(member: { trading_account_id: string; enabled: boolean; priority: number }, name: string) {
  scope.invalidate();
  Object.assign(form, { ...member, adopt_existing_exposure: false });
  original.value = { enabled: member.enabled, priority: member.priority };
  editing.value = true;
  editingName.value = name;
  error.value = "";
  adoptionNeeded.value = false;
  visible.value = true;
}

function accountName(): string {
  if (editing.value) return editingName.value;
  return props.candidates.find(item => item.trading_account_id === form.trading_account_id)?.name ?? form.trading_account_id;
}

/** 对话框的“确定”：需要确认时先打开确认框，对话框保持打开；不需要时直接保存。 */
async function save() {
  if (!form.trading_account_id) {
    Message.warning("请选择执行账户");
    return false;
  }
  const confirm =
    props.env === "paper"
      ? null
      : memberChangeConfirm({
          accountName: accountName(),
          comboName: props.logical.name,
          original: original.value,
          next: { enabled: form.enabled, priority: form.priority },
          adopt: form.adopt_existing_exposure
        });
  if (!confirm) return submit();
  pending.value = confirm;
  confirmVisible.value = true;
  return false;
}

/** 确认框的“确认”：保存成功后连同对话框一起关掉；要求接管时回到对话框显示勾选。 */
async function runPending() {
  if (await submit()) visible.value = false;
}

async function submit() {
  const submission = scope.begin();
  const logicalId = props.logical.logical_account_id;
  error.value = "";
  try {
    await addLogicalAccountMember({ logical_account_id: logicalId, ...form });
  } catch (cause) {
    if (!submission.isCurrent()) return false;
    if (isTradeAdoptionRequired(cause) && !form.adopt_existing_exposure) {
      adoptionNeeded.value = true;
      error.value = editing.value
        ? "这个账户上已有持仓或挂单，重新启用要确认接管：勾选下方“接管账户里已有的持仓”后再保存。"
        : "这个账户上已有持仓或挂单：勾选下方“接管账户里已有的持仓”后再保存。";
      return false;
    }
    error.value = cause instanceof Error ? cause.message : "请求失败，请稍后重试";
    return false;
  }
  if (!submission.isCurrent() || props.logical.logical_account_id !== logicalId) return false;
  emit("saved");
  return true;
}

defineExpose({ openAdd, openEdit });
</script>

<style src="../shared/accounts.css"></style>
<style scoped>
.block {
  margin-bottom: var(--moox-space-3);
}

.confirm-line {
  margin: 0 0 var(--moox-space-2);
}
</style>
