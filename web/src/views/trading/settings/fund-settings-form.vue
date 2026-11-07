<template>
  <a-form :model="model" layout="vertical" class="fund-settings-form">
    <section class="settings-group">
      <h4>资金预留</h4>
      <a-form-item label="资金预留比例" extra="换算目标时从权益里扣掉的比例，0.05 表示 5%">
        <a-input v-model="model.reserve_ratio" />
      </a-form-item>
    </section>

    <section v-if="liveOnly" class="settings-group">
      <h4>BNB（币安）</h4>
      <a-form-item label="BNB 抵扣手续费" extra="开启后会在币安账户上打开 BNB 抵扣开关，现货、合约一起设">
        <a-switch v-model="model.bnb_discount" />
      </a-form-item>
      <a-form-item
        label="BNB 保留金额"
        extra="BNB 市值低于这个金额减 10 时市价补买到这个金额；0 表示不保留。保留期间 BNB 不参与策略持仓"
      >
        <a-input v-model="model.bnb_reserve_amount" :suffix="unit" />
      </a-form-item>
      <a-form-item
        label="小额资产兑换 BNB"
        extra="定期把 MooX 交易留下的零头（不在当前目标里、不是排除资产、低于 5 个结算币）兑成 BNB；你自己放在账户里、MooX 没交易过的币不动"
      >
        <a-switch v-model="model.dust_convert_enabled" />
      </a-form-item>
      <a-form-item v-if="model.dust_convert_enabled" label="兑换间隔（天）">
        <a-input-number v-model="model.dust_convert_interval_days" :min="1" :max="365" :precision="0" />
      </a-form-item>
    </section>

    <section v-if="liveOnly" class="settings-group">
      <h4>稳定币保证金（币安统一账户）</h4>
      <p class="settings-hint">
        各稳定币的目标比例，必须包含结算币（{{ unit }}）且比例之和为 1。每小时检查一次，偏离目标超过 5%
        时在稳定币交易对上调平；不填表示不调平。
      </p>
      <div v-for="(row, index) in model.stablecoin_margin" :key="index" class="asset-row">
        <a-input v-model="row.asset" placeholder="资产，例如 USDT、USDC" />
        <a-input v-model="row.value" placeholder="目标比例，例如 0.6" />
        <a-button type="text" status="danger" @click="model.stablecoin_margin.splice(index, 1)">删除</a-button>
      </div>
      <a-button type="text" @click="model.stablecoin_margin.push({ asset: '', value: '' })">增加稳定币</a-button>
    </section>

    <section class="settings-group">
      <h4>保证金币</h4>
      <p class="settings-hint">统一账户里作为保证金的币种，按这里配置的固定 {{ unit }} 价值计入权益，不参与策略持仓。</p>
      <div v-for="(row, index) in model.margin_coins" :key="index" class="asset-row">
        <a-input v-model="row.asset" placeholder="资产，例如 ETH" />
        <a-input v-model="row.value" placeholder="配置的价值" :suffix="unit" />
        <a-button type="text" status="danger" @click="model.margin_coins.splice(index, 1)">删除</a-button>
      </div>
      <a-button type="text" @click="model.margin_coins.push({ asset: '', value: '' })">增加保证金币</a-button>
    </section>

    <section class="settings-group">
      <h4>套利底仓</h4>
      <a-form-item label="底仓资产" extra="这些币的现货和对应永续都不参与策略持仓，按净值计入权益。多个用逗号或空格分隔">
        <a-input v-model="model.arbitrage_base_assets" placeholder="例如 BTC, ETH" />
      </a-form-item>
    </section>

    <section v-if="marginMonitor" class="settings-group">
      <h4>保证金率监控</h4>
      <p class="settings-hint">
        只对生产环境的币安实盘组合生效。触发后会暂停这个组合的自动执行，并把每个持仓按“触发后减仓比例”同比例减掉；普通账户的钱包划转还没有接入，只会提示你手动把现货钱包里的
        {{ unit }} 划回合约钱包。触发后组合保持暂停，需要你确认账户安全后手动恢复。
      </p>
      <p v-if="mixedNote" class="settings-hint">{{ mixedNote }}</p>
      <a-form-item label="关闭保证金率监控" extra="开启后不再监控，触发条件满足也不会暂停组合或减仓">
        <a-switch v-model="model.margin_monitor_disabled" />
      </a-form-item>
      <a-form-item
        label="普通账户触发阈值"
        extra="合约钱包权益 ÷ 合约持仓名义，不高于这个值时触发。留空用默认 0.5，范围大于 0 且不超过 10"
      >
        <a-input v-model="model.margin_ratio_threshold" placeholder="默认 0.5" />
      </a-form-item>
      <a-form-item
        label="统一账户触发阈值（uniMMR）"
        extra="统一账户的 uniMMR 低于这个值时触发。留空用默认 1.5，范围大于 1 且不超过 100"
      >
        <a-input v-model="model.unified_mmr_threshold" placeholder="默认 1.5" />
      </a-form-item>
      <a-form-item label="触发后减仓比例" extra="每个持仓各减掉的比例，0.1 表示 10%。留空用默认 0.1，范围大于 0 且小于 1">
        <a-input v-model="model.margin_reduce_ratio" placeholder="默认 0.1" />
      </a-form-item>
    </section>

    <a-alert v-if="paper" type="info" class="settings-error">
      模拟盘组合没有资金维护任务，BNB、稳定币保证金和保证金率监控都不显示。
    </a-alert>
    <a-alert v-else-if="!liveOnly" type="info" class="settings-error">
      BNB 抵扣、BNB 保留、小额兑换和稳定币保证金只对币安实盘组合生效，这个组合不显示。
    </a-alert>
    <a-alert v-if="marginNote" type="info" class="settings-error">{{ marginNote }}</a-alert>
    <a-alert v-if="formError" type="error" class="settings-error">{{ formError }}</a-alert>
    <a-alert v-if="props.error" type="error" class="settings-error">
      已保存的资金设置读不出来（{{ props.error }}），先修好再保存，否则会覆盖原来的设置。
    </a-alert>
    <a-space>
      <a-button type="primary" :loading="saving" :disabled="!!props.error" @click="submit">保存资金设置</a-button>
      <a-button @click="restore">还原</a-button>
      <a-popconfirm
        v-if="props.error"
        content="按默认值重置会丢掉原来保存的全部资金设置（包括稳定币保证金、保证金币、底仓、保证金率监控），确认重置？"
        @ok="resetToDefault"
      >
        <a-button status="danger">按默认值重置</a-button>
      </a-popconfirm>
    </a-space>
  </a-form>
