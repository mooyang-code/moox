<template>
  <div class="overview">
    <div class="column">
      <SectionBlock title="权益曲线">
        <template #sub>{{ logical.settlement_asset }}，每分钟一个点</template>
        <EquityCurve :logical-account-id="logical.logical_account_id" />
      </SectionBlock>
      <!-- 收益拆解由 T29 提供：按时间段汇总已实现盈亏、手续费、资金费 -->
      <SectionBlock title="收益拆解">
        <p class="placeholder">暂未提供。</p>
      </SectionBlock>
    </div>

    <div class="column">
      <SectionBlock title="策略目标">
        <template #extra>
          <a-link v-if="logical.control_mode === 1 && target" @click="goPositions">看未到位的标的</a-link>
        </template>
        <p v-if="logical.control_mode !== 1" class="placeholder">自主下单的组合没有策略目标，只按手工订单交易。</p>
        <p v-else-if="!target" class="placeholder">策略还没有给这个组合下过目标。</p>
        <a-descriptions v-else :column="1" size="small" :label-style="{ width: '96px' }">
          <a-descriptions-item label="状态">
            <StatusMark :tone="targetTone">{{ targetStateLabels[target.status.toUpperCase()] || "未知" }}</StatusMark>
          </a-descriptions-item>
          <a-descriptions-item label="K 线时间">{{ fmtClock(target.bar_end_time, true) }}</a-descriptions-item>
          <a-descriptions-item label="有效至">
            {{ fmtClock(target.valid_until, true) }}
            <span class="tx-muted">（{{ targetExpired ? "已过期" : "尚未过期" }}）</span>
          </a-descriptions-item>
          <a-descriptions-item label="目标">{{ targetCoins }} 个币</a-descriptions-item>
          <a-descriptions-item label="未到位">{{ offCount }} 个</a-descriptions-item>
          <a-descriptions-item label="被拦下">{{ (target.blocked_targets || []).length }} 个</a-descriptions-item>
          <a-descriptions-item label="最近错误">{{ target.last_error || "无" }}</a-descriptions-item>
        </a-descriptions>
      </SectionBlock>

      <SectionBlock title="执行账户" flush>
        <template #sub>{{ members.length }} 个</template>
        <template #extra><a-link @click="goSettings">管理</a-link></template>
        <div v-for="item in members" :key="item.account.trading_account_id" class="member">
          <div class="member-main">
            <div>
              <strong>{{ item.account.name }}</strong>
              <EnvMark :env="accountEnvironment(item.account)" class="member-env" />
            </div>
            <div class="tx-muted">
              {{ exchangeLabels[item.account.exchange] || "-" }} · {{ marketsText(item.account.markets) }} · 优先级
              {{ item.priority }}{{ item.enabled ? "" : " · 已停用" }}
            </div>
            <StatusMark :tone="item.account.ready ? 'ok' : 'bad'">
              {{ item.account.ready ? "就绪" : item.account.last_error || "未就绪" }}
            </StatusMark>
          </div>
          <div class="member-side">
            <strong class="tx-num">{{ fmtMoney(item.account.snapshot?.equity) }}</strong>
            <div>
              <a-button size="mini" type="text" @click="syncAccounts([item.account])">同步</a-button>
              <a-button
                size="mini"
                type="text"
                status="danger"
                :disabled="flattenRunning || positionCountOf(item.account.trading_account_id) === 0"
                @click="openFlatten(item.account)"
              >
                平仓
              </a-button>
            </div>
          </div>
        </div>
        <p v-if="!members.length" class="placeholder">组合里还没有执行账户。</p>
      </SectionBlock>

      <!-- 人工操作记录由 T29 提供：按组合分页列出暂停、下单、撤单、清仓及其结果 -->
      <SectionBlock title="人工操作记录">
        <p class="placeholder">暂未提供。</p>
      </SectionBlock>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, inject } from "vue";
import { useRoute, useRouter } from "vue-router";
import { exchangeLabels, targetStateLabels } from "@/api/trade";
import EnvMark from "../shared/env-mark.vue";
import EquityCurve from "../shared/equity-curve.vue";
import { accountEnvironment } from "../shared/environment";
import { fmtClock, fmtMoney } from "../shared/format";
import { marketsText } from "../shared/markets";
import SectionBlock from "../shared/section-block.vue";
import StatusMark from "../shared/status-mark.vue";
import { detailContextKey } from "./context";
import { targetCoinCount } from "./positions-model";

const context = inject(detailContextKey)!;
const { logical, target, targetExpired, snapshot, coinRows, accounts, flattenRunning, syncAccounts, openFlatten } = context;
const route = useRoute();
const router = useRouter();

const members = computed(() =>
  (logical.value.members || [])
    .map(member => ({ ...member, account: accounts.value.get(member.trading_account_id) }))
    .filter((item): item is typeof item & { account: NonNullable<typeof item.account> } => Boolean(item.account))
);
const targetCoins = computed(() => targetCoinCount(target.value?.targets || []));
const offCount = computed(() => coinRows.value.filter(row => row.state !== "ok").length);
const targetTone = computed(() => {
  switch (target.value?.status) {
    case "CONVERGED":
      return "ok";
    case "BLOCKED":
      return "bad";
    case "EXPIRED":
      return "warn";
    default:
      return "run";
  }
});

function positionCountOf(accountId: string): number {
  return (
    snapshot.value.swapPositions.filter(item => item.trading_account_id === accountId).length +
    snapshot.value.spotHoldings.filter(
      item => item.trading_account_id === accountId && item.instrument_id && Number(item.quantity) !== 0
    ).length
  );
}

function goTab(tab: string, extra: Record<string, string> = {}) {
  void router.replace({ query: { ...route.query, tab, ...extra } });
}
function goPositions() {
  goTab("positions", { filter: "off" });
}
function goSettings() {
  goTab("settings");
}
</script>

<style src="../shared/accounts.css"></style>
<style scoped>
.overview {
  display: grid;
  grid-template-columns: minmax(0, 1.7fr) minmax(280px, 1fr);
  gap: var(--moox-space-3);
  align-items: start;
}

.column {
  display: grid;
  gap: var(--moox-space-3);
}

.placeholder {
  margin: 0;
  color: var(--color-text-3);
  font-size: 13px;
}

.member {
  display: flex;
  justify-content: space-between;
  gap: var(--moox-space-3);
  padding: 12px 16px;
  border-bottom: 1px solid var(--color-border-2);
}

.member:last-child {
  border-bottom: 0;
}

.member-main {
  min-width: 0;
  font-size: 13px;
}

.member-env {
  margin-left: var(--moox-space-2);
  font-size: 12px;
}

.member-side {
  text-align: right;
}

@media (max-width: 900px) {
  .overview {
    grid-template-columns: minmax(0, 1fr);
  }
}
</style>
