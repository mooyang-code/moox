<template>
  <div class="moox-page">
    <div class="moox-inner">
      <div class="page-head"><h2>持仓详情</h2></div>
      <section class="positions-panel">
        <div v-if="positionAccountTabs.length" class="account-tabs-block">
          <div
            class="position-account-tabs"
            :class="{ expanded: positionAccountTabsExpanded }"
            role="tablist"
            aria-label="持仓账户"
          >
            <button
              v-for="acc in positionAccountTabs"
              :key="acc.account_id"
              type="button"
              class="position-account-tab"
              :class="{ active: positionFilter.account_id === acc.account_id }"
              role="tab"
              :aria-selected="positionFilter.account_id === acc.account_id"
              @click="onPositionAccountTabClick(acc.account_id)"
            >
              <span class="account-tab-name">{{ acc.account_name }}</span>
              <span class="account-tab-type">{{ accountTypeLabels[acc.account_type] || acc.account_type }}</span>
              <span class="account-tab-balance">余额 {{ nonZeroBalanceCount(acc.account_id) }}</span>
            </button>
          </div>
          <a-button
            class="account-tabs-toggle"
            size="small"
            type="text"
            :aria-label="positionAccountTabsExpanded ? '收起账户' : '展开账户'"
            :title="positionAccountTabsExpanded ? '收起账户' : '展开账户'"
            @click="togglePositionAccountTabs"
          >
            <template #icon>
              <icon-up v-if="positionAccountTabsExpanded" />
              <icon-down v-else />
            </template>
          </a-button>
        </div>
        <a-empty v-else description="暂无账户" />

        <div v-if="selectedAccount" class="account-balance-panel">
          <div class="account-balance-head">
            <div>
              <span class="balance-label">余额快照</span>
              <strong>{{ selectedAccount.account_name }}</strong>
              <span class="balance-total">非零 {{ selectedNonZeroBalanceCount }} / {{ selectedBalanceCount }}</span>
            </div>
            <a-space>
              <a-button size="small" @click="loadSelectedBalances" :loading="selectedBalanceLoading">
                <template #icon><icon-refresh /></template>
                刷新余额
              </a-button>
              <a-button size="small" type="primary" @click="syncBalancesForCurrent" :loading="syncingBalances">
                <template #icon><icon-sync /></template>
                同步余额
              </a-button>
            </a-space>
          </div>

          <div v-if="selectedAccountBalances.length" class="balance-chip-list">
            <span v-for="item in selectedTopBalances" :key="item.currency" class="balance-chip">
              <b>{{ item.currency }}</b>
              <em>{{ formatBalanceAmount(balanceDisplayAmount(item)) }}</em>
            </span>
          </div>
          <div v-else class="balance-empty">
            {{ selectedBalanceLoading ? '读取余额中' : '暂无非零余额' }}
          </div>

          <a-table
            v-if="selectedAccountBalances.length"
            row-key="currency"
            size="small"
            :bordered="{ cell: true }"
            :data="selectedAccountBalances"
            :pagination="{ pageSize: 12, simple: true }"
            :scroll="{ x: 'max-content' }"
          >
            <template #columns>
              <a-table-column title="币种" data-index="currency" :width="100" />
              <a-table-column title="可用" :width="160">
                <template #cell="{ record }">{{ formatBalanceAmount(record.available) }}</template>
              </a-table-column>
              <a-table-column title="冻结" :width="160">
                <template #cell="{ record }">{{ formatBalanceAmount(record.frozen) }}</template>
              </a-table-column>
              <a-table-column title="总额" :width="160">
                <template #cell="{ record }">{{ formatBalanceAmount(record.total) }}</template>
              </a-table-column>
            </template>
          </a-table>
        </div>

        <div class="position-toolbar">
          <a-space>
            <a-input
              v-model="positionFilter.symbol"
              placeholder="交易对"
              style="width: 140px"
              allow-clear
              @press-enter="loadPositions"
            />
            <a-button type="primary" size="small" :disabled="!positionFilter.account_id" @click="loadPositions">
              <template #icon><icon-search /></template>
              查询
            </a-button>
            <a-button size="small" @click="onSyncPositions" :loading="syncingPositions" :disabled="!positionFilter.account_id">
              <template #icon><icon-sync /></template>
              同步持仓
            </a-button>
          </a-space>
        </div>

        <a-table
          row-key="position_id"
          size="small"
          :bordered="{ cell: true }"
          :loading="positionLoading"
          :data="positions"
          :pagination="false"
          :scroll="{ x: 'max-content' }"
        >
          <template #columns>
            <a-table-column title="账户" :width="120">
              <template #cell="{ record }">{{ accountName(record.account_id) }}</template>
            </a-table-column>
            <a-table-column title="交易所" data-index="exchange" :width="90" />
            <a-table-column title="交易对" data-index="symbol" :width="120" />
            <a-table-column title="方向" :width="70">
              <template #cell="{ record }">
                <a-tag size="small" :color="record.pos_side === 'long' ? 'red' : record.pos_side === 'short' ? 'green' : 'gray'">
                  {{ record.pos_side || '-' }}
                </a-tag>
              </template>
            </a-table-column>
            <a-table-column title="数量" data-index="quantity" :width="120" />
            <a-table-column title="均价" data-index="avg_price" :width="120" />
            <a-table-column title="杠杆" data-index="leverage" :width="70" />
            <a-table-column title="保证金" data-index="margin" :width="120" />
            <a-table-column title="强平价" data-index="liq_price" :width="120" />
            <a-table-column title="未实现盈亏" :width="120">
              <template #cell="{ record }">
                <span :style="{ color: pnlColor(record.unrealized_pnl) }">{{ record.unrealized_pnl || '-' }}</span>
              </template>
            </a-table-column>
            <a-table-column title="已实现盈亏" :width="120">
              <template #cell="{ record }">
                <span :style="{ color: pnlColor(record.realized_pnl) }">{{ record.realized_pnl || '-' }}</span>
              </template>
            </a-table-column>
            <a-table-column title="更新时间" :width="170">
              <template #cell="{ record }">{{ formatTimestamp(record.updated_at) }}</template>
            </a-table-column>
          </template>
          <template #empty>
            <a-empty :description="positionFilter.account_id ? '暂无持仓' : '暂无账户'" />
          </template>
        </a-table>
      </section>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue';
