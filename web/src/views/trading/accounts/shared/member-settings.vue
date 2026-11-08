<template>
  <div class="member-settings">
    <div class="block">
      <a-form :model="{}" layout="vertical">
        <a-form-item v-if="!paper" label="名称">
          <a-input v-model="name" :max-length="64" />
        </a-form-item>
        <template v-if="!paper">
          <a-form-item label="交易所密钥" extra="从「系统设置 / 秘钥管理」里选，不在这里填密钥原文">
            <a-select v-model="secretId" :loading="secretsLoading" placeholder="选择密钥">
              <a-option v-for="item in secrets" :key="item.secretId" :value="item.secretId">{{ item.name }}</a-option>
              <a-option v-if="secretId && !secrets.some(item => item.secretId === secretId)" :value="secretId">{{
                secretId
              }}</a-option>
            </a-select>
          </a-form-item>
        </template>
        <!-- 后端不允许改模拟账户的名称、杠杆和启停，只读展示 -->
        <a-descriptions v-else :column="1" size="small" :label-style="{ width: '80px' }">
          <a-descriptions-item label="名称">{{ account.name }}</a-descriptions-item>
          <template v-if="account.paper">
            <a-descriptions-item label="初始资金"
              >{{ fmtMoney(account.paper.initial_balance, 0) }} {{ account.settlement_asset }}</a-descriptions-item
            >
            <a-descriptions-item label="挂单费率">{{ account.paper.maker_fee_rate }}</a-descriptions-item>
            <a-descriptions-item label="吃单费率">{{ account.paper.taker_fee_rate }}</a-descriptions-item>
            <a-descriptions-item label="滑点">{{ account.paper.slippage_bps }} 基点</a-descriptions-item>
          </template>
        </a-descriptions>
        <p v-if="paper" class="hint">模拟账户的名称、费率、滑点、杠杆创建后都不能改；要换参数就新建一个模拟账户。</p>
      </a-form>
      <a-space>
        <a-button v-if="!paper" type="primary" size="small" :loading="saving" :disabled="!dirty" @click="save">保存</a-button>
        <slot name="actions" />
      </a-space>
    </div>

    <div class="block">
      <div v-if="account.markets?.includes(2)">
        <div class="block-title">杠杆</div>
        <a-table size="small" row-key="key" :data="leverageRows" :pagination="false">
          <template #columns>
            <a-table-column title="标的"
              ><template #cell="{ record }">{{ record.label }}</template></a-table-column
            >
            <a-table-column title="倍数" :width="90" align="right"
              ><template #cell="{ record }">{{ record.value }}×</template></a-table-column
            >
          </template>
        </a-table>
        <div v-if="!paper" class="leverage-form">
          <a-input v-model="leverageInstrument" placeholder="标的，如 BTC-USDT-SWAP" />
          <a-input v-model="leverageValue" placeholder="倍数" class="leverage-value" />
          <a-button size="small" :loading="leverageSaving" @click="saveLeverage">设置</a-button>
        </div>
      </div>
      <!-- 账户类型与检测结果、重新检测；模拟账户没有检测。检测可能改变账户类型，完成后通知详情页重读 -->
      <AccountInspectionPanel
        v-if="!paper"
        :account-id="account.trading_account_id"
        :inspection-json="account.inspection_json"
        @updated="emit('changed')"
      />
    </div>

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
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref, watch } from "vue";
import { Message } from "@arco-design/web-vue";
import { setLeverage, updateTradingAccount } from "@/api/trade";
import type { TradingAccount } from "@/api/trade";
import AccountInspectionPanel from "@/components/trade-inspection/account-inspection-panel.vue";
import ConfirmDialog from "./confirm-dialog.vue";
import { accountEnvironment } from "./environment";
import { fmtMoney } from "./format";
import { loadExchangeSecrets, type ExchangeSecretOption } from "./exchange-secrets";
import { leverageConfirm, leverageError, secretChangeConfirm, type RiskyConfirm } from "./risky-actions";

const props = defineProps<{ account: TradingAccount }>();
const emit = defineEmits<{ changed: [] }>();

const paper = computed(() => props.account.execution_mode === 1);
const name = ref(props.account.name);
const secretId = ref(props.account.live?.credential_secret_id ?? "");
const secrets = ref<ExchangeSecretOption[]>([]);
const secretsLoading = ref(false);
const saving = ref(false);
const leverageInstrument = ref("");
const leverageValue = ref("");
const leverageSaving = ref(false);
// 测试网、生产环境下换密钥、设杠杆要先确认：确认框里的内容和要执行的请求在打开时定下来，之后的 5 秒轮询不影响。
const env = computed(() => accountEnvironment(props.account));
const confirmVisible = ref(false);
const pending = ref<(RiskyConfirm & { name: string; run: () => Promise<void> }) | null>(null);

