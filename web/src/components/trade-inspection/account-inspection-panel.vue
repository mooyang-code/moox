<template>
  <div class="account-inspection">
    <div class="account-inspection-head">
      <div>
        <span class="account-inspection-title">账户类型与检测</span>
        <span v-if="view" class="account-inspection-mode">{{ accountModeLabel(view.accountMode) }}</span>
      </div>
      <a-button size="small" :loading="running" @click="reinspect">重新检测</a-button>
    </div>
    <inspection-result v-if="view" :view="view" :zone="displayZone" />
    <p v-else class="account-inspection-empty">还没有检测结果，点“重新检测”读取账户类型和权限。</p>
    <a-alert v-if="message" :type="messageType" class="account-inspection-message">{{ message }}</a-alert>
  </div>
</template>

<script setup lang="ts">
import { computed, ref } from "vue";
import type { TradingAccount } from "@/api/trade/types";
import InspectionResult from "./inspection-result.vue";
import { useDisplayZone } from "@/hooks/useDisplayZone";
import { accountModeLabel, fromStoredInspection, type InspectionView } from "./inspection";
import { runReinspect } from "./inspection-actions";

defineOptions({ name: "TradeAccountInspectionPanel" });

// 执行账户的检测展示：账户类型、最近一次检测结果和“重新检测”。交易账户列表的“执行账户”页签里，每个账户展开后挂一个。
// accountId 是执行账户 ID，inspectionJson 是账户里存的最近一次检测结果（TradingAccount.inspection_json）。
const props = defineProps<{ accountId: string; inspectionJson?: string }>();
// 检测时间：交易页按当前空间的时区
const displayZone = useDisplayZone();
const emit = defineEmits<{ updated: [account: TradingAccount | null] }>();

const running = ref(false);
const message = ref("");
const failed = ref(false);
// 重新检测后用返回的结果（检测没完成时是保留的上次结果）；还没点过时用账户里存的。
const fresh = ref<InspectionView | null>(null);

const view = computed<InspectionView | null>(() => fresh.value ?? fromStoredInspection(props.inspectionJson));
const messageType = computed(() => (failed.value ? "warning" : "info"));

async function reinspect() {
  running.value = true;
  message.value = "";
  try {
    const outcome = await runReinspect(props.accountId);
    failed.value = !outcome.ok;
    message.value = outcome.message;
    if (outcome.view) fresh.value = outcome.view;
    emit("updated", outcome.account);
  } finally {
    running.value = false;
  }
}
</script>

<style scoped>
.account-inspection-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 8px;
}
.account-inspection-title {
  font-weight: 600;
}
.account-inspection-mode {
  margin-left: 8px;
  color: var(--color-text-2);
  font-size: 13px;
}
.account-inspection-empty {
  color: var(--color-text-3);
  font-size: 13px;
}
.account-inspection-message {
  margin-top: 8px;
}
</style>
