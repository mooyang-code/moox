<template>
  <a-modal
    v-model:visible="visible"
    width="min(600px, calc(100vw - 24px))"
    :modal-class="form.kind === 'paper' ? '' : `tx-confirm tx-confirm--${form.kind}`"
    :footer="false"
    unmount-on-close
  >
    <template #title>
      <span>新建账户</span>
      <EnvMark v-if="form.kind !== 'paper'" :env="form.kind" class="title-env" />
    </template>

    <a-steps :current="form.step + 1" size="small" class="steps">
      <a-step v-for="title in stepTitles(form.kind)" :key="title">{{ title }}</a-step>
    </a-steps>

    <a-alert v-if="error" type="error" class="block">{{ error }}</a-alert>

    <!-- 1 类型 -->
    <div v-if="form.step === 0" class="choice" role="radiogroup" aria-label="账户类型">
      <label v-for="item in createKinds" :key="item.key" :class="{ on: form.kind === item.key, off: !item.open }">
        <input v-model="form.kind" type="radio" name="kind" :value="item.key" :disabled="!item.open" />
        <strong>{{ item.label }}</strong>
        <span class="desc">{{ item.desc }}</span>
      </label>
    </div>

    <!-- 2 交易所（实盘还要选密钥） -->
    <a-form v-else-if="form.step === 1" :model="form" layout="vertical">
      <a-form-item label="交易所">
        <a-radio-group v-model="form.exchange" type="button" @change="onExchangeChange">
          <a-radio :value="1">币安</a-radio>
          <a-radio :value="2">OKX</a-radio>
        </a-radio-group>
      </a-form-item>
      <a-form-item v-if="form.kind !== 'paper'" label="交易所密钥" extra="先在「系统设置 / 密钥管理」里录入，这里只选名称">
        <a-select v-model="form.secretId" :loading="secretsLoading" placeholder="选择密钥">
          <a-option v-for="item in secrets" :key="item.secretId" :value="item.secretId">{{ item.name }}</a-option>
        </a-select>
      </a-form-item>
      <a-form-item label="账户名称"><a-input v-model="form.name" :max-length="64" /></a-form-item>
      <!-- 实盘：选完密钥立即检测账户类型和权限，有阻断项或没有结论时“下一步”不放行。
           这一步还没选市场，只拦和市场无关的阻断项（提现权限、密钥被拒绝等）；和市场有关的在下一步按所选市场再检测 -->
      <InspectionPreview
        v-if="form.kind !== 'paper'"
        :exchange="form.exchange"
        :environment="form.kind === 'testnet' ? 1 : 2"
        :secret-id="form.secretId"
        @change="onPreviewChange"
      />
    </a-form>

    <!-- 3 市场与参数 -->
    <a-form v-else-if="form.step === 2" :model="form" layout="vertical">
      <a-form-item label="开通的市场" :extra="marketsExtra">
        <a-space size="large">
          <a-checkbox
            :model-value="form.markets.includes(1)"
            :disabled="!marketAvailable('SPOT')"
            @change="(value: unknown) => setMarket(1, Boolean(value))"
            >现货</a-checkbox
          >
          <a-checkbox
            :model-value="form.markets.includes(2)"
            :disabled="!marketAvailable('SWAP')"
            @change="(value: unknown) => setMarket(2, Boolean(value))"
            >永续</a-checkbox
          >
        </a-space>
      </a-form-item>
      <a-form-item label="结算资产">
        <a-input v-model="form.settlementAsset" :disabled="form.markets.includes(2)" />
      </a-form-item>
      <!-- 实盘：按所选市场再检测一次（万向划转权限、双向持仓等和市场有关的阻断项），通过才能继续 -->
      <InspectionPreview
        v-if="form.kind !== 'paper'"
        :exchange="form.exchange"
        :environment="form.kind === 'testnet' ? 1 : 2"
        :secret-id="form.secretId"
        :markets="form.markets"
        @change="onMarketPreviewChange"
      />
      <template v-if="form.kind === 'paper'">
        <div class="row">
          <a-form-item label="初始资金"><a-input v-model="form.initialBalance" /></a-form-item>
          <a-form-item label="滑点（基点）"><a-input v-model="form.slippageBps" /></a-form-item>
        </div>
        <div class="row">
          <a-form-item label="挂单费率"><a-input v-model="form.makerFeeRate" @input="form.feesEdited = true" /></a-form-item>
          <a-form-item label="吃单费率"><a-input v-model="form.takerFeeRate" @input="form.feesEdited = true" /></a-form-item>
        </div>
        <p class="hint">
          这些参数创建后不能改。费率默认按普通用户的水平给出，可以按自己的实际费率修改。模拟账户用一个资金池同时撮合开通的各个市场。
        </p>
      </template>
      <template v-else-if="form.kind === 'prod'">
        <a-alert type="error" class="block"
          >生产环境账户创建后就有真实交易能力。请确认选的是生产密钥，并且这个密钥没有提现权限。</a-alert
        >
        <a-checkbox v-model="form.ack">我确认这是生产环境密钥，且没有提现权限</a-checkbox>
      </template>
    </a-form>

    <!-- 4 组合 -->
    <div v-else>
      <div class="choice" role="radiogroup" aria-label="组合">
        <label :class="{ on: form.join === 'new' }">
          <input v-model="form.join" type="radio" name="join" value="new" :disabled="Boolean(created.logicalId)" />
          <strong>新建一个组合账户</strong>
          <span class="desc">最常见：一份资金对应一个组合</span>
        </label>
        <label :class="{ on: form.join === 'exist' }">
          <input v-model="form.join" type="radio" name="join" value="exist" :disabled="Boolean(created.logicalId)" />
          <strong>加入已有组合</strong>
          <span class="desc">把多个交易所账户合成一份资金管理</span>
        </label>
      </div>
      <a-form :model="form" layout="vertical" class="block">
        <template v-if="form.join === 'new'">
          <a-form-item label="组合名称"
            ><a-input v-model="form.logicalName" :disabled="Boolean(created.logicalId)" :placeholder="form.name" :max-length="64"
          /></a-form-item>
          <a-form-item
            label="运行方式"
            :extra="form.controlMode === 1 ? '之后在策略管理里把策略实例绑到这个组合' : '只按你的手工订单交易，没有策略目标'"
          >
            <a-radio-group v-model="form.controlMode" type="button" :disabled="Boolean(created.logicalId)">
              <a-radio :value="1">策略驱动</a-radio>
              <a-radio :value="2">自主下单</a-radio>
            </a-radio-group>
          </a-form-item>
        </template>
        <a-form-item
          v-else
          label="选择组合"
          :extra="joinable.length ? '只列出环境和结算资产一致、已暂停自动执行的组合；运行中的组合先去暂停' : ''"
        >
          <a-select v-model="form.existingLogicalId" placeholder="选择组合">
            <a-option v-for="item in joinable" :key="item.logical_account_id" :value="item.logical_account_id">{{
              item.name
            }}</a-option>
          </a-select>
          <template v-if="!joinable.length" #extra
            ><span class="tx-neg">没有可以加入的组合：需要同环境、同结算资产，并且已暂停自动执行</span></template
          >
        </a-form-item>
        <a-form-item
          v-if="form.kind !== 'paper'"
          :extra="`交易所账户里已有持仓或非结算币余额（例如抵扣手续费用的 BNB）时要勾选，否则加不进组合。${ADOPT_EFFECT_TEXT}`"
        >
          <a-checkbox v-model="form.adoptExisting">接管账户里已有的持仓</a-checkbox>
        </a-form-item>
      </a-form>
    </div>

    <div class="footer">
      <a-button @click="back">{{ form.step && !locked ? "上一步" : "取消" }}</a-button>
      <a-button v-if="form.step < 3" type="primary" :disabled="nextDisabled" @click="next">下一步</a-button>
      <a-button v-else-if="form.kind === 'prod'" type="primary" status="danger" :loading="submitting" @click="submit">
        开通生产账户
      </a-button>
      <a-button v-else type="primary" status="success" :loading="submitting" @click="submit">创建</a-button>
    </div>
  </a-modal>
