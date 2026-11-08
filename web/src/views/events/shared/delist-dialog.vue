<template>
  <a-modal
    :visible="visible"
    width="min(640px, 100vw)"
    title="手工登记下架"
    :mask-closable="false"
    @update:visible="(value: boolean) => emit('update:visible', value)"
  >
    <template v-if="phase === 'form'">
      <a-alert v-if="systemRuleEnabled === false" type="warning" class="block-alert">
        系统订阅“标的即将下架时，卖出策略持有的这个币”已停用：新登记的下架不会被处理，不会禁止买入，也不会平仓。同一个标的已经在处理的下架照常处理，登记新的下架时间会照常改期。
      </a-alert>
      <a-alert v-else-if="systemRuleEnabled === null" type="warning" class="block-alert">
        用来补录漏报的下架，或做演练。{{ SYSTEM_RULE_UNKNOWN_TEXT }}
      </a-alert>
      <a-alert v-else-if="systemActionEnabled === true" class="block-alert">
        用来补录漏报的下架，或做演练。登记后，系统会立即禁止买入这个标的，下架前 24 小时平掉策略持有的部分。
      </a-alert>
      <a-alert v-else type="warning" class="block-alert">
        用来补录漏报的下架，或做演练。{{ systemActionNotice(systemActionEnabled) }}
      </a-alert>
      <a-form :model="form" auto-label-width :disabled="previewing">
        <a-form-item field="exchange" label="交易所" required>
          <a-select v-model="form.exchange">
            <a-option v-for="item in DELIST_EXCHANGES" :key="item.value" :value="item.value">{{ item.label }}</a-option>
          </a-select>
        </a-form-item>
        <a-form-item field="market" label="市场" required>
          <a-select v-model="form.market">
            <a-option v-for="item in DELIST_MARKETS" :key="item.value" :value="item.value">{{ item.label }}</a-option>
          </a-select>
        </a-form-item>
        <a-form-item field="exchange_symbol" label="交易所代码" required>
          <a-input v-model="form.exchange_symbol" placeholder="例如：FOOUSDT（币安）或 FOO-USDT-SWAP（OKX）" />
        </a-form-item>
        <a-form-item field="delist_at" label="下架时间" required>
          <ZonedDatePicker v-model="form.delist_at" zone="UTC" style="width: 100%" />
          <template #extra>{{ DELIST_TIME_HINT }}</template>
        </a-form-item>
        <a-form-item field="paper_only" label="仅模拟盘">
          <a-switch v-model="form.paper_only" />
          <template #extra>演练时用。{{ PAPER_ONLY_NOTE }}</template>
        </a-form-item>
      </a-form>
    </template>

    <template v-else>
      <p class="summary-title">
        {{ summaryTitle }}
      </p>
      <a-alert v-if="snapshot?.paper_only" type="warning" class="block-alert">这是演练。{{ PAPER_ONLY_NOTE }}</a-alert>
      <a-alert v-if="pastTime" type="warning" class="block-alert">{{ pastTimeNote(Boolean(snapshot?.paper_only)) }}</a-alert>
      <a-alert v-if="previewFailed" type="warning" class="block-alert">
        暂时无法预览会被平仓的组合（{{ previewError }}）。你可以确认后继续登记，但无法提前知道有哪些组合会受影响。
      </a-alert>
      <a-alert v-else-if="!verified" type="warning" class="block-alert">
        无法确认持有情况：交易服务没有返回持有者列表，不能当成没有组合持有。你可以确认后继续登记，但无法提前知道有哪些组合会受影响。
      </a-alert>
      <template v-else>
        <p v-if="instrumentUnknown" class="muted">
          交易服务不认识这个交易所代码：可能是代码写错了，也可能还没开通对应市场的账户。请先核对代码；代码无误的话，没有策略持有这个标的。{{
            noHolderEffect
          }}
        </p>
        <p v-else-if="!holders.length && !skippedAccounts.length" class="muted">没有策略持有这个标的。{{ noHolderEffect }}</p>
        <template v-else-if="holders.length">
          <p v-if="willProcess">下面这些组合持有这个标的，会在下架前 24 小时（已不足 24 小时的立即）被平仓：</p>
          <p v-else>下面这些组合持有这个标的：</p>
          <ul class="holder-list">
            <li v-for="holder in summary.live" :key="holderKey(holder)">实盘　{{ holderLine(holder) }}</li>
            <li v-for="holder in summary.paper" :key="holderKey(holder)">模拟盘　{{ holderLine(holder) }}</li>
          </ul>
          <template v-if="summary.dust.length">
            <p class="muted">下面是零头，不会被平仓，只在结果里列出：</p>
            <ul class="holder-list muted">
              <li v-for="holder in summary.dust" :key="holderKey(holder)">{{ holderLine(holder) }}</li>
            </ul>
          </template>
        </template>
        <template v-if="skippedAccounts.length">
          <a-alert type="warning" class="block-alert">
            下面这些账户没能判断是否持有这个标的，不能当作没有持有，登记前请先确认：
          </a-alert>
          <ul class="holder-list">
            <li v-for="account in skippedAccounts" :key="account.trading_account_id">{{ skippedLine(account) }}</li>
          </ul>
        </template>
        <template v-if="notes.length">
          <p class="muted">Trade 的说明：</p>
          <ul class="holder-list muted">
            <li v-for="note in notes" :key="note">{{ note }}</li>
          </ul>
        </template>
      </template>
      <a-checkbox v-if="confirmText" v-model="confirmed">{{ confirmText }}</a-checkbox>
    </template>

    <template #footer>
      <a-space>
        <a-button @click="emit('update:visible', false)">取消</a-button>
        <a-button v-if="phase === 'confirm'" :disabled="submitting" @click="phase = 'form'">上一步</a-button>
        <a-button v-if="phase === 'form'" type="primary" :loading="previewing" @click="preview">下一步</a-button>
        <a-button
          v-else
          type="primary"
          status="danger"
          :disabled="needConfirm && !confirmed"
          :loading="submitting"
          @click="submit"
        >
          确认登记
        </a-button>
      </a-space>
    </template>
  </a-modal>
