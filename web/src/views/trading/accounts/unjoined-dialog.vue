<template>
  <a-modal
    v-model:visible="visible"
    width="min(760px, calc(100vw - 24px))"
    :footer="false"
    unmount-on-close
    @close="mode = 'list'"
  >
    <template #title>
      <span>{{ mode === "join" && joining ? `把「${joining.name}」加入组合` : "未加入组合的执行账户" }}</span>
      <EnvMark :env="env" class="title-env" />
    </template>

    <template v-if="mode === 'list'">
      <p class="hint">
        这些账户不会交易，通常是从组合里移出的，或者停用后留下的。加入组合后才会被策略或手工下单用到。点左边的三角展开设置。
      </p>
      <a-table
        row-key="trading_account_id"
        size="small"
        :data="rows"
        :pagination="false"
        v-model:expanded-keys="expandedKeys"
        :expandable="{ width: 40 }"
      >
        <template #columns>
          <a-table-column title="执行账户">
            <template #cell="{ record }">
              <strong>{{ record.name }}</strong>
              <div class="tx-muted sub">
                {{ exchangeLabels[record.exchange] || "-" }} · {{ marketsText(record.markets) }} · 同步于
                {{ fmtClock(record.last_sync_at) }}
              </div>
            </template>
          </a-table-column>
          <a-table-column title="状态" :width="110">
            <template #cell="{ record }">
              <StatusMark :tone="record.status === 'ENABLED' ? (record.ready ? 'ok' : 'bad') : ''">
                {{ record.status === "ENABLED" ? (record.ready ? "就绪" : "未就绪") : "已停用" }}
              </StatusMark>
            </template>
          </a-table-column>
          <a-table-column title="权益" :width="110" align="right">
            <template #cell="{ record }"
              ><span class="tx-num">{{ fmtMoney(record.snapshot?.equity) }}</span></template
            >
          </a-table-column>
          <a-table-column title="操作" :width="190">
            <template #cell="{ record }">
              <a-tooltip content="先展开这一行，在设置里启用" :disabled="record.status === 'ENABLED'">
                <a-button size="mini" type="text" :disabled="record.status !== 'ENABLED'" @click="openJoin(record)"
                  >加入组合</a-button
                >
              </a-tooltip>
            </template>
          </a-table-column>
        </template>
        <template #expand-row="{ record }">
          <MemberSettings :account="record" @changed="emit('changed')">
            <template #actions>
              <!-- 模拟账户不能改启停（后端拒绝），只有实盘账户给启用、停用 -->
              <template v-if="record.execution_mode === 2">
                <a-button v-if="record.status !== 'ENABLED'" size="small" @click="setStatus(record, 'ENABLED')">启用</a-button>
                <a-button v-else size="small" status="danger" @click="setStatus(record, 'DISABLED')">停用</a-button>
              </template>
            </template>
          </MemberSettings>
        </template>
        <template #empty>{{ environmentLabel(env) }}下的执行账户都已加入组合</template>
      </a-table>
      <div class="footer"><a-button @click="visible = false">关闭</a-button></div>
    </template>

    <template v-else-if="joining">
      <a-alert v-if="error" type="error" class="block">{{ error }}</a-alert>
      <a-form :model="joinForm" layout="vertical">
        <a-form-item
          label="加入哪个组合"
          :extra="options.length ? '只列出环境和结算资产一致、已暂停自动执行的组合；运行中的组合先去暂停' : ''"
        >
          <a-select v-model="joinForm.logicalId" placeholder="选择组合">
            <a-option v-for="item in options" :key="item.logical_account_id" :value="item.logical_account_id">{{
              item.name
            }}</a-option>
          </a-select>
          <template v-if="!options.length" #extra
            ><span class="tx-neg">没有可以加入的组合，先新建一个或暂停运行中的组合</span></template
          >
        </a-form-item>
        <a-form-item label="优先级" extra="数字越小越先使用">
          <a-input-number v-model="joinForm.priority" :min="0" />
        </a-form-item>
        <a-form-item :extra="ADOPT_EFFECT_TEXT">
          <a-checkbox v-model="joinForm.adopt">接管账户里已有的持仓</a-checkbox>
        </a-form-item>
      </a-form>
      <div class="footer">
        <a-button @click="mode = 'list'">返回</a-button>
        <a-button type="primary" :loading="saving" :disabled="!joinForm.logicalId" @click="requestJoin">加入</a-button>
      </div>
    </template>

    <ConfirmDialog
      v-model:visible="confirmVisible"
      :title="pending?.title ?? ''"
      :env="pending?.env ?? env"
      :combo-name="pending?.name ?? ''"
      :name-label="pending?.nameLabel"
      :need-reason="false"
      :name-required="pending?.nameRequired ?? true"
      :danger="pending?.danger ?? false"
      :confirm-text="pending?.confirmText"
      :submit="runPending"
    >
      <p v-for="line in pending?.lines ?? []" :key="line" class="confirm-line">{{ line }}</p>
    </ConfirmDialog>
  </a-modal>
</template>

