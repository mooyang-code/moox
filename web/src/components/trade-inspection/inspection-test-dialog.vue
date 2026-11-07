<template>
  <a-modal
    :visible="visible"
    width="min(640px, 94vw)"
    title="测试连接"
    hide-cancel
    ok-text="关闭"
    @ok="emit('update:visible', false)"
    @cancel="emit('update:visible', false)"
    @close="emit('update:visible', false)"
  >
    <a-alert type="info" class="dialog-alert">
      只读检测：用这把密钥查询账户类型和权限，不会下单，也不会保存。交易所密钥通常绑定了海外机的 IP，检测由海外机上的 Trade
      服务发起。
    </a-alert>
    <a-form :model="form" auto-label-width>
      <a-form-item label="密钥">{{ secretName }}（{{ exchangeLabel }}）</a-form-item>
      <a-form-item label="环境">
        <a-radio-group v-model="form.environment" :disabled="running">
          <a-radio :value="2">生产环境</a-radio>
          <a-radio :value="1">测试网</a-radio>
        </a-radio-group>
      </a-form-item>
      <a-form-item
        label="要开通的市场"
        extra="不勾选时只判断和市场无关的问题；和市场有关的（双向持仓、万向划转权限等）写在提醒里"
      >
        <a-checkbox-group v-model="form.markets" :disabled="running">
          <a-checkbox :value="1">现货</a-checkbox>
          <a-checkbox :value="2">永续合约</a-checkbox>
        </a-checkbox-group>
      </a-form-item>
    </a-form>
    <a-space class="dialog-actions">
      <a-button type="primary" :loading="running" :disabled="!exchangeValue" @click="run">开始检测</a-button>
      <span v-if="!exchangeValue" class="dialog-hint">只有币安和 OKX 的交易所密钥可以检测</span>
    </a-space>
    <a-alert v-if="errorText" type="warning" class="dialog-alert dialog-error">
      {{ errorText }}
    </a-alert>
    <inspection-result v-if="view" :view="view" class="dialog-result" />
  </a-modal>
</template>

<script setup lang="ts">
import { computed, reactive, ref, watch } from "vue";
import { inspectExchangeCredential } from "@/api/trade";
import type { Exchange, MarketType } from "@/api/trade/types";
import InspectionResult from "./inspection-result.vue";
import { fromInspection, type InspectionView } from "./inspection";
import { exchangeFromProvider, inspectionRequest } from "./inspection-request";

defineOptions({ name: "TradeInspectionTestDialog" });

const props = defineProps<{ visible: boolean; secretId: string; secretName: string; provider: string }>();
const emit = defineEmits<{ "update:visible": [value: boolean] }>();

const form = reactive<{ environment: 1 | 2; markets: MarketType[] }>({ environment: 2, markets: [] });
const running = ref(false);
const view = ref<InspectionView | null>(null);
const errorText = ref("");

const exchangeValue = computed<Exchange>(() => exchangeFromProvider(props.provider));
const exchangeLabel = computed(() => (props.provider || "未知").toUpperCase());

// 每次打开都从干净状态开始，不沿用上一把密钥的结果。
watch(
  () => [props.visible, props.secretId],
  () => {
    view.value = null;
    errorText.value = "";
    form.environment = 2;
    form.markets = [];
  }
);

async function run() {
  running.value = true;
  view.value = null;
  errorText.value = "";
  try {
    const rsp = await inspectExchangeCredential(
      inspectionRequest({
        provider: props.provider,
        secretId: props.secretId,
        environment: form.environment,
        markets: form.markets
      })
    );
    view.value = fromInspection(rsp.inspection);
  } catch (error) {
    // 检测没有完成（超时、限频）不代表密钥有问题，只提示稍后重试。
    errorText.value = error instanceof Error ? error.message : "检测失败，请稍后重试";
  } finally {
    running.value = false;
  }
}
</script>

<style scoped>
.dialog-alert {
  margin-bottom: 12px;
}
.dialog-actions {
  margin-bottom: 12px;
}
.dialog-hint {
  color: var(--color-text-3);
  font-size: 12px;
}
.dialog-error {
  margin-top: 8px;
}
.dialog-result {
  margin-top: 8px;
}
</style>
