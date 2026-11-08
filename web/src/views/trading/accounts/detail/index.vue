<template>
  <div class="moox-page tx-detail">
    <div class="moox-inner">
      <a-result v-if="loadError" status="error" :title="loadError">
        <template #extra>
          <a-space>
            <a-button v-if="!notFound" type="primary" @click="loadFirst">重新加载</a-button>
            <a-button @click="backToList">返回交易账户</a-button>
          </a-space>
        </template>
      </a-result>
      <template v-else-if="logical">
        <div class="detail-head">
          <div class="detail-title">
            <a-button type="text" class="back" @click="backToList">
              <template #icon><icon-left /></template>
              {{ environmentLabel(env) }}
            </a-button>
            <h2>{{ logical.name }}</h2>
            <div class="detail-meta">
              <EnvMark :env="env" />
              <span>{{ exchangesText }} · 结算 {{ logical.settlement_asset }}</span>
              <span v-if="strategy">
                关联策略
                <a-link @click="openStrategy">{{ strategy.name }}</a-link>
                <span class="tx-muted">（实例 {{ strategy.instanceId }}）</span>
              </span>
              <span v-else-if="logical.owner_instance_id">
                关联策略实例
                <a-link class="tx-mono" @click="openInstance(logical.owner_instance_id)">{{ logical.owner_instance_id }}</a-link>
              </span>
              <span v-else>{{
                logical.control_mode === 2 ? "自主下单，没有关联策略" : strategiesFailed ? "策略读取失败" : "尚未绑定策略"
              }}</span>
              <span class="tx-muted tx-mono">{{ logical.logical_account_id }}</span>
            </div>
          </div>
          <a-space wrap class="detail-actions">
            <template v-if="logical.control_mode === 1">
              <a-button
                v-if="logical.automation_state === 'ACTIVE'"
                status="warning"
                :disabled="flattenRunning"
                @click="pauseVisible = true"
              >
                暂停自动执行
              </a-button>
              <a-tooltip v-else :content="logical.ready ? '' : logical.readiness_reasons?.join('；')" :disabled="logical.ready">
                <a-button type="primary" :disabled="!logical.ready || flattenRunning" @click="resumeVisible = true">
                  恢复自动执行
                </a-button>
              </a-tooltip>
            </template>
            <a-button :loading="syncing" @click="syncAccounts(enabledAccounts)">同步</a-button>
            <a-button :disabled="flattenRunning" @click="openOrder()">下单</a-button>
            <a-tooltip content="没有持仓" :disabled="snapshotCount > 0">
              <a-button status="danger" :disabled="flattenRunning || snapshotCount === 0" @click="openFlatten()">清仓</a-button>
            </a-tooltip>
          </a-space>
        </div>

        <div class="strip">
          <div>
            <div class="k">权益</div>
            <div class="v tx-num">
              {{ fmtMoney(totals.equity) }}<small>{{ logical.settlement_asset }}</small>
            </div>
            <div class="d">同步于 <TimeText :value="latestSync" :zone="displayZone" precision="minute" /></div>
          </div>
          <div v-if="initialBalance">
            <div class="k">较初始资金</div>
            <!-- 还没读到权益（新建账户首次同步前）时整格显示“—”，不能按 0 算成 −100% -->
            <div class="v tx-num" :class="equityChange === null ? '' : pnlClass(equityChange)">
              {{ equityChange === null ? "—" : fmtSigned(equityChange) }}
            </div>
            <div class="d">
              初始 {{ fmtMoney(initialBalance, 0) }} ·
              {{ equityChange === null ? "—" : fmtPercent(equityChange / initialBalance) }}
            </div>
          </div>
          <div>
            <div class="k">可用资金</div>
            <div class="v tx-num">{{ fmtMoney(totals.available) }}</div>
            <div class="d">可用于新开仓</div>
          </div>
          <div v-if="logical.markets?.includes(2)">
            <div class="k">保证金占用</div>
            <div class="v tx-num">{{ fmtMoney(totals.margin) }}</div>
            <div class="d">占权益 {{ totals.equity ? fmtPercent(totals.margin / totals.equity, 1).replace("+", "") : "—" }}</div>
          </div>
          <div v-if="logical.markets?.includes(2)">
            <div class="k">未实现盈亏</div>
            <div class="v tx-num" :class="pnlClass(totals.unrealizedPnl)">{{ fmtSigned(totals.unrealizedPnl) }}</div>
            <div class="d">永续按标记价</div>
          </div>
          <div>
            <div class="k">{{ logical.control_mode === 1 ? "持仓 / 目标" : "持仓" }}</div>
            <div class="v tx-num">
              {{ snapshotCount }}<small v-if="logical.control_mode === 1">/ {{ targetCoins }}</small>
            </div>
            <div v-if="logical.control_mode === 1" class="d">{{ offCount }} 个未到位</div>
            <div v-else class="d">现货和永续合计</div>
          </div>
        </div>

        <div class="banners">
          <a-alert v-if="tracker.action.value" :type="actionAlertType" class="banner" :show-icon="false">
            <template #title>{{ actionTitle }}</template>
            <div>
              操作编号 <span class="tx-mono">{{ tracker.action.value.action_id }}</span>
              <template v-if="tracker.action.value.reason"> · 原因：{{ tracker.action.value.reason }}</template>
            </div>
            <div v-if="tracker.submittedOrder.value">
              订单 <span class="tx-mono">{{ tracker.submittedOrder.value.order_id }}</span
              >：{{ orderStateLabels[tracker.submittedOrder.value.state] || tracker.submittedOrder.value.state }}
            </div>
            <template v-if="flattenResult">
              <div v-for="item in flattenResult.accounts" :key="item.trading_account_id" class="banner-line">
                {{ accounts.get(item.trading_account_id)?.name || item.trading_account_id }}：{{
                  actionStateLabels[item.status] || item.status
                }}
                <template v-for="rest in item.remaining_positions || []" :key="(rest.instrument_id || '') + (rest.asset || '')">
                  ；剩余 {{ rest.instrument_id ? instrumentName(rest.instrument_id) : rest.asset }} {{ rest.quantity }}（{{
                    rest.reason
                  }}）
                </template>
                <template v-if="item.dust_positions?.length"
                  >；{{ item.dust_positions.length }} 个零头低于最小下单量，不算剩余</template
                >
                <template v-if="item.error">；{{ item.error }}</template>
              </div>
            </template>
            <div v-if="tracker.action.value.last_error" class="banner-line">{{ tracker.action.value.last_error }}</div>
            <div v-if="tracker.error.value" class="banner-line">{{ tracker.error.value }}</div>
            <template v-if="tracker.action.value.status !== 'RUNNING'" #action>
              <a-button size="mini" @click="tracker.reset()">知道了</a-button>
            </template>
          </a-alert>
          <a-alert
            v-if="logical.control_mode === 1 && logical.automation_state === 'PAUSED'"
            type="warning"
            class="banner"
            :show-icon="false"
          >
            <template #title>自动执行已暂停</template>
            原因：{{ logical.pause_reason || "未填写" }}。策略目标照常更新，但不会下单。
            <template v-if="target">{{
              targetExpiredNow ? "当前目标已过期，恢复后等策略下一期目标。" : "当前目标仍有效，恢复后继续调整持仓。"
            }}</template>
          </a-alert>
          <a-alert v-if="!logical.ready" type="error" class="banner" :show-icon="false">
            <template #title>未就绪</template>
            <div v-for="reason in logical.readiness_reasons" :key="reason">{{ reason }}</div>
            <template #action>
              <a-button size="mini" @click="setTab('settings')">去看执行账户</a-button>
            </template>
          </a-alert>
        </div>

        <a-tabs :active-key="tab" class="detail-tabs" @change="(key: string | number) => setTab(String(key))">
          <a-tab-pane v-for="item in detailTabs" :key="item.key" :title="item.label" />
        </a-tabs>

        <OverviewTab v-if="tab === 'overview'" />
        <PositionsTab v-else-if="tab === 'positions'" />
        <OrdersTab v-else-if="tab === 'orders'" />
        <FillsTab v-else-if="tab === 'fills'" />
        <LedgerTab v-else-if="tab === 'ledger'" />
        <SettingsTab v-else />

        <OrderDialog
          ref="orderDialog"
          :logical="logical"
          :accounts="accounts"
          :env="env"
          :tracker="tracker"
          @submitted="afterAction"
        />

        <ConfirmDialog
          v-model:visible="pauseVisible"
          title="暂停自动执行"
          :env="env"
          :combo-name="logical.name"
          :name-required="false"
          confirm-text="暂停"
          reason-placeholder="例如：手工检查仓位"
          :submit="submitPause"
        >
          <p class="dialog-text">
            暂停后不再按策略目标下单。已经提交的订单不会自动撤销，需要的话到「订单」里撤。策略照常给出目标，恢复后从当时最新的目标继续调整。
          </p>
        </ConfirmDialog>

        <ConfirmDialog
          v-model:visible="resumeVisible"
          title="恢复自动执行"
          :env="env"
          :combo-name="logical.name"
          :need-reason="false"
          confirm-text="恢复"
          :submit="submitResume"
        >
          <a-descriptions v-if="target" :column="1" size="small" class="dialog-block">
            <a-descriptions-item label="当前目标"
              >{{ formatInstant(target.bar_end_time, displayZone) }} 的 K 线，{{
                target.targets.length
              }}
              个标的</a-descriptions-item
            >
            <a-descriptions-item label="有效至">{{ formatInstant(target.valid_until, displayZone) }}</a-descriptions-item>
            <a-descriptions-item label="暂停原因">{{ logical.pause_reason || "未填写" }}</a-descriptions-item>
          </a-descriptions>
          <p class="dialog-text">
            {{
              target && !targetExpiredNow ? "恢复后马上按这个目标继续调整持仓。" : "目标已过期，恢复后等策略下一期目标再下单。"
            }}
          </p>
          <a-alert v-if="env !== 'paper'" type="warning" class="dialog-block">恢复后会向交易所发送真实订单。</a-alert>
        </ConfirmDialog>

        <ConfirmDialog
          v-model:visible="flattenVisible"
          :title="flattenTarget ? `平掉「${flattenTarget.name}」的全部持仓` : '清仓整个组合'"
          :env="env"
          :combo-name="logical.name"
          danger
          :confirm-text="flattenTarget ? '开始平仓' : '开始清仓'"
          :submit="submitFlatten"
        >
          <a-descriptions :column="1" size="small" class="dialog-block">
            <a-descriptions-item label="范围">
              {{ flattenTarget ? "只动这一个执行账户，组合里其他账户不动" : `组合里全部 ${enabledAccounts.length} 个执行账户` }}
            </a-descriptions-item>
            <a-descriptions-item label="撤销订单">{{ flattenOrdersText }}</a-descriptions-item>
            <a-descriptions-item label="平掉持仓">
              {{ flattenInfo.positions }} 个，按市价，价值约 {{ fmtMoney(flattenInfo.value) }} {{ logical.settlement_asset }}
            </a-descriptions-item>
            <a-descriptions-item v-if="logical.control_mode === 1" label="自动执行"
              >会暂停，避免策略马上把仓位建回来</a-descriptions-item
            >
          </a-descriptions>
          <p class="dialog-text">低于交易所最小下单量的零头仓位平不掉，会在结果里列出。同一个组合同时只能有一个清仓在跑。</p>
          <a-alert v-if="env === 'prod'" type="error" class="dialog-block">会用真实资金按市价成交，滑点可能较大。</a-alert>
        </ConfirmDialog>

        <ConfirmDialog
          v-model:visible="cancelVisible"
          title="撤单"
          :env="env"
          :combo-name="logical.name"
          danger
          confirm-text="撤单"
          :submit="submitCancel"
        >
          <a-descriptions v-if="cancelOrderTarget" :column="1" size="small" class="dialog-block">
            <a-descriptions-item label="订单">
              {{ instrumentName(cancelOrderTarget.instrument_id) }}
              {{ marketLabel(cancelOrderTarget.market_type) }}
              {{ cancelOrderTarget.side === 1 ? "买入" : "卖出" }} {{ fmtQty(cancelOrderTarget.quantity) }}，{{
                orderStateLabels[cancelOrderTarget.state] || cancelOrderTarget.state
              }}
            </a-descriptions-item>
            <a-descriptions-item label="来源">{{
              orderOwnerLabels[cancelOrderTarget.owner_type] || cancelOrderTarget.owner_type
            }}</a-descriptions-item>
          </a-descriptions>
          <a-alert v-if="logical.control_mode === 1" type="warning" class="dialog-block">
            撤单会暂停这个组合的自动执行，否则策略会马上重新下单。
          </a-alert>
        </ConfirmDialog>

        <ConfirmDialog
          v-model:visible="closePaperVisible"
          :title="`关闭模拟账户「${closePaperTarget?.name ?? ''}」`"
          :env="env"
          :combo-name="logical.name"
          :need-reason="false"
          danger
          confirm-text="关闭"
          :submit="submitClosePaper"
        >
          <a-alert type="error" class="dialog-block">
            撤掉这个账户的未完成订单，停用账户并移出组合，同时暂停整个组合账户的自动执行（组合里其他执行账户也不再按策略下单，需要时再手动恢复）。关闭后不能再下单，也不能重新启用；历史订单、成交保留。
          </a-alert>
          <a-alert v-if="(logical.members || []).length === 1" type="info" class="dialog-block">
            这是组合里唯一的执行账户，关闭后组合会变成未就绪。
          </a-alert>
        </ConfirmDialog>
      </template>
      <a-spin v-else class="loading" />
    </div>
  </div>
