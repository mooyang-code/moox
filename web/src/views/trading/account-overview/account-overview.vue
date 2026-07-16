<template>
  <div class="moox-page">
    <div class="moox-inner">
      <!-- 账户列表 -->
      <div class="page-head">
        <h2>资金账户</h2>
        <a-space>
          <a-input-search
            v-model="keyword"
            placeholder="搜索账户名称"
            allow-clear
            style="width: 200px"
            @search="loadAccounts"
            @clear="loadAccounts"
          />
          <a-button type="primary" status="success" @click="openCreateAccount">
            <template #icon><icon-plus /></template>
            新增账户
          </a-button>
          <a-button @click="onSyncExchangeAccounts" :loading="syncingAccounts">
            <template #icon><icon-sync /></template>
            同步秘钥账户
          </a-button>
        </a-space>
      </div>

      <div class="account-spin">
        <div v-if="loading && !accounts.length" class="account-card-grid">
          <section v-for="index in 4" :key="index" class="account-card account-card-skeleton">
            <div class="account-card-topline" />
            <a-skeleton :animation="true">
              <a-skeleton-line :rows="6" :widths="['58%', '82%', '42%', '70%', '96%', '64%']" />
            </a-skeleton>
          </section>
        </div>
        <div v-else-if="accounts.length" class="account-card-grid">
          <section v-for="record in accounts" :key="record.account_id" class="account-card">
            <div class="account-card-topline" />
            <div class="account-card-header">
              <div class="account-title-block">
                <div class="account-name-row">
                  <h3>{{ record.account_name }}</h3>
                  <a-tag v-if="record.is_default" size="small" color="blue">默认</a-tag>
                </div>
                <div class="account-subtitle">{{ record.account_id }}</div>
              </div>
              <a-tag size="small" :color="accountStatusColors[record.status]">{{ accountStatusLabels[record.status] }}</a-tag>
            </div>

            <div class="account-meta-row">
              <span>{{ accountTypeLabels[record.account_type] || record.account_type }}</span>
              <span>{{ record.base_currency || '-' }}</span>
              <span>{{ record.channel_id ? '已绑定通道' : '未绑定通道' }}</span>
            </div>

            <div class="balance-panel">
              <div class="balance-panel-head">
                <div>
                  <span class="balance-label">余额币种</span>
                  <strong>{{ nonZeroBalanceCount(record.account_id) }}</strong>
                  <span class="balance-total">/ {{ balanceCount(record.account_id) }}</span>
                </div>
                <a-button
                  size="mini"
                  type="text"
                  :loading="balancePreviewLoading[record.account_id]"
                  @click="syncAccountBalancePreview(record)"
                >
                  <template #icon><icon-sync /></template>
                </a-button>
              </div>
              <div v-if="topBalances(record.account_id).length" class="balance-chip-list">
                <span v-for="item in topBalances(record.account_id)" :key="item.currency" class="balance-chip">
                  <b>{{ item.currency }}</b>
                  <em>{{ formatBalanceAmount(item.total || item.available) }}</em>
                </span>
              </div>
              <div v-else class="balance-empty">
                {{ balancePreviewLoading[record.account_id] ? '读取余额中' : '暂无非零余额' }}
              </div>
            </div>

            <div class="account-card-footer">
              <span>创建 {{ formatTimestamp(record.created_at) }}</span>
              <a-space class="account-card-actions" wrap>
                <a-button size="mini" type="text" status="danger" @click="openLiquidation(record)">一键清仓</a-button>
                <a-button size="mini" type="text" @click="openDetail(record)">详情</a-button>
                <a-button size="mini" type="text" @click="openBalances(record)">余额</a-button>
                <a-button size="mini" type="text" @click="openFundFlows(record)">流水</a-button>
                <a-button size="mini" type="text" @click="openApiKeys(record)">API</a-button>
                <a-button size="mini" type="text" @click="openEditAccount(record)">编辑</a-button>
                <a-popconfirm content="确定删除该账户？" @ok="onDeleteAccount(record)">
                  <a-button size="mini" type="text" status="danger">删除</a-button>
                </a-popconfirm>
              </a-space>
            </div>
          </section>
        </div>
        <a-empty v-else />
      </div>

      <div class="account-pagination">
        <a-pagination
          :current="pagination.current"
          :page-size="pagination.pageSize"
          :total="pagination.total"
          :show-total="pagination.showTotal"
          :show-page-size="pagination.showPageSize"
          :page-size-options="pagination.pageSizeOptions"
          @change="onPageChange"
          @page-size-change="onPageSizeChange"
        />
      </div>

      <!-- 新增/编辑账户弹窗 -->
      <a-modal v-model:visible="accountModalVisible" width="560px" :title="editingAccount ? '编辑账户' : '新增账户'" @ok="submitAccount">
        <a-form :model="accountForm" auto-label-width>
          <a-form-item field="account_name" label="账户名称" required>
            <a-input v-model="accountForm.account_name" placeholder="如：币币主账户" />
          </a-form-item>
          <a-form-item field="account_type" label="账户类型" required>
            <a-select v-model="accountForm.account_type" :disabled="editingAccount">
              <a-option :value="0">现货</a-option>
              <a-option :value="1">杠杆</a-option>
              <a-option :value="2">合约</a-option>
              <a-option :value="3">模拟</a-option>
            </a-select>
          </a-form-item>
          <a-form-item v-if="!editingAccount" field="channel_id" label="绑定通道">
            <a-input v-model="accountForm.channel_id" placeholder="可空，后续可绑定" />
          </a-form-item>
          <a-form-item field="base_currency" label="基础币种">
            <a-input v-model="accountForm.base_currency" placeholder="如：USDT" />
          </a-form-item>
          <a-form-item v-if="editingAccount" field="status" label="状态">
            <a-select v-model="accountForm.status">
              <a-option :value="0">禁用</a-option>
              <a-option :value="1">正常</a-option>
              <a-option :value="2">冻结</a-option>
              <a-option :value="3">只读</a-option>
            </a-select>
          </a-form-item>
          <a-form-item v-if="editingAccount" field="is_default" label="设为默认">
            <a-switch v-model="accountForm.is_default" />
          </a-form-item>
          <a-form-item field="remark" label="备注">
            <a-textarea v-model="accountForm.remark" :auto-size="{ minRows: 2, maxRows: 4 }" />
          </a-form-item>
        </a-form>
      </a-modal>

      <!-- 余额弹窗 -->
      <a-modal v-model:visible="balanceModalVisible" width="720px" :title="`账户余额 - ${selectedAccount?.account_name || ''}`" :footer="false">
        <div style="margin-bottom: 12px;">
          <a-space>
            <a-button type="primary" size="small" @click="onSyncBalances" :loading="syncing">
              <template #icon><icon-sync /></template>
              从交易所同步
            </a-button>
            <a-button size="small" @click="loadBalances">
              <template #icon><icon-refresh /></template>
              刷新
            </a-button>
          </a-space>
        </div>
        <a-table row-key="currency" size="small" :bordered="{ cell: true }" :loading="balanceLoading" :data="balances" :pagination="false">
          <template #columns>
            <a-table-column title="币种" data-index="currency" :width="100" />
            <a-table-column title="可用" data-index="available" :width="160" />
            <a-table-column title="冻结" data-index="frozen" :width="160" />
            <a-table-column title="总额" data-index="total" :width="160" />
          </template>
        </a-table>
      </a-modal>

      <!-- 一键清仓弹窗 -->
      <a-modal
        v-model:visible="liquidationModalVisible"
        width="900px"
        :title="`一键清仓 - ${selectedAccount?.account_name || ''}`"
        :mask-closable="!liquidationSubmitting"
        :esc-to-close="!liquidationSubmitting"
        :closable="!liquidationSubmitting"
      >
        <a-alert type="warning" show-icon class="liquidation-alert">
          将按当前账户持仓生成市价单；现货卖出非稳定币可用余额，合约按持仓方向平仓。提交前请再次确认数量与交易对。
        </a-alert>
        <a-alert v-if="liquidationHiddenCount > 0" type="info" show-icon class="liquidation-alert">
          已隐藏 {{ liquidationHiddenCount }} 个无法卖出或转换的小尾巴余额。
        </a-alert>

        <div class="liquidation-toolbar">
          <div>
            <span class="liquidation-count">待清仓标的 {{ liquidationTargets.length }}</span>
            <span class="liquidation-account">{{ selectedAccount?.account_id || '-' }}</span>
          </div>
          <a-button size="small" :loading="liquidationLoading" :disabled="liquidationSubmitting" @click="reloadLiquidationTargets">
            <template #icon><icon-refresh /></template>
            刷新持仓
          </a-button>
        </div>

        <a-table
          row-key="id"
          size="small"
          :bordered="{ cell: true }"
          :loading="liquidationLoading"
          :data="liquidationTargets"
          :pagination="false"
          :scroll="{ x: 'max-content' }"
        >
          <template #columns>
            <a-table-column title="标的" data-index="source_name" :width="140" />
            <a-table-column title="交易对" data-index="symbol" :width="120" />
            <a-table-column title="方向" :width="110">
              <template #cell="{ record }">
                <a-tag size="small" :color="record.side === 1 ? 'red' : 'green'">{{ record.side_label }}</a-tag>
              </template>
            </a-table-column>
            <a-table-column title="数量" :width="160">
              <template #cell="{ record }">{{ formatBalanceAmount(record.quantity) }}</template>
            </a-table-column>
            <a-table-column title="可用/持仓" :width="180">
              <template #cell="{ record }">
                <span v-if="record.source_type !== 'swap_position'">
                  可用 {{ formatBalanceAmount(record.available) }}
                  <span class="liquidation-muted">冻结 {{ formatBalanceAmount(record.frozen) }}</span>
                </span>
                <span v-else>{{ record.pos_side || 'BOTH' }}</span>
              </template>
            </a-table-column>
            <a-table-column title="说明" data-index="note" :width="180" />
            <a-table-column title="状态" :width="180">
              <template #cell="{ record }">
                <a-tag size="small" :color="liquidationResultColor(record.id)">
                  {{ liquidationResultText(record.id) }}
                </a-tag>
                <span v-if="liquidationResults[record.id]?.message" class="liquidation-result-message">
                  {{ liquidationResults[record.id]?.message }}
                </span>
              </template>
            </a-table-column>
          </template>
        </a-table>

        <div class="liquidation-confirm">
          <span>输入“清仓”确认提交</span>
          <a-input
            v-model="liquidationConfirmText"
            placeholder="清仓"
            :disabled="liquidationSubmitting || liquidationLoading"
            allow-clear
          />
        </div>

        <template #footer>
          <a-space>
            <a-button :disabled="liquidationSubmitting" @click="liquidationModalVisible = false">取消</a-button>
            <a-button
              type="primary"
              status="danger"
              :disabled="liquidationConfirmDisabled"
              :loading="liquidationSubmitting"
              @click="submitLiquidation"
            >
              确认清仓
            </a-button>
          </a-space>
        </template>
      </a-modal>

      <!-- 资金流水弹窗 -->
      <a-modal v-model:visible="fundFlowModalVisible" width="900px" :title="`资金流水 - ${selectedAccount?.account_name || ''}`" :footer="false">
        <div class="filter-bar">
          <a-input v-model="fundFlowFilter.currency" placeholder="币种" style="width: 100px" allow-clear />
          <a-select v-model="fundFlowFilter.biz_type" placeholder="业务类型" style="width: 140px" allow-clear>
            <a-option v-for="(label, val) in bizTypeLabels" :key="val" :value="val">{{ label }}</a-option>
          </a-select>
          <a-button type="primary" size="small" @click="loadFundFlows">查询</a-button>
        </div>
        <a-table
          row-key="flow_id"
          size="small"
          :bordered="{ cell: true }"
          :loading="fundFlowLoading"
          :data="fundFlows"
          :pagination="fundFlowPagination"
          :scroll="{ x: 'max-content' }"
          @page-change="onFundFlowPageChange"
        >
          <template #columns>
            <a-table-column title="时间" :width="170">
              <template #cell="{ record }">{{ formatTimestamp(record.created_at) }}</template>
            </a-table-column>
            <a-table-column title="币种" data-index="currency" :width="80" />
            <a-table-column title="业务" :width="80">
              <template #cell="{ record }">{{ bizTypeLabels[record.biz_type] || record.biz_type }}</template>
            </a-table-column>
            <a-table-column title="方向" :width="70">
              <template #cell="{ record }">
                <a-tag size="small" :color="record.direction > 0 ? 'green' : 'red'">{{ record.direction > 0 ? '增加' : '减少' }}</a-tag>
              </template>
            </a-table-column>
            <a-table-column title="金额" data-index="amount" :width="140" />
            <a-table-column title="余额后" data-index="balance_after" :width="140" />
            <a-table-column title="备注" data-index="remark" :width="140" ellipsis />
          </template>
        </a-table>
      </a-modal>

      <!-- API 凭证弹窗 -->
      <a-modal v-model:visible="apiKeyModalVisible" width="860px" :title="`API 凭证 - ${selectedAccount?.account_name || ''}`" :footer="false">
        <div style="margin-bottom: 12px;">
          <a-button type="primary" status="success" size="small" @click="openCreateApiKey">
            <template #icon><icon-plus /></template>
            新增凭证
          </a-button>
        </div>
        <a-table row-key="api_key_id" size="small" :bordered="{ cell: true }" :loading="apiKeyLoading" :data="apiKeys" :pagination="false">
          <template #columns>
            <a-table-column title="交易所" data-index="exchange" :width="100" />
            <a-table-column title="API Key" data-index="api_key" :width="240" ellipsis />
            <a-table-column title="权限" :width="160">
              <template #cell="{ record }">
                <a-tag v-for="p in (record.permissions || [])" :key="p" size="small">{{ p }}</a-tag>
              </template>
            </a-table-column>
            <a-table-column title="状态" :width="70">
              <template #cell="{ record }">
                <a-tag size="small" :color="record.status === 1 ? 'green' : 'gray'">{{ record.status === 1 ? '正常' : '禁用' }}</a-tag>
              </template>
            </a-table-column>
            <a-table-column title="创建时间" :width="170">
              <template #cell="{ record }">{{ formatTimestamp(record.created_at) }}</template>
            </a-table-column>
            <a-table-column title="操作" :width="80" align="center">
              <template #cell="{ record }">
                <a-popconfirm content="确定删除该凭证？" @ok="onDeleteApiKey(record)">
                  <a-button size="mini" type="text" status="danger">删除</a-button>
                </a-popconfirm>
              </template>
            </a-table-column>
          </template>
        </a-table>

        <!-- 新增 API Key 弹窗 -->
        <a-modal v-model:visible="apiKeyFormVisible" width="520px" title="新增 API 凭证" @ok="submitApiKey">
          <a-form :model="apiKeyForm" auto-label-width>
            <a-form-item field="exchange" label="交易所" required>
              <a-select v-model="apiKeyForm.exchange">
                <a-option value="binance">Binance</a-option>
                <a-option value="okx">OKX</a-option>
              </a-select>
            </a-form-item>
            <a-form-item field="api_key" label="API Key" required>
              <a-input v-model="apiKeyForm.api_key" />
            </a-form-item>
            <a-form-item field="api_secret" label="API Secret" required>
              <a-input-password v-model="apiKeyForm.api_secret" />
            </a-form-item>
            <a-form-item field="passphrase" label="Passphrase">
              <a-input-password v-model="apiKeyForm.passphrase" placeholder="OKX 需要" />
            </a-form-item>
            <a-form-item field="permissions" label="权限">
              <a-select v-model="apiKeyForm.permissions" multiple allow-create>
                <a-option value="read">读取</a-option>
                <a-option value="trade">交易</a-option>
                <a-option value="withdraw">提现</a-option>
              </a-select>
            </a-form-item>
          </a-form>
        </a-modal>
      </a-modal>

      <!-- 账户详情弹窗 -->
      <a-modal v-model:visible="detailModalVisible" width="560px" :title="`账户详情 - ${selectedAccount?.account_name || ''}`" :footer="false">
        <a-descriptions :column="1" bordered size="small">
          <a-descriptions-item label="账户ID">{{ selectedAccount?.account_id }}</a-descriptions-item>
          <a-descriptions-item label="账户名称">{{ selectedAccount?.account_name }}</a-descriptions-item>
          <a-descriptions-item label="类型">{{ accountTypeLabels[selectedAccount?.account_type ?? 0] }}</a-descriptions-item>
          <a-descriptions-item label="基础币种">{{ selectedAccount?.base_currency || '-' }}</a-descriptions-item>
          <a-descriptions-item label="绑定通道">{{ selectedAccount?.channel_id || '-' }}</a-descriptions-item>
          <a-descriptions-item label="状态">{{ accountStatusLabels[selectedAccount?.status ?? 0] }}</a-descriptions-item>
          <a-descriptions-item label="默认账户">{{ selectedAccount?.is_default ? '是' : '否' }}</a-descriptions-item>
          <a-descriptions-item label="备注">{{ selectedAccount?.remark || '-' }}</a-descriptions-item>
          <a-descriptions-item label="创建时间">{{ formatTimestamp(selectedAccount?.created_at) }}</a-descriptions-item>
          <a-descriptions-item label="更新时间">{{ formatTimestamp(selectedAccount?.updated_at) }}</a-descriptions-item>
        </a-descriptions>
      </a-modal>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue';
