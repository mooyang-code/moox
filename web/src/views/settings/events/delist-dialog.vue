<template>
  <a-modal
    :visible="visible"
    width="640px"
    title="手工登记下架"
    :mask-closable="false"
    @update:visible="(value: boolean) => emit('update:visible', value)"
  >
    <template v-if="phase === 'form'">
      <a-alert v-if="systemActionEnabled === true" class="block-alert">
        用来补录漏报的下架，或做演练。登记后，系统会立即禁止买入这个标的，下架前 24 小时平掉策略持有的部分。
      </a-alert>
      <a-alert v-else type="warning" class="block-alert">
        用来补录漏报的下架，或做演练。{{ systemActionNotice(systemActionEnabled) }}
      </a-alert>
      <a-form :model="form" auto-label-width>
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
          <a-date-picker
            v-model="form.delist_at"
            show-time
            value-format="timestamp"
            format="YYYY-MM-DD HH:mm:ss"
            style="width: 100%"
          />
          <template #extra>填你本地的时间。填已经过去的时间时，只做核对，不会下单。</template>
        </a-form-item>
        <a-form-item field="paper_only" label="仅模拟盘">
          <a-switch v-model="form.paper_only" />
          <template #extra>打开后只处理模拟盘组合，实盘组合不受影响。演练时用。</template>
        </a-form-item>
      </a-form>
    </template>

    <template v-else>
      <p class="summary-title">
        {{ exchangeLabel }} {{ marketLabel }} {{ form.exchange_symbol.trim() }}，下架时间 {{ formatMs(delistAtMs) }}
      </p>
      <a-alert v-if="pastTime" type="warning" class="block-alert">这个时间已经过去，只做核对，不会下单。</a-alert>
      <a-alert v-if="previewFailed" type="warning" class="block-alert">
        暂时无法预览会被平仓的组合（{{ previewError }}）。你可以确认后继续登记，但无法提前知道有哪些组合会受影响。
      </a-alert>
      <a-alert v-else-if="!verified" type="warning" class="block-alert">
        无法确认持有情况：交易服务没有返回持有者列表，不能当成没有组合持有。你可以确认后继续登记，但无法提前知道有哪些组合会受影响。
      </a-alert>
      <template v-else>
        <p v-if="instrumentUnknown" class="muted">
          交易服务里没有这个标的（还没开通对应市场的账户），没有策略持有。{{ noHolderEffect }}
        </p>
        <p v-else-if="!holders.length" class="muted">没有策略持有这个标的。{{ noHolderEffect }}</p>
        <template v-else>
          <p v-if="systemActionEnabled === true">下面这些组合持有这个标的，下架前 24 小时会被平仓：</p>
          <p v-else>下面这些组合持有这个标的：</p>
          <ul class="holder-list">
            <li v-for="holder in summary.live" :key="holderKey(holder)">生产　{{ holderLine(holder) }}</li>
            <li v-for="holder in summary.paper" :key="holderKey(holder)">模拟盘　{{ holderLine(holder) }}</li>
          </ul>
          <template v-if="summary.dust.length">
            <p class="muted">下面是零头，不会被平仓，只在结果里列出：</p>
            <ul class="holder-list muted">
              <li v-for="holder in summary.dust" :key="holderKey(holder)">{{ holderLine(holder) }}</li>
            </ul>
          </template>
        </template>
      </template>
      <a-checkbox v-if="previewFailed || !verified" v-model="confirmed">我知道无法确认持有情况，仍然登记</a-checkbox>
      <a-checkbox v-else-if="summary.live.length" v-model="confirmed">
        {{ systemActionEnabled === true ? "我确认有生产组合会被平仓，仍然登记" : "我确认有生产组合持有这个标的，仍然登记" }}
      </a-checkbox>
    </template>

    <template #footer>
      <a-space>
        <a-button @click="emit('update:visible', false)">取消</a-button>
        <a-button v-if="phase === 'confirm'" @click="phase = 'form'">上一步</a-button>
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
import { computed, reactive, ref, watch } from "vue";
import { Message } from "@arco-design/web-vue";
import {
  listInstrumentHolders,
  registerDelistNotice,
  type DelistExchange,
  type DelistMarket,
  type InstrumentHolder
} from "@/api/callback-dispatcher/delist";
import { reportControlError } from "@/api/admin/http";
import {
  DELIST_EXCHANGES,
  DELIST_MARKETS,
  delistTimeError,
  formatMs,
  holderLine,
  isPastDelistTime,
  pickerValueToMs,
  summarizeHolders,
  systemActionNotice
} from "./event-center-utils";