</template>

<script setup lang="ts">
import TimeText from "@/components/time-text/index.vue";
import { computed, onBeforeUnmount, provide, ref, watch } from "vue";
import { usePolling } from "@/hooks/usePolling";
import { useRoute, useRouter } from "vue-router";
import { Message } from "@arco-design/web-vue";
import { useSpaceStore } from "@/store/modules/space";
import { createClientId } from "@/utils/client-id";
import { createLatestRequestGuard } from "@/utils/latest-request";
import {
  actionStateLabels,
  cancelOrder,
  closePaperSimulation,
  exchangeLabels,
  flattenLogicalAccount,
  flattenTradingAccount,
  getLogicalAccount,
  isTradeNotFound,
  getLogicalAccountTarget,
  listOperatorActions,
  listOrders,
  listTradingAccounts,
  orderOwnerLabels,
  orderStateLabels,
  parseFlattenResult,
  pauseLogicalAccount,
  resumeLogicalAccount,
  syncTradingAccount
} from "@/api/trade";
import type { LogicalAccount, LogicalAccountTarget, Order, TradingAccount } from "@/api/trade";
import { accountMapOf } from "../account-list";
import ConfirmDialog from "../shared/confirm-dialog.vue";
import EnvMark from "../shared/env-mark.vue";
import { environmentFromQuery, environmentLabel, logicalEnvironment } from "../shared/environment";
import { fmtMoney, fmtPercent, fmtQty, fmtSigned, pnlClass } from "../shared/format";
import { instrumentName, marketLabel } from "../shared/markets";
import { loadPositionSnapshot, positionCount, type PositionSnapshot } from "../shared/position-snapshot";
import { loadStrategyLinks, type StrategyLink } from "../shared/strategy-link";
import { useActionTracker } from "./action-tracker";
import { detailContextKey, type OrderPrefill } from "./context";
import {
  actionTypeLabels,
  detailTabs,
  equityChangeOf,
  flattenPreview,
  isFlattenRunning,
  memberTotals,
  paperInitialBalance,
  runningAction,
  tabFromQuery,
  targetExpired
} from "./detail-model";
import FillsTab from "./fills-tab.vue";
import LedgerTab from "./ledger-tab.vue";
import OrderDialog from "./order-dialog.vue";
import OrdersTab from "./orders-tab.vue";
import OverviewTab from "./overview-tab.vue";
import PositionsTab from "./positions-tab.vue";
import { buildCoinRows, targetCoinCount } from "./positions-model";
import SettingsTab from "./settings-tab.vue";
import { formatInstant } from "@/utils/time-zone";
import { useDisplayZone } from "@/hooks/useDisplayZone";