import { Message } from '@arco-design/web-vue';
import {
  listAccounts, createAccount, updateAccount, deleteAccount,
  syncExchangeAccounts,
  getBalances, syncBalances,
  listFundFlows,
  listApiKeys, createApiKey, deleteApiKey,
  placeOrder, convertDust, syncPositions, listInstruments,
  accountTypeLabels, accountStatusLabels, accountStatusColors,
  bizTypeLabels, formatTimestamp,
} from '@/api/trade';
import type { Account, Balance, FundFlow, ApiKey, AccountType, AccountStatus } from '@/api/trade/types';
import { defaultPagination, applyPageResult } from '@/views/data/shared/metadata-utils';
import {
  buildSpotLiquidationPlan,
  buildSwapLiquidationTargets,
  splitLiquidationTargetsForSubmit,
} from './account-liquidation-utils';
import type { LiquidationResult, LiquidationTarget } from './account-liquidation-utils';

defineOptions({ name: 'account-overview' });

// ========== 账户列表 ==========
const accounts = ref<Account[]>([]);
const loading = ref(false);
const syncingAccounts = ref(false);
const keyword = ref('');
const pagination = reactive(defaultPagination());
const accountBalanceMap = reactive<Record<string, Balance[]>>({});
const balancePreviewLoading = reactive<Record<string, boolean>>({});
let accountLoadSeq = 0;