const dirty = computed(
  () =>
    name.value.trim() !== "" &&
    (name.value.trim() !== props.account.name ||
      (props.account.execution_mode === 2 && secretId.value !== (props.account.live?.credential_secret_id ?? "")))
);
const leverageRows = computed(() =>
  Object.entries(props.account.leverage_settings || {}).map(([key, value]) => ({
    key,
    value,
    label: key === "*" ? "默认（全部标的）" : key
  }))
);

// 详情页每 5 秒整组换一次账户对象：只有名称、密钥的值真的变了才回填，否则正在改的内容会被冲掉。
watch(
  () => props.account.name,
  value => {
    name.value = value;
  }
);
watch(
  () => props.account.live?.credential_secret_id ?? "",
  value => {
    secretId.value = value;
  }
);

onMounted(async () => {
  if (props.account.execution_mode !== 2) return;
  secretsLoading.value = true;
  try {
    secrets.value = await loadExchangeSecrets(props.account.exchange, props.account.live?.environment === 1 ? 1 : 2);
  } catch {
    secrets.value = [];
  } finally {
    secretsLoading.value = false;
  }
});

function secretName(id: string): string {
  return secrets.value.find(item => item.secretId === id)?.name ?? id;
}

function openConfirm(confirm: RiskyConfirm, run: () => Promise<void>) {
  pending.value = { ...confirm, name: props.account.name, run };
  confirmVisible.value = true;
}

async function runPending() {
  await pending.value?.run();
}

function save() {
  const accountId = props.account.trading_account_id;
  const nextName = name.value.trim() !== props.account.name ? name.value.trim() : undefined;
  const fromSecret = props.account.live?.credential_secret_id ?? "";
  const nextSecret = props.account.execution_mode === 2 && secretId.value !== fromSecret ? secretId.value : undefined;
  const submit = async (silent: boolean) => {
    saving.value = true;
    try {
      await updateTradingAccount({ trading_account_id: accountId, name: nextName, credential_secret_id: nextSecret }, { silent });
      Message.success("已保存");
      emit("changed");
    } finally {
      saving.value = false;
    }
  };
  // 只改名称不用确认；模拟账户没有密钥
  if (nextSecret === undefined || env.value === "paper") {
    void submit(false).catch(() => undefined);
    return;
  }
  openConfirm(
    secretChangeConfirm({ accountName: props.account.name, from: secretName(fromSecret), to: secretName(nextSecret) }),
    () => submit(true)
  );
}

function saveLeverage() {
  const instrument = leverageInstrument.value.trim().toUpperCase();
  if (!instrument) {
    Message.warning("请填写标的");
    return;
  }
  const invalid = leverageError(leverageValue.value);
  if (invalid) {
    Message.warning(invalid);
    return;
  }
  const accountId = props.account.trading_account_id;
  const next = leverageValue.value.trim();
  const submit = async () => {
    leverageSaving.value = true;
    try {
      await setLeverage({ trading_account_id: accountId, instrument_id: instrument, leverage: next }, { silent: true });
      Message.success("杠杆已设置，下次开仓前生效");
      leverageInstrument.value = "";
      leverageValue.value = "";
      emit("changed");
    } finally {
      leverageSaving.value = false;
    }
  };
  // 杠杆只有实盘账户能设（模拟账户不显示这一栏），所以总是先确认
  openConfirm(leverageConfirm({ instrument, current: props.account.leverage_settings?.[instrument], next }), submit);
}
</script>

<style src="./accounts.css"></style>
<style scoped>
.member-settings {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(280px, 1fr));
  gap: var(--moox-space-4) var(--moox-space-6);
  padding: 12px 16px 16px;
  background: var(--color-fill-1);
}

.block {
  display: grid;
  align-content: start;
  gap: var(--moox-space-3);
}

.block-title {
  font-size: 13px;
  font-weight: 600;
}

.hint {
  margin: 0;
  color: var(--color-text-3);
  font-size: 12px;
}

.leverage-form {
  display: flex;
  gap: var(--moox-space-2);
  margin-top: var(--moox-space-2);
}

.leverage-value {
  width: 80px;
  flex: none;
}

.confirm-line {
  margin: 0 0 var(--moox-space-2);
}
</style>