<script setup lang="ts">
import { computed, reactive, ref, watch } from "vue";
import { Message } from "@arco-design/web-vue";
import { addLogicalAccountMember, exchangeLabels, updateTradingAccount } from "@/api/trade";
import type { LogicalAccount, TradingAccount } from "@/api/trade";
import { createSubmitScope } from "@/utils/submit-scope";
import { accountMapOf } from "./account-list";
import ConfirmDialog from "./shared/confirm-dialog.vue";
import EnvMark from "./shared/env-mark.vue";
import { accountEnvironment, environmentLabel, type Environment } from "./shared/environment";
import { fmtClock, fmtMoney } from "./shared/format";
import { marketsText } from "./shared/markets";
import MemberSettings from "./shared/member-settings.vue";
import { joinableLogicals, nextPriority } from "./shared/membership";
import { ADOPT_EFFECT_TEXT, accountStatusConfirm, memberChangeConfirm, type RiskyConfirm } from "./shared/risky-actions";
import StatusMark from "./shared/status-mark.vue";

const props = defineProps<{
  env: Environment;
  /** 当前环境下未加入组合的执行账户 */
  rows: TradingAccount[];
  logicals: LogicalAccount[];
  accounts: TradingAccount[];
}>();
const emit = defineEmits<{ changed: [] }>();

const visible = ref(false);
const mode = ref<"list" | "join">("list");
const expandedKeys = ref<string[]>([]);
const joining = ref<TradingAccount | null>(null);
const joinForm = reactive({ logicalId: "", priority: 1, adopt: false });
const saving = ref(false);
const error = ref("");
const scope = createSubmitScope();
// 测试网、生产环境下启停执行账户、接管已有持仓要先确认；模拟账户不能改启停，接管也按环境分档。
const confirmVisible = ref(false);
const pending = ref<(RiskyConfirm & { env: Environment; name: string; nameLabel: string; run: () => Promise<void> }) | null>(
  null
);

async function runPending() {
  await pending.value?.run();
}

const options = computed(() =>
  joining.value ? joinableLogicals(props.logicals, accountMapOf(props.accounts), props.env, joining.value.settlement_asset) : []
);

watch(visible, () => scope.invalidate());

function open() {
  mode.value = "list";
  expandedKeys.value = [];
  visible.value = true;
}

function openJoin(account: TradingAccount) {
  joining.value = account;
  const first = options.value[0];
  Object.assign(joinForm, {
    logicalId: first?.logical_account_id ?? "",
    priority: first ? nextPriority(first) : 1,
    adopt: false
  });
  error.value = "";
  mode.value = "join";
}

function setStatus(account: TradingAccount, status: "ENABLED" | "DISABLED") {
  const accountId = account.trading_account_id;
  pending.value = {
    ...accountStatusConfirm({ accountName: account.name, enable: status === "ENABLED" }),
    env: accountEnvironment(account),
    name: account.name,
    nameLabel: "执行账户名称",
    run: async () => {
      await updateTradingAccount({ trading_account_id: accountId, status }, { silent: true });
      Message.success(status === "ENABLED" ? "已启用，等首次同步" : "已停用");
      emit("changed");
    }
  };
  confirmVisible.value = true;
}

/** 勾了接管已有持仓时先确认（模拟盘直接加入）；否则直接加入。 */
function requestJoin() {
  if (!joining.value || !joinForm.logicalId) return;
  const target = options.value.find(item => item.logical_account_id === joinForm.logicalId);
  const confirm = joinForm.adopt
    ? memberChangeConfirm({
        accountName: joining.value.name,
        comboName: target?.name ?? joinForm.logicalId,
        original: null,
        next: { enabled: true, priority: joinForm.priority },
        adopt: true
      })
    : null;
  if (!confirm || props.env === "paper") {
    void join();
    return;
  }
  pending.value = {
    ...confirm,
    env: props.env,
    name: target?.name ?? joinForm.logicalId,
    nameLabel: "组合名称",
    run: async () => {
      // 确认框里只关掉确认框；加入失败的原因显示在加入表单上
      await join();
    }
  };
  confirmVisible.value = true;
}

async function join() {
  if (!joining.value || !joinForm.logicalId) return;
  const submission = scope.begin();
  saving.value = true;
  error.value = "";
  try {
    await addLogicalAccountMember({
      logical_account_id: joinForm.logicalId,
      trading_account_id: joining.value.trading_account_id,
      enabled: true,
      priority: joinForm.priority,
      adopt_existing_exposure: joinForm.adopt
    });
  } catch (cause) {
    if (submission.isCurrent()) error.value = cause instanceof Error ? cause.message : "请求失败，请稍后重试";
    saving.value = false;
    return;
  }
  saving.value = false;
  if (!submission.isCurrent()) return;
  Message.success("已加入组合");
  mode.value = "list";
  emit("changed");
}

defineExpose({ open });
</script>

<style src="./shared/accounts.css"></style>
<style scoped>
.title-env {
  margin-left: var(--moox-space-3);
  font-size: 13px;
}

.hint {
  margin: 0 0 var(--moox-space-3);
  color: var(--color-text-2);
  font-size: 13px;
}

.sub {
  font-size: 12px;
}

.block {
  margin-bottom: var(--moox-space-3);
}

.confirm-line {
  margin: 0 0 var(--moox-space-2);
}

.footer {
  display: flex;
  justify-content: flex-end;
  gap: var(--moox-space-2);
  margin-top: var(--moox-space-4);
}
</style>
