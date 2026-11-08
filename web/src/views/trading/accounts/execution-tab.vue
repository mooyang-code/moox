<template>
  <a-table
    :bordered="{ cell: true }"
    size="small"
    row-key="key"
    :data="rows"
    :loading="loading"
    :pagination="false"
    :scroll="{ x: LIST_WIDTH }"
    :expandable="{ width: 40 }"
    v-model:expanded-keys="expandedKeys"
  >
    <template #columns>
      <a-table-column title="执行账户" :width="180">
        <template #cell="{ record }">
          <strong>{{ record.account.name }}</strong>
          <div class="tx-muted tx-mono">{{ record.account.trading_account_id }}</div>
        </template>
      </a-table-column>
      <a-table-column title="交易所 · 市场" :width="150">
        <template #cell="{ record }">
          <span class="tx-nowrap"
            >{{ exchangeLabels[record.account.exchange] || "-" }} · {{ marketsText(record.account.markets) }}</span
          >
        </template>
      </a-table-column>
      <a-table-column title="所属组合" :width="170">
        <template #cell="{ record }">
          <template v-if="record.logical">
            <a-link @click="openCombo(record.logical.logical_account_id)">{{ record.logical.name }}</a-link>
            <div class="tx-muted">优先级 {{ record.priority }}{{ record.enabled ? "" : " · 成员已停用" }}</div>
          </template>
          <span v-else class="tx-muted">未加入</span>
        </template>
      </a-table-column>
      <a-table-column title="状态" :width="130">
        <template #cell="{ record }">
          <StatusMark v-if="record.account.status !== 'ENABLED'">已停用</StatusMark>
          <StatusMark v-else :tone="record.account.ready ? 'ok' : 'bad'">
            {{ record.account.ready ? "就绪" : record.account.last_error || "未就绪" }}
          </StatusMark>
        </template>
      </a-table-column>
      <a-table-column title="权益" :width="120" align="right">
        <template #cell="{ record }">
          <span class="tx-num tx-nowrap">{{ fmtMoney(record.account.snapshot?.equity) }}</span>
        </template>
      </a-table-column>
      <a-table-column title="可用资金" :width="120" align="right">
        <template #cell="{ record }">
          <span class="tx-num tx-nowrap">{{ fmtMoney(record.account.snapshot?.available_funds) }}</span>
        </template>
      </a-table-column>
      <a-table-column title="最近同步" :width="150">
        <template #cell="{ record }">
          <span class="tx-muted tx-nowrap"
            ><TimeText :value="record.account.last_sync_at" :zone="displayZone" precision="minute"
          /></span>
        </template>
      </a-table-column>
      <a-table-column title="操作" :width="90" :fixed="isNarrow ? undefined : 'right'">
        <template #cell="{ record }">
          <a-tooltip v-if="!record.logical" content="先展开这一行，在设置里启用" :disabled="record.account.status === 'ENABLED'">
            <a-button
              size="mini"
              type="text"
              :disabled="record.account.status !== 'ENABLED'"
              @click="joinDialog?.open(record.account)"
              >加入组合</a-button
            >
          </a-tooltip>
        </template>
      </a-table-column>
    </template>
    <template #expand-row="{ record }">
      <MemberSettings :account="record.account" @changed="emit('changed')">
        <template #actions>
          <!-- 模拟账户不能改启停（后端拒绝）；在组合里的实盘账户要先移出组合才能停用，这里只给未加入组合的实盘账户启停 -->
          <template v-if="record.account.execution_mode === 2 && !record.logical">
            <a-button v-if="record.account.status !== 'ENABLED'" size="small" @click="setStatus(record.account, 'ENABLED')"
              >启用</a-button
            >
            <a-button v-else size="small" status="danger" @click="setStatus(record.account, 'DISABLED')">停用</a-button>
          </template>
        </template>
      </MemberSettings>
    </template>
    <template #empty> {{ environmentLabel(env) }}还没有执行账户。<a-link @click="emit('create')">新建一个</a-link> </template>
  </a-table>

  <JoinDialog ref="joinDialog" :env="env" :logicals="logicals" :accounts="accounts" @changed="emit('changed')" />
  <ConfirmDialog
    v-model:visible="confirmVisible"
    :title="pending?.title ?? ''"
    :env="env"
    :combo-name="pending?.name ?? ''"
    name-label="执行账户名称"
    :need-reason="false"
    :name-required="pending?.nameRequired ?? true"
    :danger="pending?.danger ?? false"
    :confirm-text="pending?.confirmText"
    :submit="runPending"
  >
    <p v-for="line in pending?.lines ?? []" :key="line" class="confirm-line">{{ line }}</p>
  </ConfirmDialog>
