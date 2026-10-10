<template>
  <a-space :size="4">
    <a-tag size="small" :color="color">{{ label }}</a-tag>
    <a-tooltip v-if="health === 'degraded'" content="最近一期因配置或数据问题跳过，请检查 View、因子与数据后重新启用实例">
      <a-tag size="small" color="red">需要处理</a-tag>
    </a-tooltip>
    <a-tooltip
      v-else-if="health === 'session_unverified'"
      content="启动或对账时无法核实 Trade 的账户会话，目标是否被接受未知；对账成功后自动恢复"
    >
      <a-tag size="small" color="orange">Trade 会话待核实</a-tag>
    </a-tooltip>
  </a-space>
</template>

<script setup lang="ts">
import { computed } from "vue";
const props = defineProps<{ enabled: boolean; uncertain?: boolean; health?: string }>();
const label = computed(() => (props.uncertain ? "状态未知" : props.enabled ? "已启用" : "已停用"));
const color = computed(() => (props.uncertain ? "orange" : props.enabled ? "green" : "gray"));
</script>
