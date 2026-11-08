<template>
  <div class="inspection-preview">
    <a-spin v-if="state === 'running'" tip="正在检测密钥（由海外机上的 Trade 服务发起）…" />
    <a-alert v-else-if="state === 'unavailable'" type="warning" :show-icon="true">
      <template #title>这次没有检测成功，不能继续</template>
      {{ message }}
      <a-button size="mini" type="text" @click="run">重新检测</a-button>
    </a-alert>
    <inspection-result v-else-if="view" :view="view" :zone="displayZone" />
    <p v-else class="inspection-preview-hint">选好交易所、环境和密钥后会立即检测账户类型和权限。</p>
  </div>
</template>

<script setup lang="ts">
import { ref, watch } from "vue";
import type { Exchange, MarketType } from "@/api/trade/types";
import InspectionResult from "./inspection-result.vue";
import { useDisplayZone } from "@/hooks/useDisplayZone";
import type { InspectionView } from "./inspection";
import { previewAllowsNext, runPreview, type PreviewState } from "./inspection-actions";

defineOptions({ name: "TradeInspectionPreview" });

// 新建账户第二步的内嵌检测：选完密钥立即检测并展示结果。父组件按 change 事件里的 allowNext 禁用“下一步”：
// 有阻断项、检测没完成（没有结论）、还没检测，都不放行。
const props = defineProps<{ exchange: Exchange; environment: 1 | 2; secretId: string; markets?: MarketType[] }>();
// 检测时间：交易页按当前空间的时区
const displayZone = useDisplayZone();
const emit = defineEmits<{ change: [payload: { state: PreviewState; allowNext: boolean; view: InspectionView | null }] }>();

const state = ref<PreviewState>("idle");
const view = ref<InspectionView | null>(null);
const message = ref("");
// 请求序号：连续改选密钥时，只认最后一次请求的结果，先发后到的旧结果直接丢弃。
let sequence = 0;

function publish() {
  emit("change", { state: state.value, allowNext: previewAllowsNext(state.value), view: view.value });
}

async function run() {
  if (!props.secretId || !props.exchange) {
    sequence++;
    state.value = "idle";
    view.value = null;
    message.value = "";
    publish();
    return;
  }
  const mine = ++sequence;
  state.value = "running";
  view.value = null;
  publish();
  const result = await runPreview({
    exchange: props.exchange,
    environment: props.environment,
    credential_secret_id: props.secretId,
    ...(props.markets?.length ? { markets: [...props.markets].sort((a, b) => a - b) } : {})
  });
  if (mine !== sequence) return;
  state.value = result.state;
  view.value = result.view;
  message.value = result.message;
  publish();
}

watch(() => [props.exchange, props.environment, props.secretId, (props.markets ?? []).join(",")], run, { immediate: true });

defineExpose({ run });
</script>

<style scoped>
.inspection-preview-hint {
  color: var(--color-text-3);
  font-size: 13px;
}
</style>
