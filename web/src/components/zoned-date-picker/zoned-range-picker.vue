<template>
  <span class="zoned-picker">
    <a-range-picker
      v-bind="$attrs"
      :model-value="wall"
      :show-time="showTime"
      :format="showTime ? 'YYYY-MM-DD HH:mm' : 'YYYY-MM-DD'"
      :value-format="showTime ? 'YYYY-MM-DD HH:mm:ss' : 'YYYY-MM-DD'"
      :disabled="disabled"
      @update:model-value="onChange"
    />
    <span class="zone-suffix">{{ suffix }}</span>
  </span>
</template>

<script setup lang="ts">
import { computed } from "vue";
import { msToWallClock, wallClockToMs, zoneSuffix } from "@/utils/time-zone";

/**
 * 按指定时区选时间范围：对外的 v-model 是 [起, 止) 两个毫秒时刻，起始含、结束不含。
 * 只选日期时（showTime 为 false），结束日期整天都算在内：止是结束日期的下一天零点（按这个时区）。
 */
defineOptions({ inheritAttrs: false });
const props = withDefaults(
  defineProps<{
    modelValue?: [number, number] | null;
    zone?: string | null;
    showTime?: boolean;
    disabled?: boolean;
  }>(),
  { modelValue: null, zone: "UTC", showTime: true, disabled: false }
);
const emit = defineEmits<{ "update:modelValue": [value: [number, number] | null] }>();

const DAY = 86_400_000;

/** 日期字符串的下一天（纯日历运算，和时区无关）。 */
function nextDate(date: string): string {
  const [y, m, d] = date.split("-").map(Number);
  return new Date(Date.UTC(y, m - 1, d) + DAY).toISOString().slice(0, 10);
}

const wall = computed(() => {
  const range = props.modelValue;
  if (!range) return undefined;
  if (props.showTime) return [msToWallClock(range[0], props.zone), msToWallClock(range[1], props.zone)];
  // 止是不含的下一天零点：显示时退回到前一天
  return [msToWallClock(range[0], props.zone, "date"), msToWallClock(range[1] - 1, props.zone, "date")];
});
const suffix = computed(() => zoneSuffix(props.modelValue?.[0] || Date.now(), props.zone));

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

defineExpose({ onChange });
</script>

<style scoped>
.zoned-picker {
  display: inline-flex;
  align-items: center;
  gap: var(--moox-space-2);
  max-width: 100%;
}

.zone-suffix {
  color: var(--color-text-3);
  white-space: nowrap;
}
</style>
