<template>
  <span class="time-text" :title="full">{{ text }}</span>
</template>

<script setup lang="ts">
import { computed } from "vue";
import { formatInstant, type TimePrecision } from "@/utils/time-zone";

/** 带时区后缀的时间：显示按 precision，悬停显示到秒的完整时间。 */
const props = withDefaults(
  defineProps<{
    value?: string | number | Date | null;
    zone?: string | null;
    precision?: TimePrecision;
  }>(),
  { value: null, zone: "UTC", precision: "second" }
);

const text = computed(() => formatInstant(props.value, props.zone, props.precision));
const full = computed(() => formatInstant(props.value, props.zone, "second"));
</script>

<style scoped>
.time-text {
  white-space: nowrap;
}
</style>
