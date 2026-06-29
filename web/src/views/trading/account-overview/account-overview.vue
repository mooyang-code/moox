<template>
  <div class="moox-page">
    <div class="moox-inner">
      <SpaceContextBar />

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
          <a-button type="primary" @click="openCreateAccount">
            <template #icon><icon-plus /></template>
            新增账户
          </a-button>
          <a-button @click="loadAccounts">
            <template #icon><icon-refresh /></template>
            刷新
          </a-button>
        </a-space>
      </div>

      <a-table
        row-key="account_id"
        size="small"
        :bordered="{ cell: true }"
        :loading="loading"
        :data="accounts"
        :pagination="pagination"
        :scroll="{ x: 'max-content' }"
        @page-change="onPageChange"
        @page-size-change="onPageSizeChange"
      >
        <template #columns>
          <a-table-column title="账户名称" data-index="account_name" :width="160" />
          <a-table-column title="类型" :width="80">
            <template #cell="{ record }">
              <a-tag size="small">{{ accountTypeLabels[record.account_type] || record.account_type }}</a-tag>
            </template>
          </a-table-column>
          <a-table-column title="基础币种" data-index="base_currency" :width="90" />
          <a-table-column title="绑定通道" data-index="channel_id" :width="140">
            <template #cell="{ record }">{{ record.channel_id || '-' }}</template>
          </a-table-column>
          <a-table-column title="状态" :width="80">
            <template #cell="{ record }">
              <a-tag size="small" :color="accountStatusColors[record.status]">{{ accountStatusLabels[record.status] }}</a-tag>
            </template>
          </a-table-column>
          <a-table-column title="默认" :width="60">
            <template #cell="{ record }">
              <a-tag v-if="record.is_default" size="small" color="blue">是</a-tag>
              <span v-else>-</span>
            </template>
          </a-table-column>
          <a-table-column title="备注" data-index="remark" :width="140" ellipsis />
          <a-table-column title="创建时间" :width="170">
            <template #cell="{ record }">{{ formatTimestamp(record.created_at) }}</template>
          </a-table-column>
          <a-table-column title="操作" :width="280" align="center" fixed="right">
            <template #cell="{ record }">
              <a-space>
                <a-button size="mini" type="text" @click="openDetail(record)">详情</a-button>
                <a-button size="mini" type="text" @click="openBalances(record)">余额</a-button>
                <a-button size="mini" type="text" @click="openFundFlows(record)">流水</a-button>
                <a-button size="mini" type="text" @click="openApiKeys(record)">API</a-button>
                <a-button size="mini" type="text" @click="openEditAccount(record)">编辑</a-button>
                <a-popconfirm content="确定删除该账户？" @ok="onDeleteAccount(record)">
                  <a-button size="mini" type="text" status="danger">删除</a-button>
                </a-popconfirm>
              </a-space>
            </template>
          </a-table-column>
        </template>
      </a-table>

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
          <a-button type="primary" size="small" @click="openCreateApiKey">
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
import { onMounted, reactive, ref } from 'vue';
import { Message } from '@arco-design/web-vue';
import SpaceContextBar from '@/components/SpaceContextBar/index.vue';
import {
  listAccounts, createAccount, updateAccount, deleteAccount,
  getBalances, syncBalances,
  listFundFlows,
  listApiKeys, createApiKey, deleteApiKey,
  accountTypeLabels, accountStatusLabels, accountStatusColors,
  bizTypeLabels, formatTimestamp,
} from '@/api/trade';
import type { Account, Balance, FundFlow, ApiKey, AccountType, AccountStatus } from '@/api/trade/types';
import { defaultPagination, applyPageResult } from '@/views/data/shared/metadata-utils';

defineOptions({ name: 'account-overview' });

// ========== 账户列表 ==========
const accounts = ref<Account[]>([]);
const loading = ref(false);
const keyword = ref('');
const pagination = reactive(defaultPagination());

async function loadAccounts() {
  loading.value = true;
  try {
    const rsp = await listAccounts({
      keyword: keyword.value || undefined,
      page: { page: pagination.current, size: pagination.pageSize },
    });
    accounts.value = rsp.accounts || [];
    applyPageResult(pagination, rsp.page_result);
  } finally {
    loading.value = false;
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
    Message.success('余额已同步');
  } finally {
    syncing.value = false;
  }
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
  margin-bottom: 16px;
}

.page-head h2 {
  margin: 0;
  font-size: 20px;
  font-weight: 600;
}

.filter-bar {
  display: flex;
  gap: 8px;
  align-items: center;
  margin-bottom: 12px;
}
</style>