async function loadAccounts() {
  const loadSeq = ++accountLoadSeq;
  loading.value = true;
  let shouldLoadBalances = false;
  try {
    const rsp = await listAccounts({
      keyword: keyword.value || undefined,
      page: { page: pagination.current, size: pagination.pageSize },
    });
    if (loadSeq !== accountLoadSeq) return;
    accounts.value = rsp.accounts || [];
    applyPageResult(pagination, rsp.page_result);
    shouldLoadBalances = true;
  } finally {
    if (loadSeq === accountLoadSeq) {
      loading.value = false;
    }
  }
  if (shouldLoadBalances && loadSeq === accountLoadSeq) {
    void loadBalancePreviews(loadSeq);
  }
}

function onPageChange(page: number) {
  pagination.current = page;
  loadAccounts();
}

function onPageSizeChange(size: number) {
  pagination.current = 1;
  pagination.pageSize = size;
  loadAccounts();
}

async function onSyncExchangeAccounts() {
  syncingAccounts.value = true;
  try {
    const rsp = await syncExchangeAccounts({ provider: 'binance' });
    Message.success(`已同步 ${rsp.accounts?.length || 0} 个币安账户及资产快照`);
    await loadAccounts();
  } finally {
    syncingAccounts.value = false;
  }
}

async function loadBalancePreviews(loadSeq = accountLoadSeq) {
  await Promise.allSettled(accounts.value.map((account) => loadBalancePreview(account.account_id, loadSeq)));
}

