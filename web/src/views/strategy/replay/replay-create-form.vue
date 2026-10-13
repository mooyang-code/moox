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
    <a-form-item label="View" required>
      <a-select v-model="form.view_id" allow-search :loading="viewsLoading" placeholder="选择 View（第一版只支持现货）">
        <a-option v-for="view in views" :key="view.view_id" :value="view.view_id"
          >{{ view.name || view.view_id }}（{{ view.view_id }} · {{ view.freq || "无周期" }}）</a-option
        >
      </a-select>
    </a-form-item>
    <a-form-item label="区间（UTC，左闭右开，格式 YYYY-MM-DD HH:mm）" required>
      <div class="range-inputs">
        <a-input v-model="form.start" aria-label="开始时间（UTC）" placeholder="开始，例如 2026-09-01 00:00" />
        <a-input v-model="form.end" aria-label="结束时间（UTC）" placeholder="结束，例如 2026-10-01 00:00" />
      </div>
      <div class="muted range-hint">A 股日线 View 按上海日期回放：起止各取所在的上海日期，含起始日期、不含结束日期。</div>
      <a-space class="presets" :size="4">
        <a-button size="mini" @click="applyPreset(1)">近 1 天</a-button>
        <a-button size="mini" @click="applyPreset(7)">近 7 天</a-button>
        <a-button size="mini" @click="applyPreset(30)">近 30 天</a-button>
      </a-space>
    </a-form-item>
    <a-form-item label="单边手续费（bps）"><a-input-number v-model="form.fee_bps" :min="0" :max="1000" :step="1" /></a-form-item>
  </a-form>
</template>

<script setup lang="ts">
import type { Strategy } from "@/api/strategy-types";
import type { View } from "@/api/storage/types";
import type { ReplayFormState } from "@/views/strategy/replay/replay-form-model";
import { recentUtcRange } from "@/views/strategy/replay/model";

// 表单状态由页面持有（提交与预填都在页面里），这里直接编辑同一个响应式对象。
const form = defineModel<ReplayFormState>({ required: true });
defineProps<{ strategies: Strategy[]; views: View[]; viewsLoading: boolean }>();

function applyPreset(days: number) {
  [form.value.start, form.value.end] = recentUtcRange(days);
}
</script>

<style scoped>
.instance-alert {
  width: 100%;
}
.range-inputs {
  display: grid;
  gap: 6px;
  width: 100%;
}
.range-hint {
  margin-top: 6px;
  font-size: 12px;
}
.muted {
  color: var(--color-text-3);
  font-size: 12px;
}
.presets {
  margin-top: 6px;
}
.mono {
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  font-size: 12px;
}
</style>
