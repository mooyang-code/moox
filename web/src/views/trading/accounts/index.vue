<template>
  <div class="moox-page tx-accounts">
    <div class="moox-inner">
      <PageTitleTabs :model-value="env" :items="environmentTabs" aria-label="账户环境" @change="changeEnv" />

      <div class="list-toolbar">
        <span class="tx-muted desc">{{ currentDesc }}策略按组合账户下目标，组合里的执行账户负责下单。</span>
        <span class="spacer" />
        <a-link v-if="unjoined.length" @click="unjoinedDialog?.open()">未加入组合的执行账户 {{ unjoined.length }} 个</a-link>
        <a-button :loading="loading" aria-label="刷新交易账户" @click="load">
          <template #icon><icon-refresh /></template>
          刷新
        </a-button>
        <a-button type="primary" status="success" @click="createDialog?.open(env)">
          <template #icon><icon-plus /></template>
          新建账户
        </a-button>
      </div>

      <a-alert v-if="loadError" type="error" class="alert">{{ loadError }}</a-alert>
      <a-table
        :bordered="{ cell: true }"
        size="small"
        row-key="key"
        :data="rows"
        :loading="loading"
        :pagination="false"
        :scroll="{ x: LIST_WIDTH }"
        :row-class="rowClass"
        v-model:expanded-keys="expandedKeys"
        @row-click="onRowClick"
      >
        <template #columns>
          <a-table-column title="组合账户" :width="230">
            <template #cell="{ record }">
              <template v-if="record.kind === 'combo'">
                <strong>{{ record.logical.name }}</strong>
                <div class="tx-muted tx-mono">{{ record.logical.logical_account_id }}</div>
              </template>
              <template v-else>
                {{ record.account.name }} <span class="tx-muted tx-mono">{{ record.account.trading_account_id }}</span>
              </template>
            </template>
          </a-table-column>
          <a-table-column title="交易所" :width="180">
            <template #cell="{ record }">
              <template v-if="record.kind === 'combo'">{{ record.exchangesText }}</template>
              <template v-else>
                {{ exchangeLabels[record.account.exchange] || "-" }} · {{ marketsText(record.account.markets) }}
              </template>
            </template>
          </a-table-column>
          <a-table-column title="关联策略" :width="200">
            <template #cell="{ record }">
              <template v-if="record.kind === 'combo'">
                <template v-if="strategyOf(record)">
                  <a-link @click.stop="openStrategy(strategyOf(record)!.instanceId)">{{ strategyOf(record)!.name }}</a-link>
                  <div class="tx-muted">
                    实例{{ strategyOf(record)!.enabled ? "已启用" : "已停用"
                    }}{{ strategyOf(record)!.health === "degraded" ? " · 降级" : "" }}
                  </div>
                </template>
                <template v-else>
                  <span class="tx-muted">无</span>
                  <div class="tx-muted">
                    {{ record.logical.control_mode === 2 ? "自主下单" : strategiesFailed ? "策略读取失败" : "尚未绑定策略" }}
                  </div>
                </template>
              </template>
              <span v-else class="tx-muted">优先级 {{ record.priority }} · {{ record.enabled ? "启用" : "停用" }}</span>
            </template>
          </a-table-column>
          <a-table-column title="自动执行" :width="130">
            <template #cell="{ record }">
              <template v-if="record.kind === 'combo'">
                <span v-if="record.logical.control_mode !== 1" class="tx-muted">不适用</span>
                <a-tooltip
                  v-else-if="record.logical.automation_state !== 'ACTIVE'"
                  :content="record.logical.pause_reason || '已暂停'"
                >
                  <StatusMark tone="warn">已暂停</StatusMark>
                </a-tooltip>
                <StatusMark v-else tone="run">运行中</StatusMark>
              </template>
              <span v-else class="tx-muted tx-nowrap"
                >同步于 <TimeText :value="record.account.last_sync_at" :zone="displayZone" precision="minute"
              /></span>
            </template>
          </a-table-column>
          <a-table-column title="权益" :width="170" align="right">
            <template #cell="{ record }">
              <span class="tx-num tx-nowrap">
                <strong v-if="record.kind === 'combo'">{{ fmtMoney(record.equity) }}</strong>
                <template v-else>{{ fmtMoney(record.account.snapshot?.equity) }}</template>
                <span v-if="record.kind === 'combo'" class="tx-muted tx-unit">{{ record.settlementAsset }}</span>
              </span>
            </template>
          </a-table-column>
          <a-table-column title="近 24 小时" :width="130" align="right">
            <template #cell="{ record }">
              <template v-if="record.kind === 'combo'">
                <span v-if="changes[record.key] === undefined" class="tx-muted">…</span>
                <span v-else class="tx-num tx-nowrap" :class="pnlClass(changes[record.key])">{{
                  fmtPercent(changes[record.key])
                }}</span>
              </template>
              <span v-else class="tx-muted tx-num tx-nowrap">可用 {{ fmtMoney(record.account.snapshot?.available_funds) }}</span>
            </template>
          </a-table-column>
          <a-table-column title="持仓" :width="70" align="right">
            <template #cell="{ record }">
              <template v-if="record.kind === 'combo'">
                <span v-if="counts[record.key] === undefined" class="tx-muted">…</span>
                <span v-else class="tx-num">{{ counts[record.key] ?? "—" }}</span>
              </template>
            </template>
          </a-table-column>
          <a-table-column title="就绪" :width="140">
            <template #cell="{ record }">
              <template v-if="record.kind === 'combo'">
                <StatusMark v-if="record.logical.ready" tone="ok">就绪</StatusMark>
                <a-tooltip v-else :content="(record.logical.readiness_reasons || []).join('；')">
                  <StatusMark tone="bad">未就绪 · {{ record.logical.readiness_reasons?.length || 0 }} 项</StatusMark>
                </a-tooltip>
              </template>
              <StatusMark v-else :tone="record.account.ready ? 'ok' : 'bad'">
                {{ record.account.ready ? "就绪" : record.account.last_error || "未就绪" }}
              </StatusMark>
            </template>
          </a-table-column>
        </template>
        <template #empty>
          {{ environmentLabel(env) }}还没有组合账户。<a-link @click="createDialog?.open(env)">新建一个</a-link>
        </template>
      </a-table>
      <p class="tx-muted note">
        点整行进入详情；点左边的三角展开执行账户，每个执行账户各自显示市场。权益来自账户快照，「近 24 小时」按权益曲线计算。
      </p>

      <CreateAccount ref="createDialog" :logicals="logicals" :accounts="accounts" @created="onCreated" />
      <UnjoinedDialog
        ref="unjoinedDialog"
        :env="env"
        :rows="unjoined"
        :logicals="logicals"
        :accounts="accounts"
        @changed="load"
      />
    </div>
  </div>
