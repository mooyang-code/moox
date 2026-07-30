<template>
  <a-tag size="small" :color="color">{{ label }}</a-tag>
</template>

<script setup lang="ts">
import { computed } from "vue";
const props = defineProps<{ status?: string }>();
const labelMap: Record<string, string> = {
  ENABLED: "已启用",
  DISABLED: "已停用"
};
const colorMap: Record<string, string> = {
  ENABLED: "green",
  DISABLED: "gray"
};
const label = computed(() => labelMap[props.status || ""] || props.status || "未知");
const color = computed(() => colorMap[props.status || ""] || "gray");
</script>