// 交易页的时间按当前空间登记的时区显示
const displayZone = useDisplayZone();
defineOptions({ name: "trading-account-detail" });

const route = useRoute();
const router = useRouter();
const spaceStore = useSpaceStore();

const logicalId = computed(() => String(route.params.logical_account_id || ""));
const tab = computed(() => tabFromQuery(route.query.tab));
const logical = ref<LogicalAccount | null>(null);
const loadError = ref("");
const notFound = ref(false);
const allAccounts = ref<TradingAccount[]>([]);
const target = ref<LogicalAccountTarget | null>(null);
const snapshot = ref<PositionSnapshot>({ swapPositions: [], spotHoldings: [] });
const strategy = ref<StrategyLink | null>(null);
// 关联策略读取失败：页头既没有关联策略也没有实例编号时写“策略读取失败”
const strategiesFailed = ref(false);
const dataVersion = ref(0);
const syncing = ref(false);
const orderDialog = ref<InstanceType<typeof OrderDialog>>();

const accounts = computed(() => accountMapOf(allAccounts.value));
// 列表页进详情时带着当前页签（?env=）：没有可判断成员的实盘组合按它显示
const env = computed(() =>
  logical.value ? logicalEnvironment(logical.value, accounts.value, environmentFromQuery(route.query.env)) : "paper"
);
const enabledAccounts = computed(() =>
  (logical.value?.members || [])
    .filter(member => member.enabled)
    .map(member => accounts.value.get(member.trading_account_id))
    .filter((item): item is TradingAccount => Boolean(item))
);
const totals = computed(() => memberTotals(enabledAccounts.value));
const initialBalance = computed(() => paperInitialBalance(enabledAccounts.value));
const equityChange = computed(() => equityChangeOf(totals.value.equity, initialBalance.value));
const latestSync = computed(() => Math.max(0, ...enabledAccounts.value.map(item => Number(item.last_sync_at) || 0)));
const exchangesText = computed(() => {
  const names = [...new Set(enabledAccounts.value.map(item => exchangeLabels[item.exchange] || "-"))];
  return names.length ? names.join("、") : "-";
});
const targetExpiredNow = computed(() => targetExpired(target.value));
const coinRows = computed(() =>
  buildCoinRows({
    targets: target.value?.targets || [],
    blocked: target.value?.blocked_targets || [],
    swapPositions: snapshot.value.swapPositions,
    spotHoldings: snapshot.value.spotHoldings,
    converged: target.value?.status === "CONVERGED",
    hasTarget: Boolean(target.value)
  })
);
const snapshotCount = computed(() => positionCount(snapshot.value));
const targetCoins = computed(() => targetCoinCount(target.value?.targets || []));
const offCount = computed(() => coinRows.value.filter(row => row.state !== "ok").length);
const membersEditable = computed(() => logical.value?.automation_state === "PAUSED");