</template>

<script setup lang="ts">
import TimeText from "@/components/time-text/index.vue";
import { computed, onActivated, onBeforeUnmount, ref, watch } from "vue";
import { useRoute, useRouter } from "vue-router";
import PageTitleTabs from "@/components/page-title-tabs/index.vue";
import { useSpaceStore } from "@/store/modules/space";
import { exchangeLabels, listLogicalAccounts, listTradingAccounts, queryEquityCurve } from "@/api/trade";
import type { LogicalAccount, TradingAccount } from "@/api/trade";
import { createLatestRequestGuard } from "@/utils/latest-request";
import { accountMapOf, comboRows, equityChange, unjoinedAccounts, type ComboRow, type MemberRow } from "./account-list";
import CreateAccount from "./create/index.vue";
import { environmentFromQuery, environmentLabel, environmentTabs, type Environment } from "./shared/environment";
import { fmtMoney, fmtPercent, pnlClass } from "./shared/format";
import { marketsText } from "./shared/markets";
import { loadPositionSnapshot, positionCount } from "./shared/position-snapshot";
import StatusMark from "./shared/status-mark.vue";
import { loadStrategyLinks, type StrategyLink } from "./shared/strategy-link";
import UnjoinedDialog from "./unjoined-dialog.vue";
import { useDisplayZone } from "@/hooks/useDisplayZone";

// 交易页的时间按当前空间登记的时区显示
const displayZone = useDisplayZone();
defineOptions({ name: "trading-accounts" });

// 各列宽度之和：窄屏时表格横向滚动，每一列都保持自己的宽度，不会被挤成 0
const LIST_WIDTH = 230 + 180 + 200 + 130 + 170 + 130 + 70 + 140;

const route = useRoute();
const router = useRouter();
const spaceStore = useSpaceStore();

const env = computed<Environment>(() => environmentFromQuery(route.query.env));
const currentDesc = computed(() => environmentTabs.find(item => item.key === env.value)?.desc ?? "");
const logicals = ref<LogicalAccount[]>([]);
const accounts = ref<TradingAccount[]>([]);
const strategies = ref<Map<string, StrategyLink>>(new Map());
// 关联策略读取失败：列表里写“策略读取失败”，不当成没有绑定
const strategiesFailed = ref(false);
const loading = ref(false);
const loadError = ref("");
const expandedKeys = ref<string[]>([]);
const changes = ref<Record<string, number | null>>({});
const counts = ref<Record<string, number | null>>({});
const createDialog = ref<InstanceType<typeof CreateAccount>>();
const unjoinedDialog = ref<InstanceType<typeof UnjoinedDialog>>();

const accountMap = computed(() => accountMapOf(accounts.value));
const rows = computed(() => comboRows(logicals.value, accountMap.value, env.value));
const unjoined = computed(() => unjoinedAccounts(accounts.value, logicals.value, env.value));

