<template>
  <a-tag size="small" :color="color">{{ label }}</a-tag>
</template>

<script setup lang="ts">
import { computed } from "vue";
const props = defineProps<{ status?: string }>();
const labelMap: Record<string, string> = {
  enabled: "已启用",
  disabled: "已停用",
  ENABLED: "已启用",
  DISABLED: "已停用"
};
const colorMap: Record<string, string> = {
  enabled: "green",
  disabled: "gray",
  ENABLED: "green",
  DISABLED: "gray"
};
const label = computed(() => labelMap[props.status || ""] || props.status || "未知");
const color = computed(() => colorMap[props.status || ""] || "gray");
</script>