</template>

<script setup lang="ts">
// 事件中心和空间无关，时间都按 UTC 显示
import { formatInstant } from "@/utils/time-zone";
import ZonedDatePicker from "@/components/zoned-date-picker/zoned-date-picker.vue";
import { computed, reactive, ref, watch } from "vue";
import { Message } from "@arco-design/web-vue";
import {
  listInstrumentHolders,
  registerDelistNotice,
  type DelistExchange,
  type DelistMarket,
  type InstrumentHolder,
  type SkippedAccount
} from "@/api/callback-dispatcher/delist";
import { reportControlError } from "@/api/admin/http";
import {
  DELIST_EXCHANGES,
  DELIST_MARKETS,
  DELIST_TIME_HINT,
  PAPER_ONLY_NOTE,
  SYSTEM_RULE_UNKNOWN_TEXT,
  confirmCheckboxText,
  createLatestGuard,
  delistSnapshot,
  holderLine,
  pastTimeNote,
  requestErrorText,
  skippedLine,
  summarizeHolders,
  systemActionNotice,
  type DelistSnapshot
} from "./event-center-utils";

defineOptions({ name: "EventDelistDialog" });

const props = defineProps<{ visible: boolean; systemActionEnabled: boolean | null; systemRuleEnabled: boolean | null }>();
const emit = defineEmits<{ "update:visible": [value: boolean] }>();

const form = reactive({
  exchange: "binance" as DelistExchange,
  market: "SPOT" as DelistMarket,
  exchange_symbol: "",
  // 毫秒时刻，按 UTC 选择
  delist_at: null as number | null,
  paper_only: false
});
const phase = ref<"form" | "confirm">("form");
const previewing = ref(false);
const submitting = ref(false);
// 点“下一步”时存下的登记参数：确认页和提交都只用它，不再读表单。
const snapshot = ref<DelistSnapshot | null>(null);
const holders = ref<InstrumentHolder[]>([]);
const skippedAccounts = ref<SkippedAccount[]>([]);
const notes = ref<string[]>([]);
const verified = ref(true);
const instrumentUnknown = ref(false);
const previewFailed = ref(false);
const previewError = ref("");
const confirmed = ref(false);
// 预览请求的序号：迟到的旧响应（连点、关掉重开）直接丢掉。
const previewGuard = createLatestGuard();

