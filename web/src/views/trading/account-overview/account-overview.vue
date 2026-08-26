<template>
  <div class="moox-page">
    <div class="moox-inner">
      <div class="page-head">
        <h2>Trading 账户</h2>
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
        row-key="trading_account_id"
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
              <div class="muted">{{ record.trading_account_id }}</div>
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
            <template #cell="{ record }">{{ record.paper ? "Paper" : environmentLabels[record.live?.environment || 0] }}</template>
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
                <a-button size="mini" :loading="syncingId === record.trading_account_id" @click="sync(record)">
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
          <a-form-item v-if="form.execution_mode === 2" label="环境" required>
            <a-radio-group v-model="form.environment" type="button">
              <a-radio :value="1">Testnet</a-radio>
              <a-radio :value="2">Production</a-radio>
            </a-radio-group>
          </a-form-item>
          <a-form-item v-if="form.execution_mode === 2" label="Credential Secret ID" required>
            <a-input v-model="form.credential_secret_id" />
          </a-form-item>
          <a-form-item label="结算资产" required><a-input v-model="form.settlement_asset" /></a-form-item>
          <a-form-item v-if="form.execution_mode === 1" label="初始资金" required>
            <a-input v-model="form.initial_balance" suffix="USDT" />
          </a-form-item>
          <template v-if="form.execution_mode === 1">
            <a-form-item label="Maker 费率" required><a-input v-model="form.maker_fee_rate" /></a-form-item>
            <a-form-item label="Taker 费率" required><a-input v-model="form.taker_fee_rate" /></a-form-item>
            <a-form-item label="滑点 (bps)" required><a-input v-model="form.slippage_bps" /></a-form-item>
            <a-form-item label="LogicalAccount 名称" required><a-input v-model="form.logical_account_name" /></a-form-item>
          </template>
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
  createTradingAccount,
  createPaperSimulation,
  closePaperSimulation,
  environmentLabels,
  exchangeLabels,
  executionModeLabels,
  formatTimestamp,
  listTradingAccounts,
  marketTypeLabels,
  syncTradingAccount
} from "@/api/trade";
import type { TradingAccount } from "@/api/trade/types";
import { buildLiveRequest, buildPaperSimulationRequest, type AccountFormModel } from "./account-form";

defineOptions({ name: "account-overview" });

const accounts = ref<TradingAccount[]>([]);
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
  environment: 1 as 1 | 2,
  credential_secret_id: "",
  settlement_asset: "USDT",
  margin_mode: "CROSS",
  initial_balance: "100000",
  maker_fee_rate: "0",
  taker_fee_rate: "0",
  slippage_bps: "0",
  logical_account_name: "",
  sync_symbols: ""
});

watch(
  () => form.execution_mode,
  () => {
    form.environment = 1;
  }
);

async function loadAccounts() {
  loading.value = true;
  try {
    const response = await listTradingAccounts({ page: { page: pagination.current, size: pagination.pageSize } });
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

async function sync(account: TradingAccount) {
  syncingId.value = account.trading_account_id;
  try {
    const response = await syncTradingAccount(account.trading_account_id);
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
  if (!form.name.trim() || credentialMissing || !form.settlement_asset.trim() || (form.execution_mode === 1 && (!form.initial_balance.trim() || !form.logical_account_name.trim()))) {
    Message.warning("请填写所有必填字段");
    return false;
  }
  if (form.execution_mode === 1) {
    await createPaperSimulation(buildPaperSimulationRequest(form as AccountFormModel));
  } else {
    form.sync_symbols = syncSymbols.value;
    await createTradingAccount(buildLiveRequest(form as AccountFormModel));
  }
  createVisible.value = false;
  await loadAccounts();
  return true;
}

function openCreate() {
  form.name = "";
  form.execution_mode = 1;
  form.initial_balance = "100000";
  form.logical_account_name = "";
  createVisible.value = true;
}

async function closePaper(account: TradingAccount) {
  await closePaperSimulation(account.trading_account_id);
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