</template>

<script setup lang="ts">
import { computed, reactive, ref, watch } from "vue";
import { Message } from "@arco-design/web-vue";
import {
  addLogicalAccountMember,
  createLogicalAccount,
  createPaperSimulation,
  createTradingAccount,
  isTradeAdoptionRequired
} from "@/api/trade";
import type { LogicalAccount, MarketType } from "@/api/trade";
import InspectionPreview from "@/components/trade-inspection/inspection-preview.vue";
import type { InspectionView } from "@/components/trade-inspection/inspection";
import { createSubmitScope } from "@/utils/submit-scope";
import EnvMark from "@/components/trade-confirm/env-mark.vue";
import type { Environment } from "@/components/trade-confirm/environment";
import { loadExchangeSecrets, type ExchangeSecretOption } from "../shared/exchange-secrets";
import { joinableLogicals, nextPriority } from "../shared/membership";
import { ADOPT_EFFECT_TEXT } from "../shared/risky-actions";
import { accountMapOf } from "../account-list";
import type { TradingAccount } from "@/api/trade";
import {
  buildLiveRequest,
  buildPaperRequest,
  createDefaultForm,
  createKinds,
  defaultFees,
  restrictMarkets,
  retryWhileSessionStarting,
  stepTitles,
  toggleMarket,
  validateStep
} from "./create-model";