const tracker = useActionTracker({
  logicalAccountId: () => logical.value?.logical_account_id,
  // 动作状态变了（开始、完成）：订单、成交等页签按 dataVersion 重读，组合和持仓跟着刷新
  onChanged: () => afterAction()
});
const flattenRunning = computed(() => isFlattenRunning(tracker.action.value));
const flattenResult = computed(() => parseFlattenResult(tracker.action.value || undefined));
const actionTitle = computed(() => {
  const action = tracker.action.value;
  if (!action) return "";
  const name = actionTypeLabels[action.action_type] || "人工操作";
  if (action.status === "RUNNING") return `${name}进行中`;
  if (action.status === "COMPLETED")
    return ["SUBMIT_ORDER", "MANUAL_ORDER"].includes(action.action_type) ? `${name}：提交阶段完成` : `${name}完成`;
  return `${name}${actionStateLabels[action.status] || ""}`;
});
const actionAlertType = computed(() => {
  switch (tracker.action.value?.status) {
    case "COMPLETED":
      return "success";
    case "RUNNING":
      return "info";
    default:
      return "warning";
  }
});

// 同一时刻只认最新一次读取：空间切换、页签切换后迟到的响应不能覆盖当前数据。
const reloads = createLatestRequestGuard();
/** background：每 5 秒的后台刷新，不弹全局提示；失败保留上次的数据，下一轮再试。 */
async function reload(background = false) {
  const id = logicalId.value;
  if (!id) return;
  const request = reloads.begin();
  const options = { silent: background };
  const [logicalRsp, targetRsp, accountRsp] = await Promise.all([
    getLogicalAccount(id, options),
    getLogicalAccountTarget(id, options).catch(() => ({ target: undefined })),
    listTradingAccounts({ page: { page: 1, size: 200 } }, options)
  ]);
  if (!request.isLatest() || id !== logicalId.value) return;
  loadError.value = "";
  logical.value = logicalRsp.logical_account;
  target.value = targetRsp.target || null;
  allAccounts.value = accountRsp.accounts || [];
  const next = await loadPositionSnapshot(logicalRsp.logical_account, accounts.value, options).catch(() => null);
  if (next && request.isLatest() && id === logicalId.value) snapshot.value = next;
}