async function loadBalancePreview(accountID: string, loadSeq = accountLoadSeq) {
  if (!accountID) return;
  balancePreviewLoading[accountID] = true;
  try {
    const rsp = await getBalances(accountID);
    if (loadSeq !== accountLoadSeq) return;
    accountBalanceMap[accountID] = rsp.balances || [];
  } finally {
    if (loadSeq === accountLoadSeq) {
      balancePreviewLoading[accountID] = false;
    }
  }
}

async function syncAccountBalancePreview(record: Account) {
  balancePreviewLoading[record.account_id] = true;
  try {
    const rsp = await syncBalances(record.account_id);
    accountBalanceMap[record.account_id] = rsp.balances || [];
    Message.success(`${record.account_name} 余额已同步`);
  } finally {
    balancePreviewLoading[record.account_id] = false;
  }
}

function balanceCount(accountID: string) {
  return accountBalanceMap[accountID]?.length || 0;
}

function nonZeroBalances(accountID: string) {
  return (accountBalanceMap[accountID] || [])
    .filter((item) => isNonZeroAmount(item.total || item.available || item.frozen))
    .sort((a, b) => balancePriority(b) - balancePriority(a));
}

function nonZeroBalanceCount(accountID: string) {
  return nonZeroBalances(accountID).length;
}