defineOptions({ name: "trading-create-account" });

const props = defineProps<{ logicals: LogicalAccount[]; accounts: TradingAccount[] }>();
const emit = defineEmits<{ created: [env: Environment] }>();

const visible = ref(false);
const form = reactive(createDefaultForm());
const error = ref("");
const submitting = ref(false);
const secrets = ref<ExchangeSecretOption[]>([]);
const secretsLoading = ref(false);
const scope = createSubmitScope();
// 实盘第二步的检测结果：allowNext 为真才能离开这一步；可开通的市场用来收窄第三步的勾选
const preview = reactive<{ allowNext: boolean; view: InspectionView | null }>({ allowNext: false, view: null });
// 第三步按所选市场再检测的结果：和市场有关的阻断项在这里判断
const marketPreview = reactive({ allowNext: false });
// 这次已经建成的账户、组合：提交中途失败后重试时用来跳过已完成的部分
const created = reactive({ accountId: "", logicalId: "", joined: false });

// 账户已经建成：前面几步的参数锁定，重试沿用已创建的账户和组合
const locked = computed(() => Boolean(created.accountId));
const selectedKind = computed(() => createKinds.find(item => item.key === form.kind));
const joinable = computed(() => joinableLogicals(props.logicals, accountMapOf(props.accounts), form.kind, form.settlementAsset));

const nextDisabled = computed(() => {
  if (form.step === 0) return !selectedKind.value?.open;
  if (form.kind === "paper") return false;
  if (form.step === 1) return !preview.allowNext;
  return form.step === 2 && !marketPreview.allowNext;
});

// 币安测试网一个账户只能开通一个市场（现货测试网和合约测试网是两套密钥）
const singleMarket = computed(() => form.kind === "testnet" && form.exchange === 1);
const marketsExtra = computed(() =>
  singleMarket.value
    ? "币安测试网一个账户只能开通一个市场：现货测试网和合约测试网是两套密钥；永续固定全仓、USDT 结算"
    : "一个账户可以同时开通现货和永续；永续固定全仓、USDT 结算"
);

watch(visible, () => scope.invalidate());
// 换密钥、换交易所后，之前的“确认这是生产环境密钥且没有提现权限”作废，要对新的密钥重新确认
watch(
  () => [form.secretId, form.exchange],
  () => {
    form.ack = false;
  }
);

// 改环境（测试网、生产环境）后，之前选的密钥属于另一个环境：清掉，检测结果也作废，回到第 2 步时按新环境重新读密钥。
watch(
  () => form.kind,
  () => {
    form.secretId = "";
    secrets.value = [];
    secretsRequest++;
    secretsLoading.value = false;
    Object.assign(preview, { allowNext: false, view: null });
  },
  // 同步执行：环境一改就清掉，不会把紧接着选的新密钥也清掉。
  { flush: "sync" }
);

