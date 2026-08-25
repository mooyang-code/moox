<template>
  <div class="moox-page">
    <div class="moox-inner">
      <div class="page-head">
        <h2>Exchange 账户</h2>
        <a-space>
          <a-button :loading="loading" @click="loadAccounts">
            <template #icon><icon-refresh /></template>
            刷新
          </a-button>
          <a-button type="primary" status="success" @click="openCreate">
            <template #icon><icon-plus /></template>
            新增账户
          </a-button>
        </a-space>
      </div>

      <a-table
        row-key="exchange_account_id"
        :data="accounts"
        :loading="loading"
        :pagination="pagination"
        :scroll="{ x: 'max-content' }"
        @page-change="changePage"
      >
        <template #columns>
          <a-table-column title="账户">
            <template #cell="{ record }">
              <strong>{{ record.name }}</strong>
              <div class="muted">{{ record.exchange_account_id }}</div>
            </template>
          </a-table-column>
          <a-table-column title="Exchange">
            <template #cell="{ record }">{{ exchangeLabels[record.exchange] }}</template>
          </a-table-column>
          <a-table-column title="账户类型">
            <template #cell="{ record }">{{ marketTypeLabels[record.market_type] }}</template>
          </a-table-column>
          <a-table-column title="执行模式">
            <template #cell="{ record }">{{ executionModeLabels[record.execution_mode] }}</template>
          </a-table-column>
          <a-table-column title="环境">
            <template #cell="{ record }">{{ environmentLabels[record.environment] }}</template>
          </a-table-column>
          <a-table-column title="状态">
            <template #cell="{ record }">
              <a-space>
                <a-tag :color="record.ready ? 'green' : 'gray'">{{ record.ready ? "Ready" : "Not Ready" }}</a-tag>
                <span>{{ record.status }}</span>
              </a-space>
            </template>
          </a-table-column>
          <a-table-column title="结算资产" data-index="settlement_asset" />
          <a-table-column title="最近同步">
            <template #cell="{ record }">{{ formatTimestamp(record.last_sync_at) }}</template>
          </a-table-column>
          <a-table-column title="最近错误" data-index="last_error" ellipsis />
          <a-table-column title="操作" fixed="right" :width="100">
            <template #cell="{ record }">
              <a-space>
                <a-button size="mini" :loading="syncingId === record.exchange_account_id" @click="sync(record)">
                  <template #icon><icon-sync /></template>
                  同步
                </a-button>
                <a-popconfirm
                  v-if="record.execution_mode === 1 && record.status === 'ENABLED'"
                  content="关闭后不可恢复，仅保留历史查询。"
                  @ok="closePaper(record)"
                >
                  <a-button size="mini" status="danger">关闭 Paper</a-button>
                </a-popconfirm>
              </a-space>
            </template>
          </a-table-column>
        </template>
      </a-table>

      <a-modal v-model:visible="createVisible" :title="form.execution_mode === 1 ? '创建 Paper 模拟' : '新增 Live 账户'" @ok="create">
        <a-form :model="form" auto-label-width>
          <a-form-item label="名称" required><a-input v-model="form.name" /></a-form-item>
          <a-form-item label="Exchange" required>
            <a-select v-model="form.exchange">
              <a-option :value="1">Binance</a-option>
              <a-option :value="2">OKX</a-option>
            </a-select>
          </a-form-item>
          <a-form-item label="账户类型" required>
            <a-radio-group v-model="form.market_type" type="button">
              <a-radio :value="1">SPOT</a-radio>
              <a-radio :value="2">SWAP</a-radio>
            </a-radio-group>
          </a-form-item>
          <a-form-item label="执行模式" required>
            <a-radio-group v-model="form.execution_mode" type="button">
              <a-radio :value="1">Paper</a-radio>
              <a-radio :value="2">Live</a-radio>
            </a-radio-group>
          </a-form-item>
          <a-form-item label="环境" required>
            <a-radio-group v-model="form.environment" type="button">
              <a-radio v-if="form.execution_mode === 1" :value="1">Paper</a-radio>
              <template v-else>
                <a-radio :value="2">Testnet</a-radio>
                <a-radio :value="3">Production</a-radio>
              </template>
            </a-radio-group>
          </a-form-item>
          <a-form-item v-if="form.execution_mode === 2" label="Credential Secret ID" required>
            <a-input v-model="form.credential_secret_id" />
          </a-form-item>
          <a-form-item label="结算资产" required><a-input v-model="form.settlement_asset" /></a-form-item>
          <a-form-item v-if="form.execution_mode === 1" label="初始资金" required>
            <a-input v-model="form.initial_balance" suffix="USDT" />
          </a-form-item>
          <a-form-item v-if="form.market_type === 2" label="保证金模式">
            <a-input model-value="Cross" disabled />
          </a-form-item>
          <a-form-item label="同步 Symbol">
            <a-input v-model="syncSymbols" placeholder="BTCUSDT, ETHUSDT" />
          </a-form-item>
        </a-form>
      </a-modal>
    </div>
  </div>