defineOptions({ name: "EventDelistDialog" });

const props = defineProps<{ visible: boolean; systemActionEnabled: boolean | null }>();
const emit = defineEmits<{ "update:visible": [value: boolean] }>();

const form = reactive({
  exchange: "binance" as DelistExchange,
  market: "SPOT" as DelistMarket,
  exchange_symbol: "",
  delist_at: "" as string | number,
  paper_only: false
});
const phase = ref<"form" | "confirm">("form");
const previewing = ref(false);
const submitting = ref(false);
const holders = ref<InstrumentHolder[]>([]);
const verified = ref(true);
const instrumentUnknown = ref(false);
const previewFailed = ref(false);
const previewError = ref("");
const confirmed = ref(false);

const delistAtMs = computed(() => pickerValueToMs(form.delist_at));
const pastTime = computed(() => isPastDelistTime(delistAtMs.value, Date.now()));
const summary = computed(() => summarizeHolders(holders.value));
const needConfirm = computed(() => previewFailed.value || !verified.value || summary.value.live.length > 0);
const noHolderEffect = computed(() => (props.systemActionEnabled ? "登记后只会禁止买入。" : ""));
const exchangeLabel = computed(() => DELIST_EXCHANGES.find(item => item.value === form.exchange)?.label ?? form.exchange);
const marketLabel = computed(() => DELIST_MARKETS.find(item => item.value === form.market)?.label ?? form.market);

const holderKey = (holder: InstrumentHolder) => `${holder.space_id}/${holder.logical_account_id}/${holder.trading_account_id}`;

watch(
  () => props.visible,
  visible => {
    if (!visible) return;
    Object.assign(form, { exchange: "binance", market: "SPOT", exchange_symbol: "", delist_at: "", paper_only: false });
    phase.value = "form";
    holders.value = [];
    verified.value = true;
    instrumentUnknown.value = false;
    previewFailed.value = false;
    confirmed.value = false;
  }
);

async function preview() {
  if (!form.exchange_symbol.trim()) {
    Message.warning("请填写交易所代码");
    return;
  }
  const timeError = delistTimeError(delistAtMs.value);
  if (timeError) {
    Message.warning(timeError);
    return;
  }
  previewing.value = true;
  confirmed.value = false;
  try {
    const result = await listInstrumentHolders({
      exchange: form.exchange,
      market: form.market,
      exchange_symbol: form.exchange_symbol,
      paper_only: form.paper_only
    });
    holders.value = result.holders;
    verified.value = result.verified;
    instrumentUnknown.value = result.instrumentUnknown;
    previewFailed.value = false;
  } catch (error) {
    holders.value = [];
    verified.value = true;
    instrumentUnknown.value = false;
    previewFailed.value = true;
    previewError.value = error instanceof Error ? error.message : "未知原因";
  } finally {
    previewing.value = false;
  }
  phase.value = "confirm";
}

async function submit() {
  submitting.value = true;
  try {
    const result = await registerDelistNotice({
      exchange: form.exchange,
      market: form.market,
      exchange_symbol: form.exchange_symbol.trim(),
      delist_at_ms: delistAtMs.value,
      paper_only: form.paper_only
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