import { Message } from '@arco-design/web-vue';
import {
  getBalances, listAccounts, listPositions, syncBalances, syncPositions,
  accountTypeLabels, formatTimestamp,
} from '@/api/trade';
import type { Account, Balance, Position } from '@/api/trade/types';

defineOptions({ name: 'position-detail' });

const accounts = ref<Account[]>([]);
const positionAccountTabs = computed(() => accounts.value);
const positionAccountTabsExpanded = ref(false);
const selectedAccount = computed(() => (
  accounts.value.find((acc) => acc.account_id === positionFilter.account_id) || null
));

function togglePositionAccountTabs() {
  positionAccountTabsExpanded.value = !positionAccountTabsExpanded.value;
}

async function loadAccounts() {
  const rsp = await listAccounts({ page: { page: 1, size: 200 } });
  accounts.value = rsp.accounts || [];
}

function defaultPositionAccountID(): string {
  return accounts.value.find((acc) => acc.account_type === 2)?.account_id
    || accounts.value.find((acc) => acc.is_default)?.account_id
    || accounts.value[0]?.account_id
    || '';
}

function positionAccountCandidates(): string[] {
  const ids: string[] = [];
  const add = (id?: string) => {
    if (id && !ids.includes(id)) {
      ids.push(id);
    }
  };
  accounts.value
    .filter((acc) => acc.account_type === 2)
    .forEach((acc) => add(acc.account_id));
  add(defaultPositionAccountID());
  add(accounts.value[0]?.account_id);
  return ids;
}

async function onPositionAccountTabClick(accountID: string) {
  if (!accountID || positionFilter.account_id === accountID) {
    return;
  }
  positionFilter.account_id = accountID;
  await Promise.all([
    loadPositions(),
    loadBalancesForAccount(accountID),
  ]);
}

function accountName(accountId: string): string {
  const acc = accounts.value.find((a) => a.account_id === accountId);
  return acc ? acc.account_name : accountId || '-';
}

function pnlColor(val?: string): string {
  if (!val) return '';
  const n = parseFloat(val);
  if (isNaN(n)) return '';
  return n > 0 ? '#f53f3f' : n < 0 ? '#00b42a' : '';
}

// ========== 持仓列表 ==========
const positions = ref<Position[]>([]);
const positionLoading = ref(false);
const syncingPositions = ref(false);
const positionFilter = reactive({ account_id: '', symbol: '' });

