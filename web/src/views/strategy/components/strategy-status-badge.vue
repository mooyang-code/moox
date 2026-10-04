<template><a-tag size="small" :color="color">{{ label }}</a-tag></template>

<script setup lang="ts">
import { computed } from "vue";
const props = defineProps<{ enabled: boolean; uncertain?: boolean }>();
const label = computed(() => (props.uncertain ? "状态未知" : props.enabled ? "已启用" : "已停用"));
const color = computed(() => (props.uncertain ? "orange" : props.enabled ? "green" : "gray"));
</script>