</template>

<script setup lang="ts">
import { computed, reactive, ref, watch } from "vue";
import type { FundSettings } from "@/api/trade/settings";
import {
  buildFundSettingsRequest,
  defaultFundSettings,
  fundSettingsToModel,
  marginMonitorHiddenNote,
  marginMonitorMixedNote,
  showsLiveOnlyFields,
  showsMarginMonitor,
  validateFundSettingsModel,
  withStoredMarginMonitor
} from "./fund-settings-form";

/**
 * 资金设置表单，可以独立挂载：当前设置由父组件传入，点保存只发出 save 事件（带校验过的设置），
 * 由父组件调用 updateLogicalAccountFundSettings 并重新取组合账户；点还原发出 refresh，让父组件重新取数。
 * paper、nonBinance 为真时不显示仅币安实盘生效的项；保证金率监控只对生产环境的币安实盘生效，
 * paper、testnet 为真或 hasBinanceLive 为 false 时不显示（不校验，提交库里已存的值，不重置）。
 */
const props = defineProps<{
  settings?: FundSettings;
  saving?: boolean;
  /** 存的设置读不出来时的说明（LogicalAccount.fund_settings_error）：显示提示并禁止保存，不按默认值显示 */
  error?: string;
  /** 模拟盘组合 */
  paper?: boolean;
  /** 组合里有非币安成员。父组件要把停用成员也算上（后端按全部成员判断，包括停用的） */
  nonBinance?: boolean;
  /** 测试网组合（不监控保证金率）。父组件按交易账户的环境传入 */
  testnet?: boolean;
  /** 组合里有币安生产实盘成员（后端只要有就监控保证金率）。父组件要把停用成员也算上；不传按有处理 */
  hasBinanceLive?: boolean;
  /** 组合的结算币，默认 USDT：稳定币保证金必须含它，保证金币和底仓不能写它 */
  settlementAsset?: string;
}>();
const emit = defineEmits<{ save: [settings: FundSettings]; refresh: [] }>();

const scope = computed(() => ({
  paper: props.paper,
  nonBinance: props.nonBinance,
  testnet: props.testnet,
  hasBinanceLive: props.hasBinanceLive,
  settlementAsset: props.settlementAsset
}));
const liveOnly = computed(() => showsLiveOnlyFields(scope.value));
const marginMonitor = computed(() => showsMarginMonitor(scope.value));
const marginNote = computed(() => marginMonitorHiddenNote(scope.value));
const mixedNote = computed(() => marginMonitorMixedNote(scope.value));
const unit = computed(() => (props.settlementAsset || "USDT").toUpperCase());

const model = reactive(fundSettingsToModel(props.settings));
const formError = ref("");

function reset() {
  Object.assign(model, fundSettingsToModel(props.settings));
  formError.value = "";
}

// 详情页每 5 秒整体换一次组合对象：只有保存的设置内容真的变了才重置表单，否则正在改的内容会被悄悄还原。
watch(() => JSON.stringify(props.settings ?? null), reset);

// 已保存的设置读不出来（或个别数值读不出来）时的出路：用户确认后按默认值整个覆盖（由父组件调保存接口）。
function resetToDefault() {
  emit("save", buildFundSettingsRequest(fundSettingsToModel(defaultFundSettings()), scope.value));
}

function restore() {
  reset();
  emit("refresh");
}

function submit() {
  formError.value = validateFundSettingsModel(model, scope.value);
  if (formError.value) return;
  // 监控区块不显示时，这四项沿用库里已存的值，不能被默认值重置
  emit("save", buildFundSettingsRequest(withStoredMarginMonitor(model, props.settings, scope.value), scope.value));
}
</script>

<style scoped lang="scss">
.settings-group {
  margin-bottom: 20px;

  h4 {
    margin: 0 0 12px;
    font-weight: 600;
  }
}

.settings-hint {
  margin: 0 0 8px;
  color: var(--color-text-3);
  font-size: 12px;
}

.asset-row {
  display: flex;
  gap: 8px;
  margin-bottom: 6px;
}

.settings-error {
  margin-bottom: 12px;
}
</style>
