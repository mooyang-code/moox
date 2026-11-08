<template>
  <SectionBlock title="收益拆解">
    <template #sub>
      <template v-if="view">{{ view.fillCount }} 笔成交 · </template>各资产分别列出，不折算
    </template>
    <template #extra>
      <a-radio-group v-model="range" type="button" size="small" @change="load">
        <a-radio v-for="item in pnlRanges" :key="item.key" :value="item.key">{{ item.label }}</a-radio>
      </a-radio-group>
    </template>
    <a-alert v-if="error" type="warning">{{ error }}</a-alert>
    <a-spin v-else :loading="loading" class="pnl-spin">
      <template v-if="view">
        <div v-for="row in view.rows" :key="row.key" class="pnl-row">
          <div class="pnl-label">
            {{ row.label }}<span v-if="row.hint" class="tx-muted pnl-hint">{{ row.hint }}</span>
          </div>
          <div class="pnl-amounts">
            <div v-for="item in row.amounts" :key="item.asset">
              <span class="tx-num" :class="item.tone">{{ item.text }}</span>
              <span class="tx-muted"> {{ item.asset }}</span>
            </div>
            <span v-if="!row.amounts.length" class="tx-muted">{{ row.empty }}</span>
          </div>
        </div>
        <a-alert v-if="view.notice?.level === 'warn'" type="warning" class="pnl-notice">{{ view.notice.text }}</a-alert>
        <p v-else-if="view.notice" class="tx-muted pnl-note">{{ view.notice.text }}</p>
      </template>
      <p v-else-if="!loading" class="tx-muted pnl-note">暂无数据</p>
    </a-spin>
  </SectionBlock>
</template>

<script setup lang="ts">
import { inject, ref, watch } from "vue";
import { summarizeAccountPnl } from "@/api/trade";
import { createLatestRequestGuard } from "@/utils/latest-request";
import SectionBlock from "../shared/section-block.vue";
import { detailContextKey } from "./context";
import { buildPnlView, pnlRanges, pnlRequest, type PnlRange, type PnlView } from "./pnl-model";

const { logical, dataVersion } = inject(detailContextKey)!;

const range = ref<PnlRange>("7d");
const view = ref<PnlView | null>(null);
const loading = ref(false);
const error = ref("");
const requests = createLatestRequestGuard();

async function load() {
  const request = requests.begin();
  loading.value = true;
  error.value = "";
  try {
    const rsp = await summarizeAccountPnl(pnlRequest(logical.value.logical_account_id, range.value));
    if (!request.isLatest()) return;
    view.value = buildPnlView(rsp);
  } catch (cause) {
    if (!request.isLatest()) return;
    view.value = null;
    error.value = cause instanceof Error ? cause.message : "收益拆解读取失败";
  } finally {
    if (request.isLatest()) loading.value = false;
  }
}

watch(() => logical.value.logical_account_id, load, { immediate: true });
watch(dataVersion, () => void load());
</script>

<style src="../shared/accounts.css"></style>
<style scoped>
.pnl-spin {
  display: block;
}

.pnl-row {
  display: flex;
  justify-content: space-between;
  gap: var(--moox-space-3);
  padding: 8px 0;
  border-bottom: 1px solid var(--color-border-2);
  font-size: 13px;
}

.pnl-hint {
  margin-left: var(--moox-space-2);
  font-size: 12px;
}

.pnl-amounts {
  text-align: right;
}

.pnl-notice {
  margin-top: var(--moox-space-3);
}

.pnl-note {
  margin: var(--moox-space-2) 0 0;
  font-size: 12px;
}
</style>
