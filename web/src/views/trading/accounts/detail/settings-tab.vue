<template>
  <div class="settings">
    <SectionBlock title="执行账户" flush>
      <template #sub>
        点左边的三角展开单个账户的设置<template v-if="logical.control_mode === 1 && !membersEditable"
          >；增删执行账户、改优先级和启用状态前，要先暂停自动执行</template
        >
      </template>
      <template #extra>
        <a-tooltip content="先暂停自动执行" :disabled="membersEditable">
          <a-button type="primary" status="success" size="small" :disabled="!membersEditable" @click="memberDialog?.openAdd()"
            >添加执行账户</a-button
          >
        </a-tooltip>
      </template>
      <a-table
        :bordered="{ cell: true }"
        row-key="trading_account_id"
        size="small"
        :data="rows"
        :pagination="false"
        :scroll="{ x: MEMBERS_WIDTH }"
        :expandable="{ width: 40 }"
        v-model:expanded-keys="expandedKeys"
      >
        <template #columns>
          <a-table-column title="执行账户" :width="190">
            <template #cell="{ record }">
              <strong>{{ record.account.name }}</strong>
              <div class="tx-muted tx-mono">{{ record.account.trading_account_id }}</div>
            </template>
          </a-table-column>
          <a-table-column title="交易所 · 市场" :width="160">
            <template #cell="{ record }"
              >{{ exchangeLabels[record.account.exchange] || "-" }} · {{ marketsText(record.account.markets) }}</template
            >
          </a-table-column>
          <a-table-column title="优先级" :width="80" align="right">
            <template #cell="{ record }">{{ record.priority }}</template>
          </a-table-column>
          <a-table-column title="启用" :width="70">
            <template #cell="{ record }">{{ record.enabled ? "是" : "否" }}</template>
          </a-table-column>
          <a-table-column title="就绪" :width="140">
            <template #cell="{ record }">
              <StatusMark :tone="record.account.ready ? 'ok' : 'bad'">
                {{ record.account.ready ? "就绪" : record.account.last_error || "未就绪" }}
              </StatusMark>
            </template>
          </a-table-column>
          <a-table-column title="权益" :width="120" align="right">
            <template #cell="{ record }"
              ><span class="tx-num tx-nowrap">{{ fmtMoney(record.account.snapshot?.equity) }}</span></template
            >
          </a-table-column>
          <a-table-column title="可用资金" :width="120" align="right">
            <template #cell="{ record }"
              ><span class="tx-num tx-nowrap">{{ fmtMoney(record.account.snapshot?.available_funds) }}</span></template
            >
          </a-table-column>
          <a-table-column title="最近同步" :width="120">
            <template #cell="{ record }"
              ><span class="tx-muted">{{ fmtClock(record.account.last_sync_at) }}</span></template
            >
          </a-table-column>
          <a-table-column title="操作" :width="190" fixed="right">
            <template #cell="{ record }">
              <a-space :size="0">
                <a-button size="mini" type="text" @click="syncAccounts([record.account])">同步</a-button>
                <a-tooltip content="先暂停自动执行" :disabled="membersEditable">
                  <a-button
                    size="mini"
                    type="text"
                    :disabled="!membersEditable"
                    @click="memberDialog?.openEdit(record, record.account.name)"
                    >编辑</a-button
                  >
                </a-tooltip>
                <a-tooltip content="先暂停自动执行" :disabled="membersEditable">
                  <a-button
                    size="mini"
                    type="text"
                    status="danger"
                    :disabled="!membersEditable"
                    @click="openRemove(record.account)"
                    >移出</a-button
                  >
                </a-tooltip>
              </a-space>
            </template>
          </a-table-column>
        </template>
        <template #expand-row="{ record }">
          <MemberSettings :account="record.account" @changed="reload" />
        </template>
        <template #empty>组合里还没有执行账户</template>
      </a-table>
    </SectionBlock>

    <!-- 挂载点：下单设置（T14）。接入时用对应的表单组件替换下面这一行。 -->
    <SectionBlock title="下单设置">
      <p class="placeholder">暂未提供。</p>
    </SectionBlock>

    <!-- 挂载点：资金设置（T22）。接入时用对应的表单组件替换下面这一行。 -->
    <SectionBlock title="资金设置">
      <p class="placeholder">暂未提供。</p>
    </SectionBlock>

    <SectionBlock title="组合设置">
      <a-form :model="{}" layout="vertical" class="combo-form">
        <a-form-item label="名称"><a-input v-model="comboName" :max-length="64" /></a-form-item>
        <a-form-item label="运行方式" extra="创建后不能改">
          <a-input :model-value="logical.control_mode === 1 ? '策略驱动' : '自主下单'" disabled />
        </a-form-item>
        <a-form-item label="关联策略" extra="在策略管理里绑定或换绑">
          <a-input :model-value="strategy ? `${strategy.name}（实例 ${strategy.instanceId}）` : '无'" disabled />
        </a-form-item>
        <a-form-item label="结算资产"><a-input :model-value="logical.settlement_asset" disabled /></a-form-item>
      </a-form>
      <a-button type="primary" size="small" :loading="renaming" :disabled="!nameDirty" @click="renameCombo">保存</a-button>
    </SectionBlock>

    <section class="danger-zone">
      <h3>危险操作</h3>
      <div class="danger-row">
        <div class="danger-text">
          <strong>清仓整个组合</strong>
          <div class="tx-muted">
            撤掉所有执行账户的未完成订单，按市价平掉全部持仓{{ logical.control_mode === 1 ? "，并暂停自动执行" : "" }}。
          </div>
        </div>
        <a-button status="danger" :disabled="flattenRunning || !hasPositions" @click="openFlatten()">清仓</a-button>
      </div>
      <div v-for="item in rows" :key="`dz-${item.account.trading_account_id}`" class="danger-row">
        <template v-if="item.account.execution_mode === 1">
          <div class="danger-text">
            <strong>关闭模拟账户「{{ item.account.name }}」</strong>
            <div class="tx-muted">撤掉它的未完成订单，停用账户并移出组合。之后不能再下单，历史订单和成交保留。</div>
          </div>
          <a-button status="danger" @click="openClosePaper(item.account)">关闭模拟账户</a-button>
        </template>
        <template v-else>
          <div class="danger-text">
            <strong>停用执行账户「{{ item.account.name }}」</strong>
            <div class="tx-muted">还在组合里的执行账户不能停用，先暂停自动执行并把它移出组合。</div>
          </div>
          <a-button status="danger" disabled>停用</a-button>
        </template>
      </div>
    </section>

    <MemberDialog ref="memberDialog" :logical="logical" :candidates="candidates" @saved="reload" />

    <ConfirmDialog
      v-model:visible="removeVisible"
      :title="`把「${removeTarget?.name ?? ''}」移出组合`"
      :env="env"
      :combo-name="logical.name"
      :need-reason="false"
      danger
      confirm-text="移出"
      :submit="submitRemove"
    >
      <p class="dialog-text">移出后它的持仓留在交易所账户里不动，也不再算进这个组合。需要的话先平仓再移出。</p>
      <a-descriptions :column="1" size="small">
        <a-descriptions-item label="当前持仓"
          >{{ removeTarget ? positionCountOf(removeTarget.trading_account_id) : 0 }} 个</a-descriptions-item
        >
      </a-descriptions>
    </ConfirmDialog>
  </div>