/** 刷新页面后接回进行中的操作（例如清仓）：页头继续显示进度，结束后订单、成交跟着刷新。 */
async function resumeRunningAction() {
  const id = logicalId.value;
  if (!id) return;
  try {
    const rsp = await listOperatorActions({ logical_account_id: id, page: { page: 1, size: 20 } }, { silent: true });
    if (id !== logicalId.value || tracker.action.value) return;
    const running = runningAction(rsp.actions);
    if (!running) return;
    tracker.action.value = running;
    tracker.start(running.action_id);
  } catch {
    // 只用于接回进度；读不到时不提示，操作记录页签里仍能看到
  }
}

async function loadFirst() {
  loadError.value = "";
  try {
    await reload();
    void resumeRunningAction();
    const links = await loadStrategyLinks(logical.value ? [logical.value] : []);
    // 读取失败时保留上一次读到的关联策略；没有的话页头仍能按 owner_instance_id 链到实例，
    // 两样都没有时写“策略读取失败”，不当成没有绑定
    strategiesFailed.value = links === null;
    if (links) strategy.value = links.get(logicalId.value) ?? null;
  } catch (cause) {
    if (logical.value) return;
    // 不存在和其他失败（网络、服务暂时不可用）分开说：后者可以重试，轮询读到后也会自动恢复
    loadError.value = isTradeNotFound(cause)
      ? "组合账户不存在或无权限"
      : `组合账户加载失败：${cause instanceof Error ? cause.message : "请稍后重试"}`;
    notFound.value = isTradeNotFound(cause);
  }
}

