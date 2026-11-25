<template>
  <a-space :size="4">
    <a-tooltip v-if="pendingSeed && !uncertain && enabled" content="上游数据集首次接入、历史还在补齐：补齐完成前策略不求值">
      <a-tag size="small" color="gray">{{ PENDING_SEED_LABEL }}</a-tag>
    </a-tooltip>
    <a-tooltip v-else-if="trial && !uncertain && enabled" content="挂了组合账户、还没确认实盘：目标只记为影子目标、不下单">
      <a-tag size="small" color="arcoblue">试运行</a-tag>
    </a-tooltip>
    <a-tag v-else size="small" :color="color">{{ label }}</a-tag>
    <a-tooltip v-if="health === 'degraded'" content="最近一期被跳过，请在实例详情的历史结果中查看具体跳过原因">
      <a-tag size="small" color="red">需要处理</a-tag>
    </a-tooltip>
  </a-space>
</template>

<script setup lang="ts">
import { computed } from "vue";
import { PENDING_SEED_LABEL } from "@/views/strategy/running/running-model";
// trial：启用中、挂了组合账户、还没确认实盘（见 running-model 的 isTrialInstance）。
const props = defineProps<{ enabled: boolean; uncertain?: boolean; health?: string; trial?: boolean; pendingSeed?: boolean }>();
const label = computed(() => (props.uncertain ? "状态未知" : props.enabled ? "已启用" : "已停用"));
const color = computed(() => (props.uncertain ? "orange" : props.enabled ? "green" : "gray"));
</script>