// 空间切换后，旧空间迟到的响应不能覆盖新空间的列表：只有最新一次请求可以更新页面。
const listRequests = createLatestRequestGuard();
const extraRequests = createLatestRequestGuard();

async function load() {
  const request = listRequests.begin();
  loading.value = true;
  loadError.value = "";
  try {
    // 标题页签、未加入组合都要看全量，这里一次取完，表格不分页。
    const [logicalRsp, accountRsp] = await Promise.all([
      listLogicalAccounts({ page: 1, size: 500 }),
      listTradingAccounts({ page: { page: 1, size: 200 } })
    ]);
    if (!request.isLatest()) return;
    logicals.value = logicalRsp.logical_accounts || [];
    accounts.value = accountRsp.accounts || [];
    // 重新读列表后权益变化和持仓数也要重读
    changes.value = {};
    counts.value = {};
    void loadExtras(rows.value);
    void loadStrategies(request);
  } catch (error) {
    if (request.isLatest()) loadError.value = error instanceof Error ? error.message : "读取交易账户失败，请稍后重试";
  } finally {
    if (request.isLatest()) loading.value = false;
  }
}

async function loadStrategies(request: { isLatest(): boolean }) {
  const links = await loadStrategyLinks(logicals.value);
  if (!request.isLatest()) return;
  strategiesFailed.value = links === null;
  strategies.value = links ?? new Map();
}

const strategyOf = (row: ComboRow) => strategies.value.get(row.key);

/** 权益变化和持仓数逐个组合补：列表先渲染出来，数字陆续出现，同时最多三个请求。 */
async function loadExtras(list: ComboRow[]) {
  const request = extraRequests.begin();
  const pending = list.filter(row => changes.value[row.key] === undefined || counts.value[row.key] === undefined);
  const now = Date.now();
  let cursor = 0;
  async function worker() {
    while (cursor < pending.length && request.isLatest()) {
      const row = pending[cursor++];
      const [change, count] = await Promise.all([
        queryEquityCurve({ logical_account_id: row.key, start_time: String(now - 86_400_000), end_time: String(now) })
          .then(rsp => equityChange(rsp.points || []))
          .catch(() => null),
        loadPositionSnapshot(row.logical, accountMap.value)
          .then(positionCount)
          .catch(() => null)
      ]);
      if (!request.isLatest()) return;
      changes.value = { ...changes.value, [row.key]: change };
      counts.value = { ...counts.value, [row.key]: count };
    }
  }
  await Promise.all([worker(), worker(), worker()]);
}

// 切换环境后，新环境里还没读过的组合补读权益变化和持仓数
watch(env, () => void loadExtras(rows.value));

function changeEnv(key: string) {
  const next = environmentFromQuery(key);
  void router.replace({ query: { ...route.query, env: next === "paper" ? undefined : next } });
}

function rowClass(record: ComboRow | MemberRow) {
  return record.kind === "combo" ? "combo-row" : "member-row";
}

function onRowClick(record: ComboRow | MemberRow, event: Event) {
  if (record.kind !== "combo") return;
  // 点三角只展开，不进详情
  if ((event.target as HTMLElement).closest(".arco-table-expand-btn, .arco-link, a, button")) return;
  // 带上当前页签：没有成员的实盘组合在测试网、生产环境两个页签都列出，详情按进来的页签显示环境
  void router.push({ path: `/trading/accounts/${record.key}`, query: { env: env.value === "paper" ? undefined : env.value } });
}

function openStrategy(instanceId: string) {
  void router.push({ name: "strategy-detail", params: { instanceId } });
}

function onCreated(kind: Environment) {
  if (kind !== env.value) changeEnv(kind);
  void load();
}

watch(
  () => spaceStore.selectedSpaceId,
  (next, previous) => {
    if (!next || next === previous) return;
    listRequests.invalidate();
    extraRequests.invalidate();
    logicals.value = [];
    accounts.value = [];
    changes.value = {};
    counts.value = {};
    void load();
  }
);
onBeforeUnmount(() => {
  listRequests.invalidate();
  extraRequests.invalidate();
});

// 列表页被页面缓存：从详情页回来时（关闭模拟账户、移出成员、改名之后）重新读，不显示旧数据。
let activatedOnce = false;
onActivated(() => {
  if (activatedOnce) void load();
  activatedOnce = true;
});

void load();
</script>

<style src="./shared/accounts.css"></style>
<style scoped>
.list-toolbar {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: var(--moox-space-3);
  margin: var(--moox-space-3) 0;
}

.desc {
  font-size: 13px;
}

.spacer {
  flex: 1;
}

.alert {
  margin-bottom: var(--moox-space-3);
}

.note {
  margin: var(--moox-space-3) 0 0;
  font-size: 12px;
}

:deep(.combo-row) {
  cursor: pointer;
}

:deep(.member-row td) {
  background: var(--color-fill-1);
  font-size: 12.5px;
}
</style>