async function loadPositions() {
  if (!positionFilter.account_id) {
    positions.value = [];
    return;
  }
  positionLoading.value = true;
  try {
    const rsp = await listPositions(positionFilter.account_id, positionFilter.symbol || undefined);
    positions.value = rsp.positions || [];
  } finally {
    positionLoading.value = false;
  }
}

async function onSyncPositions() {
  if (!positionFilter.account_id) {
    Message.warning('请先选择账户');
    return;
  }
  await syncPositionsForCurrent(true);
}

async function syncPositionsForCurrent(showMessage: boolean): Promise<number> {
  syncingPositions.value = true;
  try {
    const rsp = await syncPositions(positionFilter.account_id, positionFilter.symbol || undefined);
    positions.value = rsp.positions || [];
    await loadBalancesForAccount(positionFilter.account_id);
    if (showMessage) {
      Message.success(`已同步 ${positions.value.length} 条持仓`);
    }
    return positions.value.length;
  } finally {
    syncingPositions.value = false;
  }
}

// ========== 余额快照 ==========
const accountBalanceMap = reactive<Record<string, Balance[]>>({});
const balanceLoadingMap = reactive<Record<string, boolean>>({});
const syncingBalances = ref(false);

const selectedAccountBalances = computed(() => nonZeroBalances(positionFilter.account_id));
const selectedTopBalances = computed(() => selectedAccountBalances.value.slice(0, 12));
const selectedBalanceCount = computed(() => balanceCount(positionFilter.account_id));
const selectedNonZeroBalanceCount = computed(() => selectedAccountBalances.value.length);
const selectedBalanceLoading = computed(() => Boolean(balanceLoadingMap[positionFilter.account_id]));

async function loadBalancePreviews() {
  await Promise.allSettled(accounts.value.map((account) => loadBalancesForAccount(account.account_id)));
}

async function loadBalancesForAccount(accountID: string) {
  if (!accountID) return;
  balanceLoadingMap[accountID] = true;
  try {
    const rsp = await getBalances(accountID);
    accountBalanceMap[accountID] = rsp.balances || [];
  } finally {
    balanceLoadingMap[accountID] = false;
  }
}

async function loadSelectedBalances() {
  if (!positionFilter.account_id) {
    return;
  }
  await loadBalancesForAccount(positionFilter.account_id);
}

async function syncBalancesForCurrent() {
  if (!positionFilter.account_id) {
    Message.warning('请先选择账户');
    return;
  }
  syncingBalances.value = true;
  try {
    const rsp = await syncBalances(positionFilter.account_id);
    accountBalanceMap[positionFilter.account_id] = rsp.balances || [];
    Message.success(`已同步 ${nonZeroBalanceCount(positionFilter.account_id)} 个非零余额`);
  } finally {
    syncingBalances.value = false;
  }
}

function balanceCount(accountID: string) {
  return accountBalanceMap[accountID]?.length || 0;
}

function nonZeroBalances(accountID: string) {
  return (accountBalanceMap[accountID] || [])
    .filter((item) => hasNonZeroBalance(item))
    .sort((a, b) => balancePriority(b) - balancePriority(a));
}

function nonZeroBalanceCount(accountID: string) {
  return nonZeroBalances(accountID).length;
}

function balancePriority(item: Balance) {
  const preferred: Record<string, number> = { USDT: 1000, USDC: 900, BTC: 800, ETH: 700, BNB: 600, SOL: 500 };
  const amount = Number.parseFloat(balanceDisplayAmount(item));
  return (preferred[item.currency] || 0) + (Number.isFinite(amount) && amount > 0 ? Math.min(amount, 100) : 0);
}

function hasNonZeroBalance(item: Balance) {
  return [item.total, item.available, item.frozen].some((value) => isNonZeroAmount(value));
}

function balanceDisplayAmount(item: Balance) {
  if (isNonZeroAmount(item.total)) return item.total;
  if (isNonZeroAmount(item.available)) return item.available;
  return item.frozen || '0';
}

function isNonZeroAmount(value?: string) {
  const parsed = Number.parseFloat(value || '0');
  return Number.isFinite(parsed) && Math.abs(parsed) > 0;
}

function formatBalanceAmount(value?: string) {
  const raw = (value || '0').trim();
  if (!raw) return '0';
  if (!raw.includes('.')) return raw;
  const trimmed = raw.replace(/(\.\d*?[1-9])0+$/, '$1').replace(/\.0+$/, '');
  return trimmed.length > 16 ? `${trimmed.slice(0, 16)}...` : trimmed;
}