// 每 5 秒后台刷新（页面隐藏时暂停、不叠加）
usePolling(() => reload(true), 5000);

watch(
  logicalId,
  () => {
    reloads.invalidate();
    tracker.reset();
    logical.value = null;
    target.value = null;
    strategy.value = null;
    snapshot.value = { swapPositions: [], spotHoldings: [] };
    void loadFirst();
  },
  { immediate: true }
);
// 切换空间后这个组合编号在新空间里不存在，回到列表。
watch(
  () => spaceStore.selectedSpaceId,
  (next, previous) => {
    if (next && previous && next !== previous) backToList();
  }
);
onBeforeUnmount(() => {
  reloads.invalidate();
});

function setTab(key: string) {
  const next = tabFromQuery(key);
  if (next === tab.value) return;
  void router.replace({ query: { ...route.query, tab: next === "overview" ? undefined : next } });
}

function backToList() {
  void router.push({ path: "/trading/accounts", query: { env: env.value === "paper" ? undefined : env.value } });
}

function openInstance(instanceId: string) {
  void router.push({ name: "strategy-detail", params: { instanceId } });
}

function openStrategy() {
  if (strategy.value) void router.push({ name: "strategy-detail", params: { instanceId: strategy.value.instanceId } });
}

function afterAction() {
  dataVersion.value += 1;
  void reload().catch(() => undefined);
}