function topBalances(accountID: string) {
  return nonZeroBalances(accountID).slice(0, 6);
}

function balancePriority(item: Balance) {
  const preferred: Record<string, number> = { USDT: 1000, USDC: 900, BTC: 800, ETH: 700, BNB: 600, SOL: 500 };
  const amount = Number.parseFloat(item.total || item.available || '0');
  return (preferred[item.currency] || 0) + (Number.isFinite(amount) && amount > 0 ? Math.min(amount, 100) : 0);
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
  return trimmed.length > 12 ? `${trimmed.slice(0, 12)}…` : trimmed;
}

// ========== 账户新增/编辑 ==========
const accountModalVisible = ref(false);
const editingAccount = ref(false);
const accountForm = reactive<{
  account_id: string;
  account_name: string;
  account_type: AccountType;
  channel_id: string;
  base_currency: string;
  status: AccountStatus;
  is_default: boolean;
  remark: string;
}>({
  account_id: '',
  account_name: '',
  account_type: 0,
  channel_id: '',
  base_currency: '',
  status: 1,
  is_default: false,
  remark: '',
});

function resetAccountForm() {
  Object.assign(accountForm, {
    account_id: '', account_name: '', account_type: 0,
    channel_id: '', base_currency: '', status: 1, is_default: false, remark: '',
  });
}

function openCreateAccount() {
  editingAccount.value = false;
  resetAccountForm();
  accountModalVisible.value = true;
}

function openEditAccount(record: Account) {
  editingAccount.value = true;
  Object.assign(accountForm, {
    account_id: record.account_id,
    account_name: record.account_name,
    account_type: record.account_type,
    channel_id: record.channel_id,
    base_currency: record.base_currency,
    status: record.status,
    is_default: record.is_default,
    remark: record.remark,
  });
  accountModalVisible.value = true;
}

async function submitAccount() {
  if (!accountForm.account_name) {
    Message.warning('请输入账户名称');
    return;
  }
  if (editingAccount.value) {
    await updateAccount({
      account_id: accountForm.account_id,
      account_name: accountForm.account_name,
      status: accountForm.status,
      is_default: accountForm.is_default,
      remark: accountForm.remark,
    });
    Message.success('账户已更新');
  } else {
    await createAccount({
      account_name: accountForm.account_name,
      account_type: accountForm.account_type,
      channel_id: accountForm.channel_id || undefined,
      base_currency: accountForm.base_currency || undefined,
      remark: accountForm.remark || undefined,
    });
    Message.success('账户已创建');
  }
  accountModalVisible.value = false;
  await loadAccounts();
}

async function onDeleteAccount(record: Account) {
  await deleteAccount(record.account_id);
  Message.success('账户已删除');
  await loadAccounts();
}

// ========== 账户详情 ==========
const detailModalVisible = ref(false);
const selectedAccount = ref<Account | null>(null);

function openDetail(record: Account) {
  selectedAccount.value = record;
  detailModalVisible.value = true;
}

// ========== 余额 ==========
const balanceModalVisible = ref(false);
const balances = ref<Balance[]>([]);
const balanceLoading = ref(false);
const syncing = ref(false);

function openBalances(record: Account) {
  selectedAccount.value = record;
  balanceModalVisible.value = true;
  loadBalances();
}

async function loadBalances() {
  if (!selectedAccount.value) return;
  balanceLoading.value = true;
  try {
    const rsp = await getBalances(selectedAccount.value.account_id);
    balances.value = rsp.balances || [];
    accountBalanceMap[selectedAccount.value.account_id] = balances.value;
  } finally {
    balanceLoading.value = false;
  }
}

async function onSyncBalances() {
  if (!selectedAccount.value) return;
  syncing.value = true;
  try {
    const rsp = await syncBalances(selectedAccount.value.account_id);
    balances.value = rsp.balances || [];
    accountBalanceMap[selectedAccount.value.account_id] = balances.value;
    Message.success('余额已同步');
  } finally {
    syncing.value = false;
  }
}

// ========== 一键清仓 ==========
const liquidationModalVisible = ref(false);
const liquidationLoading = ref(false);
const liquidationSubmitting = ref(false);
const liquidationTargets = ref<LiquidationTarget[]>([]);
const liquidationResults = reactive<Record<string, LiquidationResult>>({});
const liquidationConfirmText = ref('');
const liquidationHiddenCount = ref(0);
const liquidationConfirmDisabled = computed(() => (
  liquidationLoading.value
  || liquidationSubmitting.value
  || liquidationTargets.value.length === 0
  || liquidationConfirmText.value !== '清仓'
));

