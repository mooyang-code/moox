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
          ><span class="tx-neg">没有能加入这个组合的执行账户，先去新建一个</span></template
        >
      </a-form-item>
      <a-form-item label="优先级" extra="数字越小越先使用，当前账户资金用完才用下一个账户">
        <a-input-number v-model="form.priority" :min="0" />
      </a-form-item>
      <a-form-item v-if="editing" label="启用" extra="停用后策略不再用它下单">
        <a-switch v-model="form.enabled" />
      </a-form-item>
      <a-form-item v-else extra="只有出现在策略目标里的标的会被调整，其余资产不动">
        <a-checkbox v-model="form.adopt_existing_exposure">接管账户里已有的持仓</a-checkbox>
      </a-form-item>
    </a-form>
  </a-modal>
</template>

<script setup lang="ts">
import { reactive, ref } from "vue";
import { Message } from "@arco-design/web-vue";
import { addLogicalAccountMember, exchangeLabels } from "@/api/trade";
import type { LogicalAccount, TradingAccount } from "@/api/trade";
import { createSubmitScope } from "@/utils/submit-scope";
import { marketsText } from "../shared/markets";
import { nextPriority } from "../shared/membership";

const props = defineProps<{ logical: LogicalAccount; candidates: TradingAccount[] }>();
const emit = defineEmits<{ saved: [] }>();

const visible = ref(false);
const editing = ref(false);
const editingName = ref("");
const error = ref("");
const form = reactive({ trading_account_id: "", enabled: true, priority: 1, adopt_existing_exposure: false });
// 打开和关闭都使进行中的保存失效：迟到的响应不能关闭用户重新打开的对话框。
const scope = createSubmitScope();

function openAdd() {
  scope.invalidate();
  Object.assign(form, {
    trading_account_id: props.candidates[0]?.trading_account_id ?? "",
    enabled: true,
    priority: nextPriority(props.logical),
    adopt_existing_exposure: false
  });
  editing.value = false;
  error.value = "";
  visible.value = true;
}

function openEdit(member: { trading_account_id: string; enabled: boolean; priority: number }, name: string) {
  scope.invalidate();
  Object.assign(form, { ...member, adopt_existing_exposure: false });
  editing.value = true;
  editingName.value = name;
  error.value = "";
  visible.value = true;
}

async function save() {
  if (!form.trading_account_id) {
    Message.warning("请选择执行账户");
    return false;
  }
  const submission = scope.begin();
  const logicalId = props.logical.logical_account_id;
  error.value = "";
  try {
    await addLogicalAccountMember({ logical_account_id: logicalId, ...form });
  } catch (cause) {
    if (submission.isCurrent()) error.value = cause instanceof Error ? cause.message : "请求失败，请稍后重试";
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
</style>