// ---- 对话框 ----
const pauseVisible = ref(false);
const resumeVisible = ref(false);
const flattenVisible = ref(false);
const flattenTarget = ref<TradingAccount | null>(null);
// 未完成订单数：null 表示读取中，undefined 表示读取失败
const flattenOrders = ref<number | null | undefined>(null);
const flattenActionId = ref("");
const cancelVisible = ref(false);
const cancelOrderTarget = ref<Order | null>(null);
const cancelActionId = ref("");
const closePaperVisible = ref(false);
const closePaperTarget = ref<TradingAccount | null>(null);
const flattenOrdersText = computed(() => {
  if (flattenOrders.value === null) return "读取中…";
  if (flattenOrders.value === undefined) return "未完成订单数读取失败，清仓时仍会全部撤销";
  return `${flattenOrders.value} 个未完成订单`;
});
const flattenInfo = computed(() => flattenPreview(snapshot.value, flattenTarget.value?.trading_account_id));

/** 提交期间组合账户被切走：请求已经发出，但不再改动当前页面。 */
function abandoned(id: string): boolean {
  if (logical.value?.logical_account_id === id) return false;
  Message.info("操作已提交到原来的组合账户，当前页面已切换；请回到原账户确认结果");
  return true;
}

async function submitPause(reason: string) {
  const id = logicalId.value;
  await pauseLogicalAccount(id, reason);
  if (!abandoned(id)) await reload();
}

async function submitResume() {
  const id = logicalId.value;
  const rsp = await resumeLogicalAccount(id);
  if (rsp.warning) Message.warning(rsp.warning);
  if (!abandoned(id)) await reload();
}

function openFlatten(account?: TradingAccount) {
  flattenTarget.value = account ?? null;
  flattenOrders.value = null;
  // 每次打开生成新的操作编号；同一次弹窗内重试复用它，保证幂等。
  flattenActionId.value = createClientId();
  flattenVisible.value = true;
  const id = logicalId.value;
  const scope = account ? { trading_account_id: account.trading_account_id } : { logical_account_id: id };
  listOrders({ ...scope, only_open: true, page: { page: 1, size: 1 } }, { silent: true })
    .then(rsp => {
      if (flattenVisible.value && logicalId.value === id) flattenOrders.value = rsp.page_result?.total ?? 0;
    })
    .catch(() => {
      if (flattenVisible.value && logicalId.value === id) flattenOrders.value = undefined;
    });
}

async function submitFlatten(reason: string) {
  const id = logicalId.value;
  const account = flattenTarget.value;
  const rsp = account
    ? await flattenTradingAccount(flattenActionId.value, account.trading_account_id, reason)
    : await flattenLogicalAccount(flattenActionId.value, id, reason);
  if (abandoned(id)) return true;
  if (rsp.action.action_id !== flattenActionId.value) Message.info("该组合账户已有正在执行的清仓，显示的是那次清仓的进度");
  tracker.action.value = rsp.action;
  tracker.submittedOrder.value = null;
  if (rsp.action.status === "RUNNING") tracker.start(rsp.action.action_id);
  afterAction();
  return true;
}

function openCancel(order: Order) {
  cancelOrderTarget.value = order;
  cancelActionId.value = createClientId();
  cancelVisible.value = true;
}

