<template>
  <a-tag size="small" :color="color">{{ label }}</a-tag>
</template>

<script setup lang="ts">
import { computed } from 'vue';
const props = defineProps<{ status?: string }>();
const labelMap: Record<string, string> = { running: '运行中', enabled: '已启用', paused: '已暂停', disabled: '已停用', waiting_data: '等待数据', failed: '计算失败', unknown: '执行未知', stale: '数据过期' };
const colorMap: Record<string, string> = { running: 'green', enabled: 'green', paused: 'orange', disabled: 'gray', waiting_data: 'arcoblue', failed: 'red', unknown: 'red', stale: 'orange' };
const label = computed(() => labelMap[props.status || ''] || props.status || '未知');
const color = computed(() => colorMap[props.status || ''] || 'gray');
</script>
