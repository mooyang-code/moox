<template>
  <span class="zoned-picker">
    <a-date-picker
      v-bind="$attrs"
      :model-value="wall"
      :show-time="showTime"
      :format="showTime ? 'YYYY-MM-DD HH:mm' : 'YYYY-MM-DD'"
      :value-format="showTime ? 'YYYY-MM-DD HH:mm:ss' : 'YYYY-MM-DD'"
      :show-now-btn="false"
      :placeholder="placeholder"
      :disabled="disabled"
      @update:model-value="onChange"
    >
      <template v-if="showTime" #extra><a-link @click="pickNow">此刻</a-link></template>
    </a-date-picker>
    <span class="zone-suffix">{{ suffix }}</span>
  </span>
</template>

<script setup lang="ts">
import { computed } from "vue";
import { msToWallClock, wallClockToMs, zoneSuffix } from "@/utils/time-zone";

/**
 * 按指定时区选时间的日期选择器：选择器里显示和选到的是这个时区的墙钟，对外的 v-model 是毫秒时刻。
 * Arco 的选择器只认浏览器本地时区，所以内部用墙钟字符串，自带的“此刻”换成按时刻取值的版本。
 * 面板里“今天”的高亮仍按浏览器本地，这是 Arco 的限制。
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

const wall = computed(() =>
  props.modelValue ? msToWallClock(props.modelValue, props.zone, props.showTime ? "second" : "date") : undefined
);
const suffix = computed(() => zoneSuffix(props.modelValue || Date.now(), props.zone));

function onChange(value: unknown) {
  emit("update:modelValue", typeof value === "string" && value ? wallClockToMs(value, props.zone) : null);
}

function pickNow() {
  emit("update:modelValue", Math.floor(Date.now() / 1000) * 1000);
}

defineExpose({ onChange, pickNow });
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