async function submitCancel(reason: string) {
  const order = cancelOrderTarget.value;
  if (!order) return false;
  const id = logicalId.value;
  const rsp = await cancelOrder(cancelActionId.value, order.order_id, reason);
  if (abandoned(id)) return true;
  tracker.action.value = rsp.action;
  tracker.submittedOrder.value = null;
  if (rsp.action.status === "RUNNING") tracker.start(rsp.action.action_id);
  afterAction();
  return true;
}

function openClosePaper(account: TradingAccount) {
  closePaperTarget.value = account;
  closePaperVisible.value = true;
}

async function submitClosePaper() {
  const account = closePaperTarget.value;
  if (!account) return false;
  const id = logicalId.value;
  await closePaperSimulation(account.trading_account_id);
  Message.success("模拟账户已关闭");
  if (!abandoned(id)) afterAction();
  return true;
}

async function syncAccounts(list: TradingAccount[]) {
  if (!list.length) return;
  syncing.value = true;
  try {
    const results = await Promise.allSettled(list.map(item => syncTradingAccount(item.trading_account_id)));
    const failed = results.filter(item => item.status === "rejected").length;
    if (failed) Message.warning(`${list.length} 个执行账户同步完成，${failed} 个失败`);
    else Message.success(`已同步 ${list.length} 个执行账户`);
    await reload();
  } finally {
    syncing.value = false;
  }
}

function openOrder(instrumentId?: string, accountId?: string, prefill?: OrderPrefill) {
  orderDialog.value?.open(instrumentId, accountId, prefill);
}

// 页签只在组合账户读到之后才渲染，这里可以放心当作非空。
const loadedLogical = computed(() => logical.value as LogicalAccount);

provide(detailContextKey, {
  logical: loadedLogical,
  allAccounts,
  accounts,
  env,
  target,
  targetExpired: targetExpiredNow,
  snapshot,
  coinRows,
  strategy,
  tracker,
  dataVersion,
  membersEditable,
  flattenRunning,
  reload,
  openOrder,
  openFlatten,
  openCancel,
  openClosePaper,
  syncAccounts
});
</script>

<style src="../shared/accounts.css"></style>
<style scoped>
.detail-head {
  display: flex;
  flex-wrap: wrap;
  align-items: flex-start;
  justify-content: space-between;
  gap: var(--moox-space-3);
}

.detail-title {
  min-width: 0;
}

.detail-title h2 {
  margin: 0;
  font-size: 20px;
  line-height: 28px;
}

.back {
  margin-left: -8px;
  color: var(--color-text-3);
}

.detail-meta {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 4px 16px;
  margin-top: 4px;
  color: var(--color-text-2);
  font-size: 13px;
}

.strip {
  display: flex;
  flex-wrap: wrap;
  margin: var(--moox-space-3) 0;
  border-top: 1px solid var(--color-border-2);
  border-bottom: 1px solid var(--color-border-2);
}

.strip > div {
  min-width: 140px;
  flex: 1 1 140px;
  padding: 12px 16px;
  border-right: 1px solid var(--color-border-2);
}

.strip > div:last-child {
  border-right: 0;
}

.strip .k {
  color: var(--color-text-3);
  font-size: 12px;
}

.strip .v {
  font-size: 18px;
  font-weight: 600;
  line-height: 26px;
  white-space: nowrap;
}

.strip .v small {
  margin-left: 4px;
  color: var(--color-text-3);
  font-size: 12px;
  font-weight: 400;
}

.strip .d {
  color: var(--color-text-3);
  font-size: 12px;
}

.banners {
  display: grid;
  gap: var(--moox-space-2);
}

.banners:empty {
  display: none;
}

.banner-line {
  margin-top: 4px;
}

.detail-tabs {
  margin-top: var(--moox-space-2);
}

.dialog-text {
  margin: 0 0 var(--moox-space-3);
  color: var(--color-text-2);
  font-size: 13px;
}

.dialog-block {
  margin-bottom: var(--moox-space-3);
}

.loading {
  display: block;
  margin: 80px auto;
}
</style>
