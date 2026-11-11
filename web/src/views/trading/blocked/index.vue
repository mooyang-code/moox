<template>
  <div class="moox-page tx-blocked">
    <div class="moox-inner">
      <h2 class="page-title">下架名单</h2>
      <div class="list-toolbar">
        <span class="tx-muted">全局生效，不分环境和空间：名单里的标的只减不增，不能交易时整项跳过。</span>
        <span class="spacer" />
        <a-button :loading="loading" aria-label="刷新下架名单" @click="load">
          <template #icon><icon-refresh /></template>
          刷新
        </a-button>
      </div>
      <a-alert v-if="loadError" type="error" class="alert">{{ loadError }}</a-alert>
      <BlockedTab :rows="rows" :loading="loading" @changed="load" />
    </div>
  </div>
</template>

<script setup lang="ts">
import { onBeforeUnmount, ref } from "vue";
import { listBlockedInstruments } from "@/api/trade";
import type { BlockedInstrument } from "@/api/trade";
import { createLatestRequestGuard } from "@/utils/latest-request";
import BlockedTab from "../accounts/blocked-tab.vue";

defineOptions({ name: "trading-blocked" });

const rows = ref<BlockedInstrument[]>([]);
const loading = ref(false);
const loadError = ref("");
const requests = createLatestRequestGuard();

async function load() {
  const request = requests.begin();
  loading.value = true;
  loadError.value = "";
  try {
    const rsp = await listBlockedInstruments({ silent: true });
    if (request.isLatest()) rows.value = rsp.instruments || [];
  } catch (error) {
    if (request.isLatest()) loadError.value = error instanceof Error ? error.message : "读取下架名单失败，请稍后重试";
  } finally {
    if (request.isLatest()) loading.value = false;
  }
}

onBeforeUnmount(() => requests.invalidate());

void load();
</script>

<style src="../accounts/shared/accounts.css"></style>
<style scoped>
.page-title {
  margin: 0;
  font-size: 20px;
  font-weight: 600;
  line-height: 32px;
}

.list-toolbar {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: var(--moox-space-3);
  margin: var(--moox-space-3) 0;
}

.spacer {
  flex: 1;
}

.alert {
  margin-bottom: var(--moox-space-3);
}
</style>
