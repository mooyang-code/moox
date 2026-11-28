<template>
  <div class="moox-page tx-blocked">
    <div class="moox-inner">
      <h2 class="page-title">下架名单</h2>
      <div class="list-toolbar">
        <span class="tx-muted">全局生效，不分环境和空间：名单里的标的只减不增，不能交易时整项跳过；时间按 UTC 显示。</span>
        <span class="spacer" />
        <a-button aria-label="手工登记下架" @click="dialogVisible = true">手工登记下架</a-button>
        <a-button :loading="loading" aria-label="刷新下架名单" @click="load">
          <template #icon><icon-refresh /></template>
          刷新
        </a-button>
      </div>
      <a-alert v-if="loadError" type="error" class="alert">{{ loadError }}</a-alert>

      <h3 class="section-title">下架处理</h3>
      <p class="tx-muted section-note">
        Trade 收到下架后立即禁止买入，下架前 24 小时、2 小时各规划一轮清仓（只平策略持有的部分），下架后 10
        分钟核对。进行中的记录每 30 秒刷新。
      </p>
      <a-table
        :bordered="{ cell: true }"
        size="small"
        row-key="delist_id"
        :data="delists"
        :loading="loading"
        :pagination="false"
        :scroll="{ x: DELIST_WIDTH }"
        :expandable="{ width: 40 }"
        :row-class="(record: DelistRecord) => (record.delist_id === highlighted ? 'is-highlighted' : '')"
      >
        <template #columns>
          <a-table-column title="标的" :width="200">
            <template #cell="{ record }">
              <strong>{{ record.exchange_symbol }}</strong>
              <span class="tx-muted"> {{ exchangeLabel(record.exchange) }} {{ marketLabel(record.market_type) }}</span>
            </template>
          </a-table-column>
          <a-table-column title="范围" :width="90">
            <template #cell="{ record }">{{ scopeLabel(record.scope) }}</template>
          </a-table-column>
          <a-table-column title="下架时间" :width="170">
            <template #cell="{ record }">
              <span class="tx-nowrap"><TimeText :value="record.delist_at" zone="UTC" /></span>
            </template>
          </a-table-column>
          <a-table-column title="状态" :width="200">
            <template #cell="{ record }">
              <a-tag size="small" :color="delistStatusColor(record.status)">{{ delistStatusText(record.status) }}</a-tag>
            </template>
          </a-table-column>
          <a-table-column title="已规划轮次" :width="170">
            <template #cell="{ record }">{{ plannedRoundText(record.planned_round) }}</template>
          </a-table-column>
          <a-table-column title="说明" :width="320" ellipsis tooltip>
            <template #cell="{ record }">{{ record.result || record.note || "—" }}</template>
          </a-table-column>
        </template>
        <template #expand-row="{ record }">
          <div class="delist-detail">
            <p v-if="record.note" class="tx-muted">最近一次规划：{{ record.note }}</p>
            <p v-if="record.result" class="tx-muted">结论：{{ record.result }}</p>
            <p v-if="!record.flattens.length" class="tx-muted">还没有清仓：没到下架前 24 小时，或查到没有组合持有。</p>
            <ul v-else class="flatten-list">
              <li v-for="flatten in record.flattens" :key="`${flatten.round}/${flatten.space_id}/${flatten.logical_account_id}`">
                第 {{ flatten.round }} 轮
                <a-link @click="openAccount(flatten)">{{ flatten.space_id }} / {{ flatten.logical_account_id }}</a-link>
                ：{{ actionStatusText(flatten.action_status) }}
                <span class="tx-muted tx-mono">（动作号 {{ flatten.action_id }}）</span>
                <span v-if="flatten.last_error" class="tx-muted">{{ flatten.last_error }}</span>
              </li>
            </ul>
          </div>
        </template>
        <template #empty>没有下架处理记录。</template>
      </a-table>

      <h3 class="section-title">名单</h3>
      <BlockedTab :rows="rows" :loading="loading" @changed="load" @locate="locate" />
    </div>
    <DelistDialog v-model:visible="dialogVisible" @registered="load" />
  </div>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, ref } from "vue";
import { useRouter } from "vue-router";
import TimeText from "@/components/time-text/index.vue";
import { exchangeLabel, listBlockedInstruments } from "@/api/trade";
import type { BlockedInstrument } from "@/api/trade";
import { listDelists, type DelistFlatten, type DelistRecord } from "@/api/trade/delist";
import { usePolling } from "@/hooks/usePolling";
import { useSpaceStore } from "@/store/modules/space";
import { createLatestRequestGuard } from "@/utils/latest-request";
import BlockedTab from "./blocked-tab.vue";
import { marketLabel } from "../accounts/shared/markets";
import DelistDialog from "./delist-dialog.vue";
import { actionStatusText, delistStatusColor, delistStatusText, plannedRoundText, scopeLabel, sortDelists } from "./delist-utils";

defineOptions({ name: "trading-blocked" });

// 进行中的全部列出，另带最近结束的若干条
const RECENT_LIMIT = 50;
const DELIST_WIDTH = 40 + 200 + 90 + 170 + 200 + 170 + 320;

const router = useRouter();
const spaceStore = useSpaceStore();
const rows = ref<BlockedInstrument[]>([]);
const delists = ref<DelistRecord[]>([]);
const loading = ref(false);
const loadError = ref("");
const dialogVisible = ref(false);
const highlighted = ref("");
const requests = createLatestRequestGuard();

const hasActive = computed(() => delists.value.some(item => item.status === "ACTIVE"));

async function load() {
  const request = requests.begin();
  loading.value = true;
  loadError.value = "";
  try {
    const [blocked, records] = await Promise.all([listBlockedInstruments(), listDelists(false, RECENT_LIMIT)]);
    if (!request.isLatest()) return;
    rows.value = blocked.instruments || [];
    delists.value = sortDelists(records);
  } catch (error) {
    if (request.isLatest()) loadError.value = error instanceof Error ? error.message : "读取下架名单失败，请稍后重试";
  } finally {
    if (request.isLatest()) loading.value = false;
  }
}

/** 名单里的“下架记录”点过来：高亮上段对应的记录。 */
function locate(delistId: string) {
  highlighted.value = delistId;
}

/** 组合账户属于某个空间：先切到那个空间再打开账户详情。 */
function openAccount(flatten: DelistFlatten) {
  spaceStore.setSelectedSpace(flatten.space_id);
  void router.push(`/trading/accounts/${encodeURIComponent(flatten.logical_account_id)}`);
}

onBeforeUnmount(() => requests.invalidate());

void load();
// 进行中的记录每 30 秒刷新
usePolling(load, 30_000, hasActive);
</script>

<style src="../accounts/shared/accounts.css"></style>
<style scoped>
.page-title {
  margin: 0;
  font-size: 20px;
  font-weight: 600;
  line-height: 32px;
}

.section-title {
  margin: var(--moox-space-4) 0 var(--moox-space-2);
  font-size: 16px;
  font-weight: 600;
}

.section-note {
  margin: 0 0 var(--moox-space-2);
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

.delist-detail p {
  margin: 0 0 var(--moox-space-2);
}

.flatten-list {
  margin: 0;
  padding-left: 18px;
  line-height: 1.8;
}

:deep(.is-highlighted td) {
  background: var(--color-primary-light-1);
}
</style>
