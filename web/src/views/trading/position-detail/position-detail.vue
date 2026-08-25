<template>
  <div class="moox-page">
    <div class="moox-inner">
      <div class="page-head position-toolbar">
        <h2>持仓详情</h2>
        <a-space>
          <a-select v-model="exchangeAccountId" placeholder="选择 Trading 账户" style="width: 260px" @change="loadPositions">
            <a-option v-for="account in accounts" :key="account.trading_account_id" :value="account.trading_account_id">
              {{ account.name }} · {{ marketTypeLabels[account.market_type] }}
            </a-option>
          </a-select>
          <a-input v-model="instrument_id" placeholder="Symbol" allow-clear style="width: 150px" @press-enter="loadPositions" />
          <a-button :disabled="!exchangeAccountId" @click="loadPositions">
            <template #icon><icon-search /></template>
            查询
          </a-button>
          <a-button :loading="syncing" :disabled="!exchangeAccountId" @click="sync">
            <template #icon><icon-sync /></template>
            同步账户
          </a-button>
        </a-space>
      </div>

      <a-alert v-if="selectedAccount" class="account-status" :type="selectedAccount.ready ? 'success' : 'warning'">
        {{ exchangeLabels[selectedAccount.exchange] }} · {{ marketTypeLabels[selectedAccount.market_type] }} ·
        {{ selectedAccount.ready ? "Ready" : "Not Ready" }} · 最近同步
        {{ formatTimestamp(selectedAccount.last_sync_at) }}
      </a-alert>

      <a-table v-if="selectedAccount?.market_type === 1" row-key="asset" :data="holdings" :loading="loading" :pagination="false">
        <template #columns>
          <a-table-column title="Asset" data-index="asset" />
          <a-table-column title="数量" data-index="quantity" />
          <a-table-column title="平均成本" data-index="average_cost" />
          <a-table-column title="标记价" data-index="mark_price" />
          <a-table-column title="市值" data-index="market_value" />
          <a-table-column title="未实现 PnL">
            <template #cell="{ record }"><span :class="pnlClass(record.unrealized_pnl)">{{ record.unrealized_pnl || "-" }}</span></template>
          </a-table-column>
          <a-table-column title="Instrument" data-index="instrument_id" />
        </template>
      </a-table>

      <a-table v-else row-key="instrument_id" :data="positions" :loading="loading" :pagination="false" :scroll="{ x: 'max-content' }">
        <template #columns>
          <a-table-column title="市场">
            <template #cell>{{ selectedAccount ? marketTypeLabels[selectedAccount.market_type] : "-" }}</template>
          </a-table-column>
          <a-table-column title="Symbol" data-index="instrument_id" />
          <a-table-column title="方向">
            <template #cell="{ record }">{{ quantitySide(record.signed_quantity) }}</template>
          </a-table-column>
          <a-table-column title="Base 数量" data-index="signed_quantity" />
          <a-table-column title="开仓价" data-index="entry_price" />
          <a-table-column title="标记价" data-index="mark_price" />
          <a-table-column title="强平价" data-index="liquidation_price" />
          <a-table-column title="杠杆" data-index="leverage" />
          <a-table-column title="保证金" data-index="used_margin" />
          <a-table-column title="未实现 PnL">
            <template #cell="{ record }">
              <span :class="pnlClass(record.unrealized_pnl)">{{ record.unrealized_pnl }}</span>
            </template>
          </a-table-column>
          <a-table-column title="来源时间">
            <template #cell="{ record }">{{ formatTimestamp(record.exchange_updated_at) }}</template>
          </a-table-column>
        </template>
      </a-table>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from "vue";
import { Message } from "@arco-design/web-vue";
import { createLatestRequestGuard } from "@/utils/latest-request";
import { exchangeLabels, formatTimestamp, listTradingAccounts, listHoldings, listPositions, marketTypeLabels, syncTradingAccount } from "@/api/trade";
import type { TradingAccount, Holding, Position } from "@/api/trade/types";

defineOptions({ name: "position-detail" });

const accounts = ref<TradingAccount[]>([]);
const positions = ref<Position[]>([]);
const holdings = ref<Holding[]>([]);
const exchangeAccountId = ref("");
const instrument_id = ref("");
const loading = ref(false);
const syncing = ref(false);
const accountRequests = createLatestRequestGuard();
const positionRequests = createLatestRequestGuard();
const selectedAccount = computed(
  () => accounts.value.find(account => account.trading_account_id === exchangeAccountId.value) || null
);

async function loadAccounts() {
  const request = accountRequests.begin();
  const response = await listTradingAccounts({ page: { page: 1, size: 200 } });
  if (!request.isLatest()) return;
  accounts.value = response.accounts || [];
  exchangeAccountId.value ||= accounts.value[0]?.trading_account_id || "";
}

async function loadPositions() {
  if (!exchangeAccountId.value) {
    positionRequests.invalidate();
    positions.value = [];
    return;
  }
  const request = positionRequests.begin();
  const accountId = exchangeAccountId.value;
  const requestedSymbol = instrument_id.value.trim().toUpperCase();
  loading.value = true;
  try {
    if (selectedAccount.value?.market_type === 1) {
      const response = await listHoldings(accountId);
      if (!request.isLatest() || exchangeAccountId.value !== accountId) return;
      holdings.value = response.holdings || [];
      positions.value = [];
      return;
    }
    const response = await listPositions({ trading_account_id: accountId, instrument_id: requestedSymbol });
    if (!request.isLatest() || exchangeAccountId.value !== accountId) return;
    positions.value = response.positions || [];
  } finally {
    if (request.isLatest()) loading.value = false;
  }
}

async function sync() {
  const accountId = exchangeAccountId.value;
  syncing.value = true;
  try {
    const response = await syncTradingAccount(accountId);
    if (exchangeAccountId.value === accountId) Message.success(`同步完成：${response.positions_updated} positions`);
    await Promise.all([loadAccounts(), loadPositions()]);
  } finally {
    syncing.value = false;
  }
}

function quantitySide(quantity: string) {
  const value = Number(quantity);
  if (value > 0) return "LONG";
  if (value < 0) return "SHORT";
  return "FLAT";
}

function pnlClass(value: string) {
  const parsed = Number(value);
  return parsed > 0 ? "positive" : parsed < 0 ? "negative" : "";
}

onMounted(async () => {
  await loadAccounts();
  await loadPositions();
});
</script>

<style scoped>
.page-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: var(--moox-space-4);
}

.position-toolbar {
  margin-bottom: var(--moox-space-2);
}

.account-status {
  margin-bottom: var(--moox-space-4);
}

.positive {
  color: rgb(var(--red-6));
}

.negative {
  color: rgb(var(--green-6));
}
</style>
