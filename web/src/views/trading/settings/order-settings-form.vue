<template>
  <a-form :model="model" layout="vertical" class="order-settings-form">
    <section class="settings-group">
      <h4>调仓</h4>
      <a-form-item label="调仓阈值模式">
        <a-select v-model="model.rebalance_threshold_mode" :options="thresholdModeOptions" />
      </a-form-item>
      <a-form-item label="调仓阈值比例" extra="0.05 表示 5%；建仓和清仓不受阈值限制">
        <a-input v-model="model.rebalance_threshold_ratio" :disabled="model.rebalance_threshold_mode === 'ALWAYS'" />
      </a-form-item>
      <a-form-item :label="stockOnly ? '最小下单额' : '现货最小下单额'">
        <a-input v-model="model.min_notional_spot" :suffix="unit" />
      </a-form-item>
      <a-form-item v-if="!stockOnly" label="合约最小下单额" extra="合约减仓和清仓低于这个额度也能下">
        <a-input v-model="model.min_notional_swap" :suffix="unit" />
      </a-form-item>
    </section>

    <section class="settings-group">
      <h4>{{ stockOnly ? "单只股票每小时上限" : "单币每小时上限" }}</h4>
      <p class="settings-hint">
        每个标的每个自然小时累计下单金额的上限，超出的部分留到下一个整点；标的写 * 表示默认值，不填表示不限。
      </p>
      <div v-for="market in capMarkets" :key="market.key" class="cap-block">
        <div v-if="market.label" class="cap-title">{{ market.label }}</div>
        <div v-for="(row, index) in model[market.key]" :key="index" class="cap-row">
          <a-input v-model="row.key" :placeholder="market.placeholder" />
          <a-input v-model="row.value" placeholder="金额" :suffix="unit" />
          <a-button type="text" status="danger" @click="model[market.key].splice(index, 1)">删除</a-button>
        </div>
        <a-button type="text" @click="model[market.key].push({ key: '', value: '' })">增加{{ market.label }}上限</a-button>
      </div>
    </section>

    <section class="settings-group">
      <h4>拆单与节奏</h4>
      <a-form-item label="单笔上限" extra="拆单时每笔的名义金额上限">
        <a-input v-model="model.max_child_notional" :suffix="unit" />
      </a-form-item>
      <a-form-item label="拆单随机化" extra="每笔金额在单笔上限的 50% 到 100% 之间随机">
        <a-switch v-model="model.randomize_split" />
      </a-form-item>
      <a-form-item label="批间隔（秒）" extra="0 表示用默认值：实盘 1 秒，模拟盘 0.3 秒">
        <a-input-number v-model="model.batch_interval_seconds" :min="0" :max="3600" :precision="0" />
      </a-form-item>
    </section>

    <section class="settings-group">
      <h4>下单方式</h4>
      <a-form-item label="下单方式">
        <a-select v-model="model.order_mode" :options="orderModeOptions" />
      </a-form-item>
      <template v-if="model.order_mode === 'PSEUDO_MARKET'">
        <a-form-item label="滑点" extra="首次相对参考价偏移，每次重试再加一份，直到上限；0.015 表示 1.5%">
          <a-input v-model="model.slippage_step" />
        </a-form-item>
        <a-form-item label="滑点上限">
          <a-input v-model="model.slippage_max" />
        </a-form-item>
        <a-form-item label="最多尝试次数" extra="用满次数仍没成交完的标的记为未到位，等下一份目标重算">
          <a-input-number v-model="model.max_attempts" :min="1" :max="10" :precision="0" />
        </a-form-item>
      </template>
    </section>

    <section class="settings-group">
      <h4>挂单</h4>
      <a-form-item label="挂单比例" extra="加仓、减仓、调仓的名义金额中按此比例改成挂单；建仓和清仓不挂单；0 表示不挂单">
        <a-input v-model="model.resting_ratio" />
      </a-form-item>
      <a-form-item label="挂单寿命（秒）" extra="0 表示保留到下一份目标到达为止；到期撤单，剩余量重新计入差额">
        <a-input-number v-model="model.resting_ttl_seconds" :min="0" :max="604800" :precision="0" />
      </a-form-item>
    </section>

    <a-alert v-if="formError" type="error" class="settings-error">{{ formError }}</a-alert>
    <a-alert v-if="props.error" type="error" class="settings-error">
      已保存的下单设置读不出来（{{ props.error }}），先修好再保存，否则会覆盖原来的设置。
    </a-alert>
    <a-space>
      <a-button type="primary" :loading="saving" :disabled="!!props.error" @click="submit">保存下单设置</a-button>
      <a-button @click="restore">还原</a-button>
      <a-popconfirm v-if="props.error" content="按默认值重置会丢掉原来保存的全部下单设置，确认重置？" @ok="resetToDefault">
        <a-button status="danger">按默认值重置</a-button>
      </a-popconfirm>
    </a-space>
  </a-form>