</template>

<script setup lang="ts">
import { onMounted, reactive, ref, watch } from "vue";
import { Message } from "@arco-design/web-vue";
import {
  createAccount,
  createPaperSimulation,
  closePaperSimulation,
  environmentLabels,
  exchangeLabels,
  executionModeLabels,
  formatTimestamp,
  listAccounts,
  marketTypeLabels,
  syncAccount
} from "@/api/trade";
import type { ExchangeAccount } from "@/api/trade/types";

defineOptions({ name: "account-overview" });

const accounts = ref<ExchangeAccount[]>([]);
const loading = ref(false);
const syncingId = ref("");
const createVisible = ref(false);
const syncSymbols = ref("");
const pagination = reactive({ current: 1, pageSize: 20, total: 0 });
const form = reactive({
  name: "",
  exchange: 1 as 1 | 2,
  market_type: 1 as 1 | 2,
  execution_mode: 1 as 1 | 2,
  environment: 1 as 1 | 2 | 3,
  credential_secret_id: "",
  settlement_asset: "USDT",
  margin_mode: "CROSS",
  initial_balance: "100000"
});

watch(
  () => form.execution_mode,
  mode => {
    form.environment = mode === 1 ? 1 : 2;
  }
);

async function loadAccounts() {
  loading.value = true;
  try {
    const response = await listAccounts({ page: { page: pagination.current, size: pagination.pageSize } });
    accounts.value = response.accounts || [];
    pagination.total = response.page_result?.total || 0;
  } finally {
    loading.value = false;
  }
}

function changePage(page: number) {
  pagination.current = page;
  loadAccounts();
}

async function sync(account: ExchangeAccount) {
  syncingId.value = account.exchange_account_id;
  try {
    const response = await syncAccount(account.exchange_account_id);
    Message.success(
      `同步完成：${response.fills_ingested} fills，${response.orders_updated} orders，${response.positions_updated} positions`
    );
    await loadAccounts();
  } finally {
    syncingId.value = "";
  }
}

async function create() {
  const credentialMissing = form.execution_mode === 2 && !form.credential_secret_id.trim();
  if (!form.name.trim() || credentialMissing || !form.settlement_asset.trim() || (form.execution_mode === 1 && !form.initial_balance.trim())) {
    Message.warning("请填写所有必填字段");
    return false;
  }
  if (form.execution_mode === 1) {
    await createPaperSimulation({
      account_name: form.name.trim(),
      logical_account_name: `${form.name.trim()}-logical`,
      exchange: form.exchange,
      market_type: form.market_type,
      settlement_asset: form.settlement_asset.trim().toUpperCase(),
      margin_mode: form.market_type === 2 ? form.margin_mode : "",
      initial_balance: form.initial_balance.trim(),
      maker_fee_rate: "0",
      taker_fee_rate: "0",
      slippage_bps: "0"
    });
  } else {
    await createAccount({
      ...form,
      name: form.name.trim(),
      credential_secret_id: form.credential_secret_id.trim(),
      settlement_asset: form.settlement_asset.trim().toUpperCase(),
      margin_mode: form.market_type === 2 ? form.margin_mode : "",
      sync_symbols: syncSymbols.value
        .split(",")
        .map(value => value.trim().toUpperCase())
        .filter(Boolean)
    });
  }
  createVisible.value = false;
  await loadAccounts();
  return true;
}

function openCreate() {
  form.name = "";
  form.execution_mode = 1;
  form.initial_balance = "100000";
  createVisible.value = true;
}

async function closePaper(account: ExchangeAccount) {
  await closePaperSimulation(account.exchange_account_id);
  Message.success("Paper 模拟已关闭");
  await loadAccounts();
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

.muted {
  margin-top: 4px;
  color: var(--color-text-3);
  font-size: 12px;
}
</style>
