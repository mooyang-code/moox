<template>
  <a-card title="运行控制" :bordered="false">
    <a-alert v-if="!canOperate" type="info" show-icon>当前账号只有查看权限。</a-alert>
    <a-space v-else>
      <a-button v-if="status !== 'disabled'" status="warning" :loading="loading" @click="pause">暂停 Binding</a-button>
      <a-button v-else type="primary" :loading="loading" @click="resume">恢复 Binding</a-button>
      <a-select v-model="mode" style="width: 130px" :disabled="loading"
        ><a-option value="observe">Observe</a-option><a-option value="paper">Paper</a-option
        ><a-option v-if="store.liveExecutionEnabled" value="live">Live</a-option></a-select
      >
      <template v-if="mode === 'paper' || mode === 'live'">
        <a-input v-model="channelId" style="width: 180px" placeholder="执行通道 ID" :disabled="loading" />
        <a-input v-model="capitalAmount" style="width: 140px" placeholder="执行资金" :disabled="loading" />
        <a-input v-model="quoteAsset" style="width: 100px" placeholder="计价资产" :disabled="loading" />
      </template>
      <a-button :loading="loading" @click="changeMode">应用模式</a-button>
    </a-space>
    <a-modal v-model:visible="reasonVisible" title="填写操作原因" @ok="submitReason">
      <a-textarea v-model="reason" placeholder="请输入操作原因" :auto-size="{ minRows: 3, maxRows: 6 }" />
    </a-modal>
  </a-card>
</template>

<script setup lang="ts">
import { computed, ref, watch } from "vue";
import { Message } from "@arco-design/web-vue";
import { useStrategyStore } from "@/store/modules/strategy";
import { useUserInfoStore } from "@/store/modules/user-info";
const props = defineProps<{ bindingId: string; status?: string; currentMode?: string }>();
const emit = defineEmits<{ changed: [] }>();
const store = useStrategyStore();
const userStore = useUserInfoStore();
const canOperate = computed(() => userStore.account.roles.includes("admin"));
const mode = ref(props.currentMode || "observe");
const channelId = ref("");
const capitalAmount = ref("");
const quoteAsset = ref("USDT");
const reason = ref("");
const pending = ref<"pause" | "resume" | "mode">("pause");
const reasonVisible = ref(false);
const loading = ref(false);
watch(
  () => props.currentMode,
  currentMode => {
    mode.value = currentMode || "observe";
    if (!store.liveExecutionEnabled && mode.value === "live") mode.value = "observe";
  },
  { immediate: true }
);
watch(
  () => store.liveExecutionEnabled,
  () => {
    if (!store.liveExecutionEnabled && mode.value === "live") mode.value = "observe";
  }
);
function ask(action: "pause" | "resume" | "mode") {
  pending.value = action;
  reason.value = "";
  reasonVisible.value = true;
}
function pause() {
  ask("pause");
}
function resume() {
  ask("resume");
}
function changeMode() {
  ask("mode");
}
async function submitReason() {
  if (!reason.value.trim()) {
    Message.warning("请填写操作原因");
    return;
  }
  if (
    pending.value === "mode" &&
    (mode.value === "paper" || mode.value === "live") &&
    (!channelId.value.trim() || !capitalAmount.value.trim())
  ) {
    Message.warning("Paper/Live 模式需要执行通道和执行资金");
    return;
  }
  loading.value = true;
  try {
    if (pending.value === "pause") await store.pause(props.bindingId, reason.value);
    else if (pending.value === "resume") await store.resume(props.bindingId, reason.value);
    else
      await store.changeMode(props.bindingId, mode.value, reason.value, {
        channel_id: channelId.value.trim(),
        capital_amount: capitalAmount.value.trim(),
        quote_asset: quoteAsset.value.trim() || "USDT"
      });
    reasonVisible.value = false;
    Message.success("操作已提交");
    emit("changed");
  } catch (err) {
    Message.error(err instanceof Error ? err.message : "操作失败");
  } finally {
    loading.value = false;
  }
}
</script>