function resetLiquidationState() {
  liquidationTargets.value = [];
  liquidationHiddenCount.value = 0;
  liquidationConfirmText.value = '';
  Object.keys(liquidationResults).forEach((key) => delete liquidationResults[key]);
}

function openLiquidation(record: Account) {
  selectedAccount.value = record;
  resetLiquidationState();
  liquidationModalVisible.value = true;
  loadLiquidationTargets(record);
}

function reloadLiquidationTargets() {
  if (!selectedAccount.value) return;
  resetLiquidationState();
  loadLiquidationTargets(selectedAccount.value);
}

async function loadLiquidationTargets(record: Account) {
  liquidationLoading.value = true;
  try {
    if (record.account_type === 2) {
      const rsp = await syncPositions(record.account_id);
      liquidationTargets.value = buildSwapLiquidationTargets(record, rsp.positions || []);
    } else {
      const [balanceResult, instrumentResult] = await Promise.allSettled([
        syncBalances(record.account_id),
        record.channel_id ? listInstruments(record.channel_id, record.account_type) : Promise.resolve({ instruments: [] }),
      ]);
      if (balanceResult.status === 'rejected') {
        throw balanceResult.reason;
      }
      if (instrumentResult.status === 'rejected') {
        Message.warning(`交易规则读取失败：${errorText(instrumentResult.reason)}`);
      }
      const rsp = balanceResult.value;
      const nextBalances = rsp.balances || [];
      const instruments = instrumentResult.status === 'fulfilled' ? instrumentResult.value.instruments || [] : [];
      accountBalanceMap[record.account_id] = nextBalances;
      const plan = buildSpotLiquidationPlan(record, nextBalances, instruments);
      liquidationTargets.value = plan.targets;
      liquidationHiddenCount.value = plan.hidden_count;
    }
  } finally {
    liquidationLoading.value = false;
  }
}

async function submitLiquidation() {
  const account = selectedAccount.value;
  if (!account || liquidationConfirmDisabled.value) return;
  liquidationSubmitting.value = true;
  let successCount = 0;
  let failedCount = 0;
  try {
    const groupedTargets = splitLiquidationTargetsForSubmit(liquidationTargets.value);
    const dustTargets = groupedTargets.dust_targets;
    if (dustTargets.length > 0) {
      dustTargets.forEach((target) => {
        liquidationResults[target.id] = { status: 'submitting' };
      });
      const dustChannelID = dustTargets[0]?.channel_id || '';
      if (!dustChannelID) {
        failedCount += dustTargets.length;
        dustTargets.forEach((target) => {
          liquidationResults[target.id] = { status: 'failed', message: '缺少交易通道' };
        });
      } else {
        try {
          const rsp = await convertDust({
            account_id: account.account_id,
            channel_id: dustChannelID,
            assets: groupedTargets.dust_assets,
          });
          const resultByAsset = new Map((rsp.results || []).map((item) => [item.asset.toUpperCase(), item]));
          const skippedByAsset = new Map((rsp.skipped_results || []).map((item) => [item.asset.toUpperCase(), item]));
          dustTargets.forEach((target) => {
            const result = resultByAsset.get(target.source_name.toUpperCase());
            const skipped = skippedByAsset.get(target.source_name.toUpperCase());
            if (skipped) {
              failedCount += 1;
              liquidationResults[target.id] = {
                status: 'failed',
                message: skipped.reason || '估值过低，Binance 不支持转换，保留小尾巴',
              };
              return;
            }
            if (!result) {
              failedCount += 1;
              liquidationResults[target.id] = {
                status: 'failed',
                message: 'Binance 未返回转换结果，保留小尾巴',
              };
              return;
            }
            successCount += 1;
            liquidationResults[target.id] = {
              status: 'success',
              message: dustTransferMessage(result?.transfered_amount, target.source_name),
            };
          });
        } catch (err) {
          failedCount += dustTargets.length;
          dustTargets.forEach((target) => {
            liquidationResults[target.id] = { status: 'failed', message: liquidationErrorText(err) };
          });
        }
      }
    }

    for (const [idx, target] of groupedTargets.order_targets.entries()) {
      liquidationResults[target.id] = { status: 'submitting' };
      if (!target.channel_id) {
        failedCount += 1;
        liquidationResults[target.id] = { status: 'failed', message: '缺少交易通道' };
        continue;
      }
      try {
        const rsp = await placeOrder({
          account_id: target.account_id,
          channel_id: target.channel_id,
          client_order_id: buildLiquidationClientOrderID(idx),
          symbol: target.symbol,
          market_type: target.market_type,
          side: target.side,
          pos_side: target.pos_side,
          order_type: 1,
          quantity: target.quantity,
          reduce_only: target.reduce_only,
          source: 'one_click_liquidation',
        });
        successCount += 1;
        liquidationResults[target.id] = {
          status: 'success',
          order_id: rsp.order_id,
          exchange_order_id: rsp.exchange_order_id,
          message: rsp.exchange_order_id || rsp.order_id || '已提交',
        };
      } catch (err) {
        failedCount += 1;
        liquidationResults[target.id] = { status: 'failed', message: errorText(err) };
      }
    }
    if (successCount > 0) {
      Message.success(`清仓单已提交 ${successCount} 个${failedCount ? `，失败 ${failedCount} 个` : ''}`);
    } else {
      Message.error('清仓单提交失败');
    }
    await loadBalancePreview(account.account_id);
    if (account.account_type === 2) {
      await syncPositions(account.account_id);
    }
  } finally {
    liquidationSubmitting.value = false;
  }
}

