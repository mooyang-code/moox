<template>
  <a-drawer :visible="visible" width="min(560px, 100vw)" :footer="false" unmount-on-close @cancel="emit('update:visible', false)">
    <template #title>
      订单 {{ order ? `${instrumentName(order.instrument_id)} ${order.side === 1 ? "买入" : "卖出"}` : "" }}
    </template>
    <template v-if="order">
      <a-descriptions :column="1" size="small" :label-style="{ width: '96px' }">
        <a-descriptions-item label="状态">
          <StatusMark :tone="stateTone(order.state)">{{ orderStateLabels[order.state] || order.state }}</StatusMark>
        </a-descriptions-item>
        <a-descriptions-item label="市场">{{ marketLabel(order.market_type) }}</a-descriptions-item>
        <a-descriptions-item label="类型">
          {{ order.order_type === 1 ? "市价" : "限价" }}{{ order.reduce_position_only ? "，只减仓" : "" }}
        </a-descriptions-item>
        <a-descriptions-item label="数量"
          >{{ fmtQty(order.quantity) }}，已成交 {{ fmtQty(order.filled_quantity) }}</a-descriptions-item
        >
        <a-descriptions-item label="成交均价">{{
          Number(order.average_price) ? fmtQty(order.average_price) : "—"
        }}</a-descriptions-item>
        <a-descriptions-item v-if="order.limit_price" label="限价">{{ fmtQty(order.limit_price) }}</a-descriptions-item>
        <a-descriptions-item label="参考价">{{ fmtQty(order.reference_price) }}</a-descriptions-item>
        <a-descriptions-item label="来源">{{ orderOwnerLabels[order.owner_type] || order.owner_type }}</a-descriptions-item>
        <a-descriptions-item label="执行账户">{{ accountName(order.trading_account_id) }}</a-descriptions-item>
        <a-descriptions-item label="提交时间">{{ fmtClock(order.submitted_at, true) }}</a-descriptions-item>
        <a-descriptions-item v-if="Number(order.finished_at)" label="结束时间">{{
          fmtClock(order.finished_at, true)
        }}</a-descriptions-item>
        <a-descriptions-item v-if="order.reject_reason" label="原因">{{
          orderReasonText(order.reject_reason)
        }}</a-descriptions-item>
        <a-descriptions-item label="订单编号"
          ><span class="tx-mono">{{ order.order_id }}</span></a-descriptions-item
        >
        <a-descriptions-item v-if="order.exchange_order_id" label="交易所订单号">
          <span class="tx-mono">{{ order.exchange_order_id }}</span>
        </a-descriptions-item>
      </a-descriptions>
      <h4 class="fills-title">成交明细</h4>
      <a-spin v-if="loading" />
      <a-alert v-else-if="error" type="warning">{{ error }}</a-alert>
      <a-table v-else size="small" row-key="fill_id" :data="fills" :pagination="false" :scroll="{ x: 460 }">
        <template #columns>
          <a-table-column title="时间" :width="130">
            <template #cell="{ record }">{{ fmtClock(record.traded_at, true) }}</template>
          </a-table-column>
          <a-table-column title="价格" align="right"
            ><template #cell="{ record }">{{ fmtQty(record.price) }}</template></a-table-column
          >
          <a-table-column title="数量" align="right"
            ><template #cell="{ record }">{{ fmtQty(record.quantity) }}</template></a-table-column
          >
          <a-table-column title="手续费" align="right">
            <template #cell="{ record }">{{ fmtQty(record.fee) }} {{ record.fee_asset }}</template>
          </a-table-column>
        </template>
        <template #empty>这个订单还没有成交</template>
      </a-table>
    </template>
  </a-drawer>
</template>

<script setup lang="ts">
import { ref, watch } from "vue";
import { listFills, orderOwnerLabels, orderReasonText, orderStateLabels } from "@/api/trade";
import type { Fill, Order } from "@/api/trade";
import { createLatestRequestGuard } from "@/utils/latest-request";
import { fmtClock, fmtQty } from "../shared/format";
import { instrumentName, marketLabel } from "../shared/markets";
import StatusMark from "../shared/status-mark.vue";

const props = defineProps<{ visible: boolean; order: Order | null; accountName: (id: string) => string }>();
const emit = defineEmits<{ "update:visible": [value: boolean] }>();

const fills = ref<Fill[]>([]);
const loading = ref(false);
const error = ref("");
const requests = createLatestRequestGuard();

function stateTone(state: string) {
  switch (state) {
    case "FILLED":
      return "ok";
    case "REJECTED":
    case "EXPIRED":
      return "bad";
    case "PARTIALLY_FILLED":
    case "CANCELING":
    case "SUBMIT_UNKNOWN":
    case "CANCEL_UNKNOWN":
      return "warn";
    case "CANCELED":
    case "PARTIALLY_CANCELED":
      return "";
    default:
      return "run";
  }
}

watch(
  () => [props.visible, props.order?.order_id] as const,
  async ([visible, id]) => {
    if (!visible || !id) return;
    const request = requests.begin();
    loading.value = true;
    error.value = "";
    fills.value = [];
    try {
      const rsp = await listFills({
        order_id: id,
        trading_account_id: props.order?.trading_account_id,
        page: { page: 1, size: 200 }
      });
      if (request.isLatest()) fills.value = rsp.fills || [];
    } catch (cause) {
      if (request.isLatest()) error.value = cause instanceof Error ? cause.message : "成交明细读取失败";
    } finally {
      if (request.isLatest()) loading.value = false;
    }
  },
  { immediate: true }
);
</script>

<style src="../shared/accounts.css"></style>
<style scoped>
.fills-title {
  margin: var(--moox-space-4) 0 var(--moox-space-2);
  font-size: 14px;
}
</style>