async function loadInitialPositions() {
  const candidates = positionAccountCandidates();
  for (const accountID of candidates) {
    positionFilter.account_id = accountID;
    const count = await syncPositionsForCurrent(false);
    if (count > 0) {
      return;
    }
  }
}

onMounted(async () => {
  await loadAccounts();
  const balancePreviewPromise = loadBalancePreviews();
  await loadInitialPositions();
  await balancePreviewPromise;
});
</script>

<style scoped>
.positions-panel {
  display: flex;
  flex-direction: column;
  gap: 14px;
}

.account-tabs-block {
  position: relative;
  padding-bottom: 12px;
}

.position-account-tabs {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
  max-height: 42px;
  padding: 4px;
  overflow: hidden;
  border: 1px solid var(--color-border-2);
  border-radius: 6px;
  background: var(--color-fill-1);
}

.position-account-tabs.expanded {
  max-height: none;
}

.account-tabs-toggle {
  position: absolute;
  bottom: -9px;
  left: 50%;
  z-index: 2;
  width: 34px;
  min-width: 34px;
  height: 18px;
  min-height: 18px;
  padding: 0;
  color: rgb(var(--primary-6));
  background: var(--color-bg-1);
  border: 1px solid var(--color-border-2);
  border-radius: 999px;
  box-shadow: 0 1px 4px rgba(15, 23, 42, 0.1);
  transform: translateX(-50%);
}

.account-tabs-toggle:hover {
  background: var(--color-fill-2);
}

.account-tabs-toggle :deep(.arco-btn-icon) {
  margin: 0;
  font-size: 12px;
}

.position-account-tab {
  display: inline-flex;
  flex: 0 0 auto;
  align-items: center;
  gap: 8px;
  min-height: 32px;
  padding: 5px 12px;
  color: var(--color-text-2);
  white-space: nowrap;
  cursor: pointer;
  background: transparent;
  border: 1px solid transparent;
  border-radius: 4px;
  outline: none;
  transition: background-color 0.15s ease, border-color 0.15s ease, color 0.15s ease;
}

.position-account-tab:hover {
  color: rgb(var(--primary-6));
  background: var(--color-fill-2);
}

.position-account-tab.active {
  color: rgb(var(--primary-6));
  background: var(--color-bg-1);
  border-color: rgb(var(--primary-6));
}

.account-tab-name {
  max-width: 180px;
  overflow: hidden;
  font-weight: 600;
  text-overflow: ellipsis;
}

.account-tab-type {
  color: var(--color-text-3);
  font-size: 12px;
}

.account-tab-balance {
  color: var(--color-text-3);
  font-size: 12px;
}

.account-balance-panel {
  display: flex;
  flex-direction: column;
  gap: 10px;
  padding: 12px;
  border: 1px solid var(--color-border-2);
  border-radius: 6px;
  background: var(--color-bg-1);
}

.account-balance-head {
  display: flex;
  gap: 12px;
  align-items: center;
  justify-content: space-between;
}

.account-balance-head strong {
  margin-left: 8px;
  color: var(--color-text-1);
  font-size: 14px;
}

.balance-label,
.balance-total {
  color: var(--color-text-3);
  font-size: 12px;
}

.balance-total {
  margin-left: 10px;
}

.balance-chip-list {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
}

.balance-chip {
  display: inline-flex;
  gap: 6px;
  align-items: baseline;
  max-width: 220px;
  padding: 4px 8px;
  color: var(--color-text-2);
  background: var(--color-fill-1);
  border: 1px solid var(--color-border-1);
  border-radius: 4px;
}

.balance-chip b {
  color: var(--color-text-1);
  font-weight: 600;
}

.balance-chip em {
  overflow: hidden;
  color: var(--color-text-2);
  font-style: normal;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.balance-empty {
  color: var(--color-text-3);
  font-size: 13px;
}

.page-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 8px;
}

.page-head h2 { margin: 0; font-size: 20px; font-weight: 600; }
.position-toolbar { display: flex; align-items: center; gap: 12px; margin-bottom: 8px; }

@media (max-width: 760px) {
  .account-tabs-block,
  .account-balance-head,
  .position-toolbar {
    align-items: stretch;
    flex-direction: column;
  }
}
</style>
