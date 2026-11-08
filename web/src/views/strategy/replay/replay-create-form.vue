<template>
  <a-form layout="vertical">
    <a-form-item v-if="form.instance_id" label="回放对象">
      <a-alert type="info" class="instance-alert"
        >按实例 <span class="mono">{{ form.instance_id }}</span>
        当前会话（停用时为最近一次会话）固化的 DSL 版本回放；重新启用实例会按定义的当前版本重新解析。
        <template #action
          ><a-button size="mini" type="text" @click="form.instance_id = ''">改为选择定义</a-button></template
        ></a-alert
      >
    </a-form-item>
    <template v-else>
      <a-form-item label="回放对象">
        <a-radio-group v-model="form.source" type="button" size="small">
          <a-radio value="definition">已保存的定义</a-radio>
          <a-radio value="dsl">DSL 文本（草稿）</a-radio>
        </a-radio-group>
      </a-form-item>
      <a-form-item v-if="form.source === 'definition'" label="策略定义" required>
        <a-select v-model="form.strategy_id" allow-search placeholder="选择定义">
          <a-option v-for="item in strategies" :key="item.strategy_id" :value="item.strategy_id"
            >{{ item.name }}（{{ item.strategy_id }}）</a-option
          >
        </a-select>
      </a-form-item>
      <a-form-item v-else label="DSL 文本" required>
        <a-textarea
          v-model="form.dsl_yaml"
          aria-label="回放用的 DSL 文本"
          :auto-size="{ minRows: 6, maxRows: 16 }"
          placeholder="粘贴未保存的策略 DSL，直接回放"
        />
      </a-form-item>
    </template>
    <a-form-item v-if="form.instance_id" label="因子任务">
      <span class="mono">{{ tasksText(form.tasks) || "读取实例关联的因子任务" }}</span>
      <span class="muted task-note">（按实例关联的任务，不能更改）</span>
    </a-form-item>
    <template v-else>
      <a-form-item v-if="!declared" label="任务所属的市场" required>
        <a-radio-group v-model="form.single_market" type="button" size="small">
          <a-radio v-for="item in MARKET_ORDER" :key="item" :value="item">{{ marketLabel(item) }}</a-radio>
        </a-radio-group>
        <template #extra>策略没有写 market，只读一个因子任务：先选任务的数据所属的市场（要与任务源数据集的市场一致）。</template>
      </a-form-item>
      <a-form-item v-for="market in markets" :key="market" :label="`${marketLabel(market)}因子任务`" required>
        <a-select
          v-model="form.tasks[market]"
          allow-search
          :loading="tasksLoading"
          :placeholder="`选择${marketLabel(market)}因子任务`"
        >
          <a-option v-for="task in taskChoices(market)" :key="task.task_id" :value="task.task_id"
            >{{ task.name || task.task_id }}（{{ task.task_id }} · {{ task.freq || "无周期" }}）</a-option
          >
        </a-select>
        <template v-if="market === markets[0]" #extra
          ><template v-if="markets.length > 1">现货、永续各选一个，两个任务的周期、日历和用到的列要一致。</template
          >统一数据集上的任务不在这里列出，要回放请从实例详情发起。</template
        >
      </a-form-item>
    </template>
    <a-form-item label="区间" required>
      <!-- 表单项内容区是横向 flex：包一层纵向容器，选择器与说明上下排列。 -->
      <div class="range-block">
        <ZonedRangePicker
          v-model="form.range"
          :zone="zone"
          :show-time="!dateOnly"
          :quick-ranges="quickRanges"
          style="width: 100%"
        />
        <div class="muted range-hint">
          {{
            dateOnly ? "日线任务按日期回放：起止两天都含在内。" : "含开始时刻、不含结束时刻。A 股日线任务按日期回放（只选日期）。"
          }}
        </div>
      </div>
    </a-form-item>
    <a-form-item :label="usesSwap ? '现货单边手续费（bps）' : '单边手续费（bps）'">
      <a-input-number v-model="form.spot_fee_bps" :min="0" :max="1000" :step="1" />
    </a-form-item>
    <a-form-item v-if="usesSwap" label="永续单边手续费（bps）">
      <a-input-number v-model="form.swap_fee_bps" :min="0" :max="1000" :step="1" />
      <template #extra>模拟曲线未计资金费与强平；永续按收盘价逐期结算盈亏。</template>
    </a-form-item>
  </a-form>
</template>

<script setup lang="ts">
import { computed, watch } from "vue";
import type { Strategy } from "@/api/strategy-types";
import type { FactorTask } from "@/api/factor/types";
import ZonedRangePicker from "@/components/zoned-date-picker/zoned-range-picker.vue";
import { useDisplayZone } from "@/hooks/useDisplayZone";
import { recentRange } from "@/views/strategy/replay/model";
import {
  type ReplayFormState,
  replayDateOnly,
  replayDslText,
  replayMarkets,
  wholeDays
} from "@/views/strategy/replay/replay-form-model";
import { MARKET_ORDER, marketLabel, marketRequirement, tasksText } from "@/views/strategy/market-mode";

// 表单状态由页面持有（提交与预填都在页面里），这里直接编辑同一个响应式对象。
const form = defineModel<ReplayFormState>({ required: true });
const props = defineProps<{ strategies: Strategy[]; tasks: FactorTask[]; tasksLoading: boolean }>();
// 区间按当前空间登记的时区选择
const zone = useDisplayZone();

// 策略的 market 决定要读哪些市场的任务；没写 market 时只读一个，市场由用户选。
const declared = computed(() => marketRequirement(replayDslText(form.value, props.strategies)).declared);
const markets = computed(() => replayMarkets(form.value, props.strategies));
// 统一数据集上的任务同时覆盖现货与永续，永续费率也要填。
const usesSwap = computed(() => markets.value.includes("swap") || markets.value.includes("multi"));

/** 一个市场可选的任务：已被别的市场选走的不再出现。 */
function taskChoices(market: string) {
  const taken = new Set(markets.value.filter(item => item !== market).map(item => form.value.tasks[item]));
  return props.tasks.filter(task => !taken.has(task.task_id));
}

// 选的任务都是日线时只选日期（按空间时区的整天）
const dateOnly = computed(() => replayDateOnly(form.value, props.strategies, props.tasks));
// 换成日线任务时，把已有的区间（默认的整点区间，或先按分钟选好的）吸附成显示的那几个整天
watch(
  [dateOnly, () => form.value.range, zone],
  () => {
    const range = form.value.range;
    if (!dateOnly.value || !range) return;
    const snapped = wholeDays(range, zone.value);
    if (snapped[0] !== range[0] || snapped[1] !== range[1]) form.value.range = snapped;
  },
  { immediate: true }
);
const quickRanges = [1, 7, 30].map(days => ({ label: `近 ${days} 天`, range: () => recentRange(days) }));
</script>

<style scoped>
.instance-alert {
  width: 100%;
}
.range-block {
  display: grid;
  gap: 6px;
  width: 100%;
}
.range-hint {
  font-size: 12px;
}
.muted {
  color: var(--color-text-3);
  font-size: 12px;
}
.task-note {
  margin-left: 6px;
}
.mono {
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  font-size: 12px;
}
</style>