function dustTransferMessage(totalTransfered?: string, asset?: string) {
  if (totalTransfered && totalTransfered !== '0') {
    return `已转为 ${formatBalanceAmount(totalTransfered)} BNB`;
  }
  return `${asset || '资产'} 已提交转换`;
}

function liquidationErrorText(err: unknown) {
  const message = errorText(err);
  if (message.includes('It can only be requested once within 1 hour')) {
    return 'Binance 小额资产转换 1 小时内只能执行一次，请稍后再试';
  }
  if (message.includes('The valuation of the remaining') || message.includes('"code":-5005')) {
    return '部分资产估值过低，Binance 不支持转换，保留小尾巴';
  }
  return message;
}

function buildLiquidationClientOrderID(index: number) {
  const suffix = Math.random().toString(36).slice(2, 8);
  return `liq_${Date.now()}_${index}_${suffix}`;
}

function liquidationResultText(id: string) {
  const result = liquidationResults[id];
  if (!result) return '待提交';
  if (result.status === 'submitting') return '提交中';
  if (result.status === 'success') return '已提交';
  if (result.status === 'failed') return '失败';
  return '待提交';
}

function liquidationResultColor(id: string) {
  const status = liquidationResults[id]?.status;
  if (status === 'submitting') return 'blue';
  if (status === 'success') return 'green';
  if (status === 'failed') return 'red';
  return 'gray';
}

function errorText(err: unknown) {
  if (err instanceof Error) return err.message;
  if (typeof err === 'string') return err;
  return '提交失败';
}

// ========== 资金流水 ==========
const fundFlowModalVisible = ref(false);
const fundFlows = ref<FundFlow[]>([]);
const fundFlowLoading = ref(false);
const fundFlowPagination = reactive(defaultPagination());
const fundFlowFilter = reactive({ currency: '', biz_type: '' });

function openFundFlows(record: Account) {
  selectedAccount.value = record;
  fundFlowModalVisible.value = true;
  fundFlowPagination.current = 1;
  fundFlowFilter.currency = '';
  fundFlowFilter.biz_type = '';
  loadFundFlows();
}

async function loadFundFlows() {
  if (!selectedAccount.value) return;
  fundFlowLoading.value = true;
  try {
    const rsp = await listFundFlows({
      account_id: selectedAccount.value.account_id,
      currency: fundFlowFilter.currency || undefined,
      biz_type: fundFlowFilter.biz_type || undefined,
      page: { page: fundFlowPagination.current, size: fundFlowPagination.pageSize },
    });
    fundFlows.value = rsp.flows || [];
    applyPageResult(fundFlowPagination, rsp.page_result);
  } finally {
    fundFlowLoading.value = false;
  }
}

function onFundFlowPageChange(page: number) {
  fundFlowPagination.current = page;
  loadFundFlows();
}

// ========== API 凭证 ==========
const apiKeyModalVisible = ref(false);
const apiKeys = ref<ApiKey[]>([]);
const apiKeyLoading = ref(false);
const apiKeyFormVisible = ref(false);
const apiKeyForm = reactive<{
  account_id: string;
  exchange: string;
  api_key: string;
  api_secret: string;
  passphrase: string;
  permissions: string[];
}>({
  account_id: '',
  exchange: 'binance',
  api_key: '',
  api_secret: '',
  passphrase: '',
  permissions: ['read'],
});

function openApiKeys(record: Account) {
  selectedAccount.value = record;
  apiKeyModalVisible.value = true;
  loadApiKeys();
}

async function loadApiKeys() {
  if (!selectedAccount.value) return;
  apiKeyLoading.value = true;
  try {
    const rsp = await listApiKeys(selectedAccount.value.account_id);
    apiKeys.value = rsp.api_keys || [];
  } finally {
    apiKeyLoading.value = false;
  }
}

function openCreateApiKey() {
  Object.assign(apiKeyForm, {
    account_id: selectedAccount.value?.account_id || '',
    exchange: 'binance',
    api_key: '',
    api_secret: '',
    passphrase: '',
    permissions: ['read'],
  });
  apiKeyFormVisible.value = true;
}

async function submitApiKey() {
  if (!apiKeyForm.api_key || !apiKeyForm.api_secret) {
    Message.warning('请输入 API Key 和 Secret');
    return;
  }
  await createApiKey({
    account_id: apiKeyForm.account_id,
    exchange: apiKeyForm.exchange,
    api_key: apiKeyForm.api_key,
    api_secret: apiKeyForm.api_secret,
    passphrase: apiKeyForm.passphrase || undefined,
    permissions: apiKeyForm.permissions,
  });
  Message.success('凭证已创建');
  apiKeyFormVisible.value = false;
  await loadApiKeys();
}

async function onDeleteApiKey(record: ApiKey) {
  await deleteApiKey(record.api_key_id);
  Message.success('凭证已删除');
  await loadApiKeys();
}

