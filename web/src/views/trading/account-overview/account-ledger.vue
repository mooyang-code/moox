<template>
  <div class="account-ledger">
    <div class="ledger-toolbar">
      <a-range-picker v-model="range" value-format="timestamp" :allow-clear="false" class="ledger-range" @change="load" />
      <a-select v-model="typeFilter" placeholder="全部类型" allow-clear class="ledger-type">
        <a-option v-for="item in ledgerTypeOptions" :key="item.value" :value="item.value">{{ item.label }}</a-option>
      </a-select>
      <a-button :loading="loading" @click="load">查询</a-button>
    </div>
    <a-alert v-if="error" type="error" show-icon class="ledger-alert">{{ error }}</a-alert>
    <a-alert v-else-if="hasMore" type="info" show-icon class="ledger-alert">
      只显示时间范围内最新的 {{ entries.length }} 条，更早的记录请缩小时间范围后查询。
    </a-alert>
    <a-table :data="visibleEntries" :loading="loading" :pagination="{ pageSize: 20 }" row-key="id" size="small">
      <template #columns>
        <a-table-column title="时间" :width="170">
          <template #cell="{ record }">{{ formatTimestamp(record.time) }}</template>
        </a-table-column>
        <a-table-column title="类型" :width="110">
          <template #cell="{ record }">{{ ledgerTypeLabel(record.type) }}</template>
        </a-table-column>
        <a-table-column title="金额" :width="150" align="right">
          <template #cell="{ record }">
            <span :class="amountClass(record.amount)">{{ record.amount }}</span>
          </template>
        </a-table-column>
        <a-table-column title="资产" data-index="asset" :width="80" />
        <a-table-column title="标的" :width="140">
          <template #cell="{ record }">{{ record.exchange_symbol || "-" }}</template>
        </a-table-column>
        <a-table-column title="说明">
          <template #cell="{ record }">{{ record.info || record.exchange_type || "-" }}</template>
        </a-table-column>
      </template>
    </a-table>
  </div>
</template>

<script setup lang="ts">
import { computed, ref, watch } from "vue";
import { formatTimestamp, listAccountLedger, type LedgerEntry } from "@/api/trade";
import { amountClass, defaultLedgerRange, ledgerTypeLabel, ledgerTypeOptions } from "./account-ledger";

const props = defineProps<{ tradingAccountId: string }>();

const range = ref<number[]>(defaultLedgerRange());
const typeFilter = ref<string>();
const entries = ref<LedgerEntry[]>([]);
const hasMore = ref(false);
const loading = ref(false);
const error = ref("");
let requestToken = 0;

const visibleEntries = computed(() =>
  typeFilter.value ? entries.value.filter(entry => entry.type === typeFilter.value) : entries.value
);

async function load() {
  const token = ++requestToken;
  const accountId = props.tradingAccountId;
  loading.value = true;
  error.value = "";
  try {
    const rsp = await listAccountLedger({
      trading_account_id: accountId,
      start_time: String(range.value[0]),
      end_time: String(range.value[1]),
      limit: 1000
    });
    // 切换账户或重新查询后，旧请求的结果不能覆盖新结果。
    if (token !== requestToken) return;
    entries.value = rsp.entries || [];
    hasMore.value = Boolean(rsp.has_more);
  } catch (err) {
    if (token !== requestToken) return;
    entries.value = [];
    hasMore.value = false;
    error.value = err instanceof Error ? err.message : String(err);
  } finally {
    if (token === requestToken) loading.value = false;
  }
}

watch(
  () => props.tradingAccountId,
  () => {
    range.value = defaultLedgerRange();
    typeFilter.value = undefined;
    void load();
  },
  { immediate: true }
);
</script>

<style scoped>
.ledger-toolbar {
  display: flex;
  flex-wrap: wrap;
  gap: var(--moox-space-2);
  margin-bottom: var(--moox-space-2);
}

.ledger-range {
  width: 320px;
}

.ledger-type {
  width: 140px;
}

.ledger-alert {
  margin-bottom: var(--moox-space-2);
}

.amount-positive {
  color: rgb(var(--green-6));
}

.amount-negative {
  color: rgb(var(--red-6));
}
</style>