// 换交易所或环境后进入单选模式（币安测试网），已经勾着的多个市场收成一个，否则两个都勾着、点哪个都取消不掉
watch(
  () => [singleMarket.value, form.markets.length],
  () => {
    if (singleMarket.value && form.markets.length > 1) {
      form.markets = [form.markets[0]];
      syncMarketDefaults();
    }
  }
);

function onPreviewChange(payload: { allowNext: boolean; view: InspectionView | null }) {
  preview.allowNext = payload.allowNext;
  preview.view = payload.view;
  if (payload.view) form.markets = restrictMarkets(form.markets, payload.view.availableMarkets);
}

function onMarketPreviewChange(payload: { allowNext: boolean }) {
  marketPreview.allowNext = payload.allowNext;
}

/** 模拟盘不限制；实盘只能开通检测结果里有的市场（测试网跳过检测、没有市场列表时不限制）。 */
function marketAvailable(market: "SPOT" | "SWAP"): boolean {
  if (form.kind === "paper") return true;
  const available = preview.view?.availableMarkets ?? [];
  return !available.length || available.includes(market);
}

// 密钥列表的请求序号：换交易所、换环境后，旧环境迟到的列表不能覆盖新的。
let secretsRequest = 0;
async function loadSecrets() {
  const requestId = ++secretsRequest;
  secretsLoading.value = true;
  try {
    const list = await loadExchangeSecrets(form.exchange, form.kind === "testnet" ? 1 : 2);
    if (requestId === secretsRequest) secrets.value = list;
  } catch {
    if (requestId === secretsRequest) secrets.value = [];
  } finally {
    if (requestId === secretsRequest) secretsLoading.value = false;
  }
}

function open(kind: Environment) {
  Object.assign(form, createDefaultForm(createKinds.find(item => item.key === kind)?.open ? kind : "paper"));
  Object.assign(created, { accountId: "", logicalId: "", joined: false });
  Object.assign(preview, { allowNext: false, view: null });
  marketPreview.allowNext = false;
  error.value = "";
  submitting.value = false;
  visible.value = true;
}

function onExchangeChange() {
  form.secretId = "";
  Object.assign(preview, { allowNext: false, view: null });
  if (form.kind !== "paper") void loadSecrets();
}

function setMarket(market: MarketType, on: boolean) {
  // 单选模式：勾另一个市场是切换，取消勾选照常生效
  form.markets = singleMarket.value
    ? on
      ? [market]
      : form.markets.filter(item => item !== market)
    : toggleMarket(form.markets, market, on);
  syncMarketDefaults();
}

// 市场变了，结算资产和（没手工改过的）默认费率跟着重算：开通永续固定 USDT 结算，只开现货和带永续的默认费率不同。
function syncMarketDefaults() {
  if (form.markets.includes(2)) form.settlementAsset = "USDT";
  if (!form.feesEdited) {
    const fees = defaultFees(form.markets);
    form.makerFeeRate = fees.maker;
    form.takerFeeRate = fees.taker;
  }
}

function next() {
  const problem = validateStep(form, form.step);
  if (problem) {
    error.value = problem;
    return;
  }
  if (nextDisabled.value) return;
  error.value = "";
  form.step = (form.step + 1) as typeof form.step;
  if (form.step === 1 && form.kind !== "paper") void loadSecrets();
}

function back() {
  error.value = "";
  if (form.step && !locked.value) form.step = (form.step - 1) as typeof form.step;
  else visible.value = false;
}

