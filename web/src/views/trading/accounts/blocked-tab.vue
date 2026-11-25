<template>
  <a-table
    :bordered="{ cell: true }"
    size="small"
    row-key="key"
    :data="items"
    :loading="loading"
    :pagination="false"
    :scroll="{ x: LIST_WIDTH }"
  >
    <template #columns>
      <a-table-column title="标的" :width="190">
        <template #cell="{ record }">
          <strong>{{ record.instrument.exchange_symbol }}</strong>
          <span class="tx-muted"> {{ marketLabel(record.instrument.market_type) }}</span>
        </template>
      </a-table-column>
      <a-table-column title="交易所" :width="100">
        <template #cell="{ record }">{{ exchangeLabel(record.instrument.exchange) }}</template>
      </a-table-column>
      <a-table-column title="范围" :width="90">
        <template #cell="{ record }">{{ scopeLabel(record.instrument.scope) }}</template>
      </a-table-column>
      <a-table-column title="原因" :width="320" ellipsis tooltip>
        <template #cell="{ record }">{{ record.instrument.reason || "—" }}</template>
      </a-table-column>
      <a-table-column title="下架记录" :width="200" ellipsis tooltip>
        <template #cell="{ record }">
          <a-link v-if="record.instrument.delist_id" class="tx-mono" @click="emit('locate', record.instrument.delist_id)">
            {{ record.instrument.delist_id }}
          </a-link>
          <span v-else class="tx-muted">—</span>
        </template>
      </a-table-column>
      <a-table-column title="写入时间" :width="170">
        <template #cell="{ record }">
          <span class="tx-muted tx-nowrap"><TimeText :value="record.instrument.blocked_at" :zone="displayZone" /></span>
        </template>
      </a-table-column>
      <a-table-column title="操作" :width="90" :fixed="isNarrow ? undefined : 'right'">
        <template #cell="{ record }">
          <a-button size="mini" type="text" @click="askUnblock(record.instrument)">解除</a-button>
        </template>
      </a-table-column>
    </template>
    <template #empty>下架名单是空的，没有被禁止买入的标的。</template>
  </a-table>

  <ConfirmDialog
    v-model:visible="confirmVisible"
    :title="pending ? `解除「${pending.exchange_symbol}」的买入禁止` : ''"
    env="paper"
    combo-name=""
    :need-reason="false"
    confirm-text="解除"
    :submit="runUnblock"
  >
    <p class="confirm-line">解除后，这个标的恢复正常买入，策略目标里加仓的部分不再被截掉。</p>
    <p class="confirm-line">它对应的下架清仓还在运行时会提前结束：撤掉自己的订单，不再继续卖出。</p>
    <p class="confirm-line">进行中的下架处理（含演练）会一并取消。</p>
  </ConfirmDialog>
</template>

<script setup lang="ts">
import { useWindowSize } from "@vueuse/core";
import { computed, ref } from "vue";
import { Message } from "@arco-design/web-vue";
import TimeText from "@/components/time-text/index.vue";
import { exchangeLabel, unblockInstrument } from "@/api/trade";
import type { BlockedInstrument } from "@/api/trade";
import ConfirmDialog from "@/components/trade-confirm/confirm-dialog.vue";
import { useDisplayZone } from "@/hooks/useDisplayZone";
import { marketLabel } from "./shared/markets";
import { scopeLabel } from "../blocked/delist-utils";

const props = defineProps<{ rows: BlockedInstrument[]; loading: boolean }>();
const emit = defineEmits<{ changed: []; locate: [delistId: string] }>();

// 交易页的时间按当前空间登记的时区显示
const displayZone = useDisplayZone();

// 各列宽度之和：窄屏时表格横向滚动，每一列都保持自己的宽度
const LIST_WIDTH = 190 + 100 + 90 + 320 + 200 + 170 + 90;
// 窄屏时右侧操作列不固定，不然会盖住数据列
const { width: viewportWidth } = useWindowSize();
const isNarrow = computed(() => viewportWidth.value < 768);

// 名单以（交易所、市场、代码）为键
const items = computed(() =>
  props.rows.map(instrument => ({
    key: `${instrument.exchange}:${instrument.market_type}:${instrument.exchange_symbol}`,
    instrument
  }))
);

const confirmVisible = ref(false);
const pending = ref<BlockedInstrument | null>(null);

function askUnblock(instrument: BlockedInstrument) {
  pending.value = instrument;
  confirmVisible.value = true;
}

async function runUnblock() {
  const target = pending.value;
  if (!target) return;
  const rsp = await unblockInstrument({
    exchange: target.exchange,
    market_type: target.market_type,
    exchange_symbol: target.exchange_symbol
  });
  if (rsp.removed) Message.success(`已解除「${target.exchange_symbol}」的买入禁止`);
  else Message.info(`「${target.exchange_symbol}」已经不在名单里`);
  emit("changed");
}

defineExpose({ askUnblock, confirmVisible });
</script>

<style src="./shared/accounts.css"></style>
<style scoped>
.confirm-line {
  margin: 0 0 var(--moox-space-2);
}
</style>