</template>

<script setup lang="ts">
import { computed, inject, ref, watch } from "vue";
import { Message } from "@arco-design/web-vue";
import { exchangeLabels, listLogicalAccounts, removeLogicalAccountMember, updateLogicalAccount } from "@/api/trade";
import type { LogicalAccount, TradingAccount } from "@/api/trade";
import ConfirmDialog from "../shared/confirm-dialog.vue";
import { fmtClock, fmtMoney } from "../shared/format";
import { marketsText } from "../shared/markets";
import MemberSettings from "../shared/member-settings.vue";
import { eligibleAccounts } from "../shared/membership";
import SectionBlock from "../shared/section-block.vue";
import StatusMark from "../shared/status-mark.vue";
import { detailContextKey } from "./context";
import MemberDialog from "./member-dialog.vue";

const {
  logical,
  accounts,
  allAccounts,
  env,
  strategy,
  snapshot,
  membersEditable,
  flattenRunning,
  reload,
  syncAccounts,
  openFlatten,
  openClosePaper
} = inject(detailContextKey)!;

// 各列宽度之和加展开箭头列：窄屏时横向滚动，数字不折行
const MEMBERS_WIDTH = 40 + 190 + 160 + 80 + 70 + 140 + 120 + 120 + 120 + 190;
const memberDialog = ref<InstanceType<typeof MemberDialog>>();
const expandedKeys = ref<string[]>([]);
const allLogicals = ref<LogicalAccount[]>([]);
const comboName = ref(logical.value.name);
const renaming = ref(false);
const removeVisible = ref(false);
const removeTarget = ref<TradingAccount | null>(null);

