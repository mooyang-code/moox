<template>
  <span class="zoned-picker" :class="attrs.class" :style="attrs.style as StyleValue">
    <a-date-picker
      v-bind="pickerAttrs"
      class="zoned-picker__input"
      :model-value="wall"
      :show-time="showTime"
      :format="format"
      :value-format="format"
      :time-picker-props="showTime ? { defaultValue: '00:00:00' } : undefined"
      :show-now-btn="false"
      :shortcuts="shortcuts"
      shortcuts-position="bottom"
      :placeholder="placeholder"
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
 * 按指定时区选时间的日期选择器：选择器里显示和选到的是这个时区的墙钟，对外的 v-model 是毫秒时刻。
 *
 * - Arco 的选择器只认浏览器本地时区，所以内部交给它墙钟字符串；显示和提交都到分钟，
 *   时间面板默认 00:00，不会把浏览器本地“现在”的秒数悄悄带进提交值。
 * - 自带的“此刻”按本地取值，换成按这个时区算的快捷项（点了就确认并关闭面板）。
 * - class、style 落在外层容器上；其余属性透传给 Arco。注意透传的 shortcuts、disabled-date、default-value、
 *   picker-value 以及 change、ok、select 事件带的值都是浏览器本地的墙钟，不是毫秒，需要时在这里加按时刻的属性。
 * - 面板里“今天”的高亮仍按浏览器本地，这是 Arco 的限制。
 */
defineOptions({ inheritAttrs: false });
const props = withDefaults(
  defineProps<{
    modelValue?: number | null;
    zone?: string | null;
    showTime?: boolean;
    placeholder?: string;
    disabled?: boolean;
  }>(),
  { modelValue: null, zone: "UTC", showTime: true, placeholder: "", disabled: false }
);
const emit = defineEmits<{ "update:modelValue": [value: number | null] }>();

const attrs = useAttrs();
const pickerAttrs = computed(() =>
  Object.fromEntries(Object.entries(attrs).filter(([key]) => key !== "class" && key !== "style"))
);
const format = computed(() => (props.showTime ? "YYYY-MM-DD HH:mm" : "YYYY-MM-DD"));
const wall = computed(() =>
  props.modelValue ? msToWallClock(props.modelValue, props.zone, props.showTime ? "minute" : "date") : undefined
);
const suffix = computed(() => zoneSuffix(props.modelValue || Date.now(), props.zone));
const shortcuts = computed(() =>
  props.showTime ? [{ label: "此刻", value: () => msToWallClock(Date.now(), props.zone, "minute"), format: format.value }] : []
);

function onChange(value: unknown) {
  emit("update:modelValue", typeof value === "string" && value ? wallClockToMs(value, props.zone) : null);
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
