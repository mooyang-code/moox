<template>
  <div class="toolbar">
    <a-radio-group v-model="range" type="button" @change="emit('search')">
      <a-radio v-for="item in recordRanges" :key="item.key" :value="item.key">{{ item.label }}</a-radio>
    </a-radio-group>
    <slot name="filters" />
    <a-select
      v-if="memberOptions.length > 1"
      v-model="accountId"
      allow-clear
      placeholder="执行账户"
      style="width: 150px"
      @change="emit('search')"
    >
      <a-option v-for="item in memberOptions" :key="item.id" :value="item.id">{{ item.label }}</a-option>
    </a-select>
    <a-select
      v-if="marketOptions.length > 1"
      v-model="market"
      :disabled="!symbol.trim()"
      style="width: 110px"
      @change="emit('search')"
    >
      <a-option v-for="item in marketOptions" :key="item" :value="item">{{ marketLabel(item) }}</a-option>
    </a-select>
    <a-input
      v-model="symbol"
      allow-clear
      :placeholder="stockOnly ? '代码，如 600519.XSHG' : '币，如 BTC'"
      style="width: 150px"
      @press-enter="emit('search')"
      @clear="emit('search')"
    />
    <a-button :loading="loading" @click="emit('search')">查询</a-button>
    <slot />
  </div>
</template>

<script setup lang="ts">
import type { MarketType } from "@/api/trade";
import { marketLabel } from "../shared/markets";
import { recordRanges, type RecordRange } from "./record-range";

/** 订单、成交页签共用的工具栏：时间范围、执行账户、市场、标的和查询按钮；页签自己的筛选放 filters 插槽，其余放默认插槽。 */
defineProps<{
  memberOptions: { id: string; label: string }[];
  marketOptions: MarketType[];
  stockOnly: boolean;
  loading: boolean;
}>();
const emit = defineEmits<{ search: [] }>();
const range = defineModel<RecordRange>("range", { required: true });
const accountId = defineModel<string | undefined>("accountId", { required: true });
const market = defineModel<MarketType | undefined>("market", { required: true });
const symbol = defineModel<string>("symbol", { required: true });
</script>

<style scoped>
.toolbar {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: var(--moox-space-3);
  padding: 12px 16px;
}
</style>