const rows = computed(() =>
  (logical.value.members || [])
    .map(member => ({ ...member, account: accounts.value.get(member.trading_account_id) }))
    .filter((item): item is typeof item & { account: TradingAccount } => Boolean(item.account))
);
const candidates = computed(() => eligibleAccounts(allAccounts.value, allLogicals.value, logical.value, env.value));
const hasPositions = computed(
  () => snapshot.value.swapPositions.length > 0 || snapshot.value.spotHoldings.some(item => item.instrument_id)
);
const nameDirty = computed(() => comboName.value.trim() !== "" && comboName.value.trim() !== logical.value.name);

// 候选账户要排除所有组合里的成员，所以读全部组合；成员变化后重读。
async function loadLogicals() {
  try {
    allLogicals.value = (await listLogicalAccounts({ page: 1, size: 500 })).logical_accounts || [];
  } catch {
    allLogicals.value = [];
  }
}
watch(() => (logical.value.members || []).map(item => item.trading_account_id).join(","), loadLogicals, { immediate: true });
watch(
  () => logical.value.name,
  name => {
    comboName.value = name;
  }
);

function positionCountOf(accountId: string): number {
  return (
    snapshot.value.swapPositions.filter(item => item.trading_account_id === accountId).length +
    snapshot.value.spotHoldings.filter(item => item.trading_account_id === accountId && item.instrument_id).length
  );
}

function openRemove(account: TradingAccount) {
  removeTarget.value = account;
  removeVisible.value = true;
}

async function submitRemove() {
  const account = removeTarget.value;
  if (!account) return false;
  await removeLogicalAccountMember(logical.value.logical_account_id, account.trading_account_id);
  Message.success("已移出组合");
  await reload();
  return true;
}

async function renameCombo() {
  renaming.value = true;
  try {
    await updateLogicalAccount(logical.value.logical_account_id, comboName.value.trim());
    Message.success("组合名称已保存");
    await reload();
  } catch (error) {
    Message.error(error instanceof Error ? error.message : "保存失败");
  } finally {
    renaming.value = false;
  }
}
</script>

<style src="../shared/accounts.css"></style>
<style scoped>
.settings {
  display: grid;
  grid-template-columns: minmax(0, 1fr);
  gap: var(--moox-space-3);
}

.placeholder {
  margin: 0;
  color: var(--color-text-3);
  font-size: 13px;
}

.combo-form {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(260px, 1fr));
  column-gap: var(--moox-space-6);
}

.danger-zone {
  border: 1px solid rgb(var(--red-6));
  border-radius: var(--border-radius-small);
}

.danger-zone h3 {
  margin: 0;
  padding: 10px 16px;
  border-bottom: 1px solid var(--color-border-2);
  color: rgb(var(--red-6));
  font-size: 15px;
}

.danger-row {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: var(--moox-space-3);
  padding: 12px 16px;
  border-bottom: 1px solid var(--color-border-2);
}

.danger-row:last-child {
  border-bottom: 0;
}

.danger-text {
  min-width: 220px;
  flex: 1;
  font-size: 13px;
}

.dialog-text {
  margin: 0 0 var(--moox-space-3);
  color: var(--color-text-2);
  font-size: 13px;
}
</style>