</template>

<script setup lang="ts">
import { computed, reactive, ref, watch } from "vue";
import type { OrderSettings } from "@/api/trade/settings";
import {
  buildOrderSettingsRequest,
  defaultOrderSettings,
  orderModeOptions,
  orderSettingsToModel,
  thresholdModeOptions,
  validateOrderSettingsModel
} from "./order-settings-form";

/**
 * 下单设置表单，可以独立挂载：当前设置由父组件传入，点保存只发出 save 事件（带校验过的设置），
 * 由父组件调用 updateLogicalAccountOrderSettings 并重新取组合账户；点还原发出 refresh，让父组件重新取数。
 */
const props = defineProps<{
  settings?: OrderSettings;
  saving?: boolean;
  /** 存的设置读不出来时的说明（LogicalAccount.order_settings_error）：显示提示并禁止保存，不按默认值显示 */
  error?: string;
  /** 组合的结算币，默认 USDT：金额类设置的单位 */
  settlementAsset?: string;
  /** A 股组合（只开通股票）：最小下单额只有一项 */
  stockOnly?: boolean;
}>();
const emit = defineEmits<{ save: [settings: OrderSettings]; refresh: [] }>();

// A 股组合只有一组上限（走 hourly_cap_spot），标的写股票代码
const capMarkets = computed(() =>
  props.stockOnly
    ? ([{ key: "hourly_cap_spot", label: "", placeholder: "600519.XSHG 或 *" }] as const)
    : ([
        { key: "hourly_cap_spot", label: "现货", placeholder: "BTC-USDT-SPOT 或 *" },
        { key: "hourly_cap_swap", label: "合约", placeholder: "BTC-USDT-SWAP 或 *" }
      ] as const)
);

const model = reactive(orderSettingsToModel(props.settings));
const formError = ref("");
const unit = computed(() => (props.settlementAsset || "USDT").toUpperCase());

function reset() {
  Object.assign(model, orderSettingsToModel(props.settings));
  formError.value = "";
}

// 详情页每 5 秒整体换一次组合对象：只有保存的设置内容真的变了才重置表单，否则正在改的内容会被悄悄还原。
watch(() => JSON.stringify(props.settings ?? null), reset);

// 已保存的设置读不出来时的出路：用户确认后按默认值整个覆盖（由父组件调保存接口）。
function resetToDefault() {
  emit("save", buildOrderSettingsRequest(orderSettingsToModel(defaultOrderSettings())));
}

function restore() {
  reset();
  emit("refresh");
}

function submit() {
  formError.value = validateOrderSettingsModel(model, props.stockOnly);
  if (formError.value) return;
  emit("save", buildOrderSettingsRequest(model));
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

.cap-block {
  margin-bottom: 8px;
}

.cap-title {
  margin-bottom: 4px;
  font-size: 13px;
}

.cap-row {
  display: flex;
  gap: 8px;
  margin-bottom: 6px;
}

.settings-error {
  margin-bottom: 12px;
}
</style>
