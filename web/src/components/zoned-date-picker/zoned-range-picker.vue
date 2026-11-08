<template>
  <span class="zoned-picker" :class="attrs.class" :style="attrs.style as StyleValue">
    <a-range-picker
      v-bind="pickerAttrs"
      class="zoned-picker__input"
      :model-value="wall"
      :show-time="showTime"
      :format="format"
      :value-format="format"
      :time-picker-props="showTime ? { defaultValue: ['00:00:00', '00:00:00'] } : undefined"
      :shortcuts="shortcuts"
      shortcuts-position="bottom"
      :disabled="disabled"
      @update:model-value="onChange"
    />
    <span class="zone-suffix">{{ suffix }}</span>
  </span>
</template>

<script setup lang="ts">
import { computed, useAttrs, type StyleValue } from "vue";
import { msToWallClock, wallClockToMs, zoneSuffix } from "@/utils/time-zone";

/**
 * 按指定时区选时间范围：对外的 v-model 是 [起, 止) 两个毫秒时刻，起始含、结束不含。
 *
 * - 只选日期时（showTime 为 false），结束日期整天都算在内：止是结束日期的下一天零点（按这个时区）。
 * - 显示和提交都到分钟，时间面板默认 00:00，不会带上浏览器本地“现在”的秒数。
 * - 快捷范围用 quickRanges 按毫秒给出，组件按这个时区换成墙钟交给 Arco；不要透传 Arco 的 shortcuts，
 *   它按浏览器本地时间解析，会差一个时区偏移。其余透传的属性、事件值同样是浏览器本地的墙钟。
 */
defineOptions({ inheritAttrs: false });
const props = withDefaults(
  defineProps<{
    modelValue?: [number, number] | null;
    zone?: string | null;
    showTime?: boolean;
    disabled?: boolean;
    /** 快捷范围：range 返回 [起, 止) 两个毫秒时刻 */
    quickRanges?: { label: string; range: () => [number, number] }[];
  }>(),
  { modelValue: null, zone: "UTC", showTime: true, disabled: false, quickRanges: () => [] }
);
const emit = defineEmits<{ "update:modelValue": [value: [number, number] | null] }>();

const DAY = 86_400_000;
const attrs = useAttrs();
const pickerAttrs = computed(() =>
  Object.fromEntries(Object.entries(attrs).filter(([key]) => key !== "class" && key !== "style"))
);
const format = computed(() => (props.showTime ? "YYYY-MM-DD HH:mm" : "YYYY-MM-DD"));

/** 日期字符串的下一天（纯日历运算，和时区无关）。 */
function nextDate(date: string): string {
  const [y, m, d] = date.split("-").map(Number);
  return new Date(Date.UTC(y, m - 1, d) + DAY).toISOString().slice(0, 10);
}

/** 毫秒范围换成交给 Arco 的墙钟：只选日期时止是不含的下一天零点，显示时退回到前一天。 */
function toWall(range: [number, number]): [string, string] {
  if (props.showTime) return [msToWallClock(range[0], props.zone, "minute"), msToWallClock(range[1], props.zone, "minute")];
  return [msToWallClock(range[0], props.zone, "date"), msToWallClock(range[1] - 1, props.zone, "date")];
}

const wall = computed(() => (props.modelValue ? toWall(props.modelValue) : undefined));
const suffix = computed(() => zoneSuffix(props.modelValue?.[0] || Date.now(), props.zone));
const shortcuts = computed(() =>
  props.quickRanges.map(item => ({ label: item.label, value: () => toWall(item.range()), format: format.value }))
);

function onChange(value: unknown) {
  if (!Array.isArray(value) || !value[0] || !value[1]) {
    emit("update:modelValue", null);
    return;
  }
  const [start, end] = value as [string, string];
  const from = wallClockToMs(start, props.zone);
  const to = wallClockToMs(props.showTime ? end : nextDate(end), props.zone);
  emit("update:modelValue", from === null || to === null ? null : [from, to]);
}
</script>

<style scoped>
.zoned-picker {
  display: flex;
  align-items: center;
  gap: var(--moox-space-2);
  max-width: 100%;
}

.zoned-picker__input {
  flex: 1;
  min-width: 0;
}

.zone-suffix {
  color: var(--color-text-3);
  white-space: nowrap;
}
</style>
