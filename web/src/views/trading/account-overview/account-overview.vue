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
          <a-button type="primary" status="success" @click="createVisible = true">
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
          <a-table-column title="状态">
            <template #cell="{ record }">
              <a-space>
                <a-tag :color="record.paused ? 'orange' : record.ready ? 'green' : 'gray'">
                  {{ record.paused ? "已暂停" : record.ready ? "Ready" : "Not Ready" }}
                </a-tag>
                <span>{{ record.status }}</span>
              </a-space>
            </template>
          </a-table-column>
          <a-table-column title="结算资产" data-index="settlement_asset" />
          <a-table-column title="最近同步">
            <template #cell="{ record }">{{ formatTimestamp(record.last_sync_at) }}</template>
          </a-table-column>
          <a-table-column title="最近错误" data-index="last_error" ellipsis />
          <a-table-column title="操作" fixed="right" :width="190">
            <template #cell="{ record }">
              <a-space>
                <a-button size="mini" :loading="syncingId === record.exchange_account_id" @click="sync(record)">
                  <template #icon><icon-sync /></template>
                  同步
                </a-button>
                <a-button size="mini" :status="record.paused ? 'success' : 'warning'" @click="togglePause(record)">
                  {{ record.paused ? "恢复" : "暂停" }}
                </a-button>
              </a-space>
            </template>
          </a-table-column>
        </template>
      </a-table>

      <a-modal v-model:visible="createVisible" title="新增 Exchange 账户" @ok="create">
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
          <a-form-item v-if="form.execution_mode === 2" label="Credential Secret ID" required>
            <a-input v-model="form.credential_secret_id" />
          </a-form-item>
          <a-form-item label="结算资产" required><a-input v-model="form.settlement_asset" /></a-form-item>
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
import { onMounted, reactive, ref } from "vue";
import { Message, Modal } from "@arco-design/web-vue";
import {
  createAccount,
  exchangeLabels,
  executionModeLabels,
  formatTimestamp,
  listAccounts,
  marketTypeLabels,
  pauseAccount,
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
  credential_secret_id: "",
  settlement_asset: "USDT",
  margin_mode: "CROSS"
});

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

function togglePause(account: ExchangeAccount) {
  const paused = !account.paused;
  Modal.confirm({
    title: paused ? "暂停账户" : "恢复账户",
    content: paused ? "暂停后不会接受新订单。" : "确认恢复该账户的新订单执行？",
    onOk: async () => {
      await pauseAccount({
        exchange_account_id: account.exchange_account_id,
        paused,
        reason: paused ? "manual pause from console" : "manual resume from console"
      });
      await loadAccounts();
    }
  });
}

async function create() {
  const credentialMissing = form.execution_mode === 2 && !form.credential_secret_id.trim();
  if (!form.name.trim() || credentialMissing || !form.settlement_asset.trim()) {
    Message.warning("请填写所有必填字段");
    return false;
  }
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
  createVisible.value = false;
  await loadAccounts();
  return true;
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
