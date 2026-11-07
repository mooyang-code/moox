<template>
  <SectionBlock title="资金流水" flush>
    <template #sub>直接查交易所（模拟盘查本地成交），不落库，最长 90 天</template>
    <div class="toolbar">
      <a-select v-if="memberOptions.length > 1" v-model="accountId" style="width: 150px" @change="onAccountChange">
        <a-option v-for="item in memberOptions" :key="item.id" :value="item.id">{{ item.label }}</a-option>
      </a-select>
      <a-select v-if="showMarket" v-model="market" style="width: 150px" @change="load">
        <a-option v-for="item in accountMarkets" :key="item" :value="item">{{ marketLabel(item) }}</a-option>
      </a-select>
      <a-range-picker v-model="range" value-format="timestamp" :allow-clear="false" style="width: 320px" @change="load" />
      <a-select v-model="typeFilter" placeholder="全部类型" allow-clear style="width: 150px">
        <a-option v-for="item in ledgerTypeOptions" :key="item.value" :value="item.value">{{ item.label }}</a-option>
      </a-select>
      <a-button :loading="loading" @click="load">查询</a-button>
    </div>
    <a-alert v-if="unsupported" type="info" class="alert">
      这个账户的交易所没有资金流水接口，不显示资金流水。订单和成交照常可查。
    </a-alert>
    <a-alert v-else-if="error" type="warning" class="alert">{{ error }}</a-alert>
    <a-alert v-else-if="hasMore" type="info" class="alert">
      只取到时间范围内最新的 {{ entries.length }} 条{{
        typeFilter ? "，类型筛选只在这些记录里进行" : ""
      }}；更早的记录请缩小时间范围后查询。这里不显示合计，取到的只是一部分。
    </a-alert>
    <a-table
      :bordered="{ cell: true }"
      :data="visibleEntries"
      :loading="loading"
      :pagination="{ pageSize: 20 }"
      row-key="id"
      size="small"
    >
      <template #columns>
        <a-table-column title="时间" :width="150">
          <template #cell="{ record }"
            ><span class="tx-muted tx-nowrap">{{ fmtClock(record.time, true) }}</span></template
          >
        </a-table-column>
        <a-table-column title="类型" :width="110">
          <template #cell="{ record }">{{ ledgerTypeLabel(record.type) }}</template>
        </a-table-column>
        <a-table-column title="资产" data-index="asset" :width="80" />
        <a-table-column title="金额" :width="150" align="right">
          <template #cell="{ record }">
            <span class="tx-num" :class="amountClass(record.amount)">{{ fmtSigned(record.amount, 6) }}</span>
          </template>
        </a-table-column>
        <a-table-column title="标的" :width="140">
          <template #cell="{ record }">{{ record.exchange_symbol || "—" }}</template>
        </a-table-column>
        <a-table-column title="交易所原始类型">
          <template #cell="{ record }"
            ><span class="tx-muted tx-mono">{{ record.info || record.exchange_type || "—" }}</span></template
          >
        </a-table-column>
      </template>
    </a-table>
  </SectionBlock>
</template>

<script setup lang="ts">
import { computed, inject, ref, watch } from "vue";
import { getExecutionCapabilities, listAccountLedger, type LedgerEntry, type MarketType } from "@/api/trade";
import { fmtClock, fmtSigned } from "../shared/format";
import {
  amountClass,
  defaultLedgerMarket,
  defaultLedgerRange,
  ledgerRangeError,
  ledgerTypeLabel,
  ledgerTypeOptions
} from "../shared/ledger";
import { marketLabel } from "../shared/markets";
import SectionBlock from "../shared/section-block.vue";
import { detailContextKey } from "./context";

const { logical, accounts } = inject(detailContextKey)!;

// 一次最多取的条数：多了交易所要连续翻页，容易撞上限频和超时。
const LEDGER_LIMIT = 500;

const accountId = ref("");
const market = ref<MarketType>(1);
const range = ref<number[]>(defaultLedgerRange());
const typeFilter = ref<string>();
const entries = ref<LedgerEntry[]>([]);
const hasMore = ref(false);
const loading = ref(false);
const error = ref("");
let token = 0;

const memberOptions = computed(() =>
  (logical.value.members || [])
    .filter(member => accounts.value.has(member.trading_account_id))
    .map(member => ({ id: member.trading_account_id, label: accounts.value.get(member.trading_account_id)!.name }))
);
const accountMarkets = computed(() => accounts.value.get(accountId.value)?.markets || []);
// 模拟盘由本地成交生成流水，后端不看市场，下拉没有作用
const showMarket = computed(() => accountMarkets.value.length > 1 && accounts.value.get(accountId.value)?.execution_mode !== 1);
// 后端给的“能否查流水”标志（账户级）；false 时不发请求，直接说明
const supportedByAccount = ref<Record<string, boolean>>({});
const unsupported = computed(() => supportedByAccount.value[accountId.value] === false);
const visibleEntries = computed(() =>
  typeFilter.value ? entries.value.filter(entry => entry.type === typeFilter.value) : entries.value
);

function onAccountChange() {
  market.value = defaultLedgerMarket(accountMarkets.value);
  void load();
}

async function load() {
  const current = ++token;
  if (!accountId.value) return;
  const rangeError = ledgerRangeError(range.value);
  if (rangeError) {
    entries.value = [];
    hasMore.value = false;
    error.value = rangeError;
    return;
  }
  loading.value = true;
  error.value = "";
  try {
    if (supportedByAccount.value[accountId.value] === undefined) {
      const id = accountId.value;
      const capability = await getExecutionCapabilities(id).catch(() => undefined);
      if (current !== token) return;
      // proto3 的 false 不出现在 JSON 里，没有这个字段就是不支持；读不到标志时照常查询，由后端说明原因
      if (capability) supportedByAccount.value = { ...supportedByAccount.value, [id]: Boolean(capability.capabilities.ledger) };
    }
    if (unsupported.value) {
      entries.value = [];
      hasMore.value = false;
      return;
    }
    const rsp = await listAccountLedger({
      trading_account_id: accountId.value,
      // 账户只开通一个市场时可以省略，多个市场时必须指明；模拟盘不看市场
      market_type: showMarket.value ? market.value : undefined,
      start_time: String(range.value[0]),
      end_time: String(range.value[1]),
      limit: LEDGER_LIMIT
    });
    if (current !== token) return;
    entries.value = rsp.entries || [];
    hasMore.value = Boolean(rsp.has_more);
  } catch (cause) {
    if (current !== token) return;
    entries.value = [];
    hasMore.value = false;
    // 不支持的市场直接显示后端给的原因，不自己猜哪个市场支持。
    error.value = cause instanceof Error ? cause.message : String(cause);
  } finally {
    if (current === token) loading.value = false;
  }
}

watch(
  () => logical.value.logical_account_id,
  () => {
    accountId.value = memberOptions.value[0]?.id ?? "";
    market.value = defaultLedgerMarket(accountMarkets.value);
    range.value = defaultLedgerRange();
    typeFilter.value = undefined;
    void load();
  },
  { immediate: true }
);
</script>

<style src="../shared/accounts.css"></style>
<style scoped>
.toolbar {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: var(--moox-space-3);
  padding: 12px 16px;
}

.alert {
  width: auto;
  margin: 0 16px var(--moox-space-3);
}
</style>