const pastTime = computed(() => Boolean(snapshot.value?.past));
const summary = computed(() => summarizeHolders(holders.value));
const confirmText = computed(() =>
  confirmCheckboxText({
    previewFailed: previewFailed.value,
    verified: verified.value,
    skipped: skippedAccounts.value.length,
    live: summary.value.live.length,
    willProcess: willProcess.value
  })
);
const needConfirm = computed(
  () => previewFailed.value || !verified.value || skippedAccounts.value.length > 0 || summary.value.live.length > 0
);
// 系统动作已启用并且确认系统规则已启用，登记的下架才会真的被处理（规则状态读不到时不能当成已启用）；下架时间已经过去时只做核对，不会平仓。
const willProcess = computed(() => props.systemActionEnabled === true && props.systemRuleEnabled === true && !pastTime.value);
const noHolderEffect = computed(() => (willProcess.value ? "登记后只会禁止买入。" : ""));
const summaryTitle = computed(() => {
  const value = snapshot.value;
  if (!value) return "";
  const exchange = DELIST_EXCHANGES.find(item => item.value === value.exchange)?.label ?? value.exchange;
  const market = DELIST_MARKETS.find(item => item.value === value.market)?.label ?? value.market;
  return `${exchange} ${market} ${value.exchange_symbol}，下架时间 ${formatInstant(value.delist_at_ms, "UTC")}`;
});

const holderKey = (holder: InstrumentHolder) => `${holder.space_id}/${holder.logical_account_id}/${holder.trading_account_id}`;

function resetPreview() {
  holders.value = [];
  skippedAccounts.value = [];
  notes.value = [];
  verified.value = true;
  instrumentUnknown.value = false;
  previewFailed.value = false;
  previewError.value = "";
  confirmed.value = false;
}

watch(
  () => props.visible,
  visible => {
    // 关掉或重新打开都换号：上一次没回来的预览响应不会再写进来。
    previewGuard.reset();
    if (!visible) return;
    Object.assign(form, { exchange: "binance", market: "SPOT", exchange_symbol: "", delist_at: null, paper_only: false });
    phase.value = "form";
    previewing.value = false;
    submitting.value = false;
    snapshot.value = null;
    resetPreview();
  }
);

async function preview() {
  if (previewing.value) return;
  const built = delistSnapshot(form, Date.now());
  if ("error" in built) {
    Message.warning(built.error);
    return;
  }
  const request = built.snapshot;
  const ticket = previewGuard.next();
  previewing.value = true;
  resetPreview();
  try {
    const result = await listInstrumentHolders(request);
    if (!previewGuard.isLatest(ticket)) return;
    holders.value = result.holders;
    skippedAccounts.value = result.skippedAccounts;
    notes.value = result.notes;
    verified.value = result.verified;
    instrumentUnknown.value = result.instrumentUnknown;
  } catch (error) {
    if (!previewGuard.isLatest(ticket)) return;
    previewFailed.value = true;
    previewError.value = requestErrorText(error);
  } finally {
    if (previewGuard.isLatest(ticket)) previewing.value = false;
  }
  if (!previewGuard.isLatest(ticket)) return;
  snapshot.value = request;
  phase.value = "confirm";
}

async function submit() {
  const value = snapshot.value;
  if (!value || submitting.value) return;
  submitting.value = true;
  try {
    const result = await registerDelistNotice({
      exchange: value.exchange,
      market: value.market,
      exchange_symbol: value.exchange_symbol,
      delist_at_ms: value.delist_at_ms,
      paper_only: value.paper_only
    });
    Message.success(result.created ? "已登记，稍后可以在执行记录里看到处理进度" : "这条下架之前已经登记过");
    emit("update:visible", false);
  } catch (error) {
    reportControlError(error);
  } finally {
    submitting.value = false;
  }
}
</script>

<style scoped>
.block-alert {
  margin-bottom: 12px;
}

.summary-title {
  margin: 0 0 12px;
  font-weight: 600;
}

.holder-list {
  margin: 0 0 12px;
  padding-left: 18px;
  line-height: 1.8;
}

.muted {
  color: var(--color-text-3);
}
</style>