async function submit() {
  for (const step of [1, 2, 3]) {
    const problem = validateStep(form, step);
    if (problem) {
      error.value = problem;
      form.step = step as typeof form.step;
      return;
    }
  }
  const submission = scope.begin();
  submitting.value = true;
  error.value = "";
  const exist =
    form.join === "exist" ? props.logicals.find(item => item.logical_account_id === form.existingLogicalId) : undefined;
  try {
    // 前面几步已经建成的对象记在 created 里：中途失败后重试，只补没做完的，不会重复建账户（同名会冲突）
    if (!created.accountId) {
      if (form.kind === "paper") {
        // 模拟盘一次请求完成：新建组合或加入已有组合都由后端在同一个事务里做，中途失败不会留下孤立的账户
        created.accountId = (await createPaperSimulation(buildPaperRequest(form))).account.trading_account_id;
        created.joined = true;
      } else {
        created.accountId = (await createTradingAccount(buildLiveRequest(form))).account.trading_account_id;
      }
    }
    if (!created.joined) {
      let target = exist?.logical_account_id ?? created.logicalId;
      if (!target) {
        target = (
          await createLogicalAccount({
            name: form.logicalName.trim() || form.name.trim(),
            control_mode: form.controlMode,
            // 按账户类型取执行模式：模拟盘是 1，测试网和生产环境是 2
            execution_mode: form.kind === "paper" ? 1 : 2,
            settlement_asset: form.settlementAsset.trim().toUpperCase()
          })
        ).logical_account.logical_account_id;
        created.logicalId = target;
      }
      // 实盘：新账户的会话要等运行时的周期检查才建起来，这时加入组合会返回 TRADE_SESSION_NOT_READY，等几秒重试。
      const accountId = created.accountId;
      await retryWhileSessionStarting(() =>
        addLogicalAccountMember({
          logical_account_id: target,
          trading_account_id: accountId,
          enabled: true,
          priority: exist ? nextPriority(exist) : 1,
          adopt_existing_exposure: form.kind !== "paper" && form.adoptExisting
        })
      );
      created.joined = true;
    }
  } catch (cause) {
    const message = cause instanceof Error ? cause.message : "请求失败，请稍后重试";
    // 账户已经建成就通知列表刷新，哪怕对话框已经关闭或重新打开
    if (created.accountId) emit("created", form.kind);
    if (!submission.isCurrent()) return;
    // 后端明确返回需要接管（TRADE_ADOPTION_REQUIRED）时，提示勾选接管后重试
    const adoptHint = created.accountId && isTradeAdoptionRequired(cause) ? "勾选下方“接管账户里已有的持仓”后再点一次。" : "";
    error.value = created.accountId
      ? `执行账户已创建，但加入组合没有完成：${message}。前面几步的参数已锁定，修正后再点一次只会补做加入组合。${adoptHint}`
      : message;
    submitting.value = false;
    return;
  }
  emit("created", form.kind);
  if (!submission.isCurrent()) return;
  submitting.value = false;
  visible.value = false;
  Message.success("账户已创建");
}

defineExpose({ open, form, next, submit, visible });
</script>

<style src="../shared/accounts.css"></style>
<style scoped>
.steps {
  margin-bottom: var(--moox-space-4);
}

.block {
  margin-bottom: var(--moox-space-3);
}

.choice + .block {
  margin-top: var(--moox-space-4);
}

.title-env {
  margin-left: var(--moox-space-3);
  font-size: 13px;
}

.choice {
  display: grid;
  gap: var(--moox-space-2);
}

.choice label {
  display: grid;
  grid-template-columns: auto 1fr;
  gap: 2px 10px;
  padding: 10px 12px;
  border: 1px solid var(--color-border-2);
  border-radius: var(--border-radius-small);
  cursor: pointer;
}

.choice label.on {
  border-color: rgb(var(--arcoblue-6));
  background: var(--color-primary-light-1);
}

.choice label.off {
  cursor: not-allowed;
  opacity: 0.55;
}

.choice input {
  grid-row: span 2;
  margin-top: 4px;
}

.desc {
  color: var(--color-text-3);
  font-size: 12px;
}

.row {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  column-gap: var(--moox-space-4);
}

.hint {
  margin: 0;
  color: var(--color-text-3);
  font-size: 12px;
}

.footer {
  display: flex;
  justify-content: flex-end;
  gap: var(--moox-space-2);
  margin-top: var(--moox-space-4);
}
</style>