</template>

<script setup lang="ts">
import { useWindowSize } from "@vueuse/core";
import { computed, ref, watch } from "vue";
import { useRouter } from "vue-router";
import { Message } from "@arco-design/web-vue";
import TimeText from "@/components/time-text/index.vue";
import { exchangeLabels, updateTradingAccount } from "@/api/trade";
import type { LogicalAccount, TradingAccount } from "@/api/trade";
import ConfirmDialog from "@/components/trade-confirm/confirm-dialog.vue";
import { environmentLabel, type Environment } from "@/components/trade-confirm/environment";
import { useDisplayZone } from "@/hooks/useDisplayZone";
import type { ExecutionRow } from "./account-list";
import JoinDialog from "./join-dialog.vue";
import { fmtMoney } from "./shared/format";
import { marketsText } from "./shared/markets";
import MemberSettings from "./shared/member-settings.vue";
import { accountStatusConfirm, type RiskyConfirm } from "./shared/risky-actions";
import StatusMark from "./shared/status-mark.vue";

const props = defineProps<{
  env: Environment;
  rows: ExecutionRow[];
  logicals: LogicalAccount[];
  accounts: TradingAccount[];
  loading: boolean;
  /** 打开时展开的执行账户（从组合详情点账户名进来时带 ?account=） */
  focusAccountId?: string;
}>();
const emit = defineEmits<{ changed: []; create: [] }>();

// 交易页的时间按当前空间登记的时区显示
const displayZone = useDisplayZone();
const router = useRouter();

// 各列宽度之和：窄屏时表格横向滚动，每一列都保持自己的宽度
const LIST_WIDTH = 40 + 180 + 150 + 170 + 130 + 120 + 120 + 150 + 90;
// 窄屏时右侧操作列不固定，不然会盖住数据列
const { width: viewportWidth } = useWindowSize();
const isNarrow = computed(() => viewportWidth.value < 768);

const expandedKeys = ref<string[]>([]);
const joinDialog = ref<InstanceType<typeof JoinDialog>>();
// 测试网、生产环境下启停执行账户要先确认；模拟账户不能改启停
const confirmVisible = ref(false);
const pending = ref<(RiskyConfirm & { name: string; run: () => Promise<void> }) | null>(null);

watch(
  () => props.focusAccountId,
  id => {
    if (id && !expandedKeys.value.includes(id)) expandedKeys.value = [...expandedKeys.value, id];
  },
  { immediate: true }
);

function openCombo(logicalId: string) {
  void router.push({
    path: `/trading/accounts/${logicalId}`,
    query: { env: props.env === "paper" ? undefined : props.env, list: "execution" }
  });
}

async function runPending() {
  await pending.value?.run();
}

function setStatus(account: TradingAccount, status: "ENABLED" | "DISABLED") {
  const accountId = account.trading_account_id;
  pending.value = {
    ...accountStatusConfirm({ accountName: account.name, enable: status === "ENABLED" }),
    name: account.name,
    run: async () => {
      await updateTradingAccount({ trading_account_id: accountId, status }, { silent: true });
      Message.success(status === "ENABLED" ? "已启用，等首次同步" : "已停用");
      emit("changed");
    }
  };
  confirmVisible.value = true;
}

defineExpose({ setStatus });
</script>

<style src="./shared/accounts.css"></style>
<style scoped>
.confirm-line {
  margin: 0 0 var(--moox-space-2);
}
</style>
