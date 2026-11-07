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
        <a-input v-model="model.bnb_reserve_amount" suffix="USDT" />
      </a-form-item>
      <a-form-item label="小额资产兑换 BNB" extra="定期把不在目标里、低于 5 USDT 的现货零头兑成 BNB">
        <a-switch v-model="model.dust_convert_enabled" />
      </a-form-item>
      <a-form-item v-if="model.dust_convert_enabled" label="兑换间隔（天）">
        <a-input-number v-model="model.dust_convert_interval_days" :min="1" :max="365" :precision="0" />
      </a-form-item>
    </section>

    <section v-if="liveOnly" class="settings-group">
      <h4>稳定币保证金（币安统一账户）</h4>
      <p class="settings-hint">
        各稳定币的目标比例，必须包含 USDT 且比例之和为 1。每小时检查一次，偏离目标超过 5% 时在稳定币交易对上调平；不填表示不调平。
      </p>
      <div v-for="(row, index) in model.stablecoin_margin" :key="index" class="asset-row">
        <a-input v-model="row.asset" placeholder="资产，例如 USDT" />
        <a-input v-model="row.value" placeholder="目标比例，例如 0.6" />
        <a-button type="text" status="danger" @click="model.stablecoin_margin.splice(index, 1)">删除</a-button>
      </div>
      <a-button type="text" @click="model.stablecoin_margin.push({ asset: '', value: '' })">增加稳定币</a-button>
    </section>

    <section class="settings-group">
      <h4>保证金币</h4>
      <p class="settings-hint">统一账户里作为保证金的币种，按这里配置的固定 USDT 价值计入权益，不参与策略持仓。</p>
      <div v-for="(row, index) in model.margin_coins" :key="index" class="asset-row">
        <a-input v-model="row.asset" placeholder="资产，例如 ETH" />
        <a-input v-model="row.value" placeholder="配置的价值" suffix="USDT" />
        <a-button type="text" status="danger" @click="model.margin_coins.splice(index, 1)">删除</a-button>
      </div>
      <a-button type="text" @click="model.margin_coins.push({ asset: '', value: '' })">增加保证金币</a-button>
    </section>

    <section class="settings-group">
      <h4>套利底仓</h4>
      <a-form-item
        label="底仓资产"
        extra="这些币的现货和对应永续都不参与策略持仓，按净值计入权益；填了以后组合只做合约。多个用逗号或空格分隔"
      >
        <a-input v-model="model.arbitrage_base_assets" placeholder="例如 BTC, ETH" />
      </a-form-item>
    </section>

    <a-alert v-if="!liveOnly" type="info" class="settings-error">
      BNB 抵扣、BNB 保留、小额兑换和稳定币保证金只对币安实盘组合生效，这个组合不显示。
    </a-alert>
    <a-alert v-if="error" type="error" class="settings-error">{{ error }}</a-alert>
    <a-space>
      <a-button type="primary" :loading="saving" @click="submit">保存资金设置</a-button>
      <a-button @click="restore">还原</a-button>
    </a-space>
  </a-form>
</template>

<script setup lang="ts">
import { computed, reactive, ref, watch } from "vue";
import type { FundSettings } from "@/api/trade/settings";
import {
  buildFundSettingsRequest,
  fundSettingsToModel,
  showsLiveOnlyFields,
  validateFundSettingsModel
} from "./fund-settings-form";

/**
 * 资金设置表单，可以独立挂载：当前设置由父组件传入，点保存只发出 save 事件（带校验过的设置），
 * 由父组件调用 updateLogicalAccountFundSettings 并重新取组合账户；点还原发出 refresh，让父组件重新取数。
 * paper、nonBinance 为真时不显示仅币安实盘生效的项。
 */
const props = defineProps<{ settings?: FundSettings; saving?: boolean; paper?: boolean; nonBinance?: boolean }>();
const emit = defineEmits<{ save: [settings: FundSettings]; refresh: [] }>();

const scope = computed(() => ({ paper: props.paper, nonBinance: props.nonBinance }));
const liveOnly = computed(() => showsLiveOnlyFields(scope.value));

const model = reactive(fundSettingsToModel(props.settings));
const error = ref("");

function reset() {
  Object.assign(model, fundSettingsToModel(props.settings));
  error.value = "";
}

watch(() => props.settings, reset);

function restore() {
  reset();
  emit("refresh");
}

function submit() {
  error.value = validateFundSettingsModel(model, scope.value);
  if (error.value) return;
  emit("save", buildFundSettingsRequest(model, scope.value));
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