onMounted(loadAccounts);
</script>

<style scoped>
.page-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: var(--moox-space-2);
}

.page-head h2 {
  margin: 0;
  font-size: 20px;
  font-weight: 600;
}

.account-spin {
  display: block;
  min-height: 240px;
}

.account-card-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(360px, 1fr));
  gap: 14px;
}

.account-card {
  position: relative;
  display: flex;
  min-height: 248px;
  flex-direction: column;
  overflow: hidden;
  border: 1px solid var(--color-border-2);
  border-radius: 8px;
  background:
    linear-gradient(180deg, rgba(255, 255, 255, 0.92), rgba(250, 251, 253, 0.98)),
    repeating-linear-gradient(135deg, rgba(29, 33, 41, 0.025) 0, rgba(29, 33, 41, 0.025) 1px, transparent 1px, transparent 12px);
  box-shadow: 0 8px 22px rgba(29, 33, 41, 0.06);
}

.account-card-skeleton {
  padding-bottom: 16px;
}

.account-card-skeleton :deep(.arco-skeleton) {
  padding: 18px 16px 0;
}

.account-card-topline {
  height: 4px;
  background: linear-gradient(90deg, #165dff 0%, #00b42a 45%, #ff7d00 100%);
}

.account-card-header {
  display: flex;
  gap: 12px;
  align-items: flex-start;
  justify-content: space-between;
  padding: 16px 16px 10px;
}

.account-title-block {
  min-width: 0;
}

.account-name-row {
  display: flex;
  gap: 8px;
  align-items: center;
}

.account-name-row h3 {
  min-width: 0;
  margin: 0;
  overflow: hidden;
  color: #1d2129;
  font-size: 16px;
  font-weight: 650;
  line-height: 22px;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.account-subtitle {
  margin-top: 4px;
  overflow: hidden;
  color: #86909c;
  font-size: 12px;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.account-meta-row {
  display: flex;
  gap: 8px;
  padding: 0 16px 12px;
  flex-wrap: wrap;
}

.account-meta-row span {
  display: inline-flex;
  height: 24px;
  align-items: center;
  padding: 0 8px;
  border: 1px solid #e5e6eb;
  border-radius: 4px;
  background: rgba(255, 255, 255, 0.78);
  color: #4e5969;
  font-size: 12px;
}

.balance-panel {
  margin: 0 16px 14px;
  padding: 12px;
  border: 1px solid #e5e6eb;
  border-radius: 6px;
  background: rgba(247, 248, 250, 0.78);
}

.balance-panel-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  min-height: 26px;
}

.balance-label,
.balance-total {
  color: #86909c;
  font-size: 12px;
}

.balance-panel-head strong {
  margin-left: 8px;
  color: #1d2129;
  font-size: 22px;
  line-height: 26px;
}

.balance-chip-list {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 8px;
  margin-top: 10px;
}

.balance-chip {
  display: flex;
  min-width: 0;
  height: 34px;
  align-items: center;
  justify-content: space-between;
  padding: 0 9px;
  border: 1px solid rgba(22, 93, 255, 0.14);
  border-radius: 5px;
  background: #fff;
}

.balance-chip b {
  color: #1d2129;
  font-size: 12px;
}

.balance-chip em {
  max-width: 110px;
  overflow: hidden;
  color: #165dff;
  font-size: 12px;
  font-style: normal;
  font-weight: 600;
  text-align: right;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.balance-empty {
  display: flex;
  height: 78px;
  align-items: center;
  justify-content: center;
  color: #86909c;
  font-size: 13px;
}

.account-card-footer {
  display: flex;
  gap: 10px;
  align-items: center;
  justify-content: space-between;
  margin-top: auto;
  padding: 12px 16px;
  border-top: 1px solid #f2f3f5;
  background: rgba(255, 255, 255, 0.86);
}

.account-card-footer > span {
  flex: 0 0 auto;
  color: #86909c;
  font-size: 12px;
}

.account-pagination {
  display: flex;
  justify-content: flex-end;
  margin-top: 16px;
}

.filter-bar {
  display: flex;
  gap: 8px;
  align-items: center;
  margin-bottom: 12px;
}

.liquidation-alert {
  margin-bottom: 12px;
}

.liquidation-toolbar {
  display: flex;
  gap: 12px;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 12px;
}

.liquidation-count {
  color: #1d2129;
  font-weight: 600;
}

.liquidation-account,
.liquidation-muted,
.liquidation-result-message {
  margin-left: 8px;
  color: #86909c;
}

.liquidation-confirm {
  display: flex;
  gap: 12px;
  align-items: center;
  justify-content: flex-end;
  margin-top: 14px;
}

.liquidation-confirm .arco-input-wrapper {
  width: 160px;
}

@media (max-width: 768px) {
  .page-head {
    align-items: stretch;
    flex-direction: column;
  }

  .account-card-grid {
    grid-template-columns: 1fr;
  }

  .balance-chip-list {
    grid-template-columns: 1fr;
  }

  .account-card-footer {
    align-items: flex-start;
    flex-direction: column;
  }

  .liquidation-toolbar,
  .liquidation-confirm {
    align-items: stretch;
    flex-direction: column;
  }

  .liquidation-confirm .arco-input-wrapper {
    width: 100%;
  }
}
</style>
