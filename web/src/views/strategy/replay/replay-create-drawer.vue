<template>
  <a-drawer :visible="visible" :width="'min(560px, 100vw)'" title="发起回放" @cancel="emit('close')">
    <a-alert v-if="errors.strategies" type="error" show-icon class="drawer-alert"
      >{{ errors.strategies
      }}<template #action><a-button size="small" @click="emit('retryStrategies')">重试</a-button></template></a-alert
    >
    <a-alert v-if="errors.tasks" type="error" show-icon class="drawer-alert"
      >{{ errors.tasks }}<template #action><a-button size="small" @click="emit('retryTasks')">重试</a-button></template></a-alert
    >
    <a-alert v-if="errors.start" type="error" show-icon class="drawer-alert">{{ errors.start }}</a-alert>
    <ReplayCreateForm v-model="form" :strategies="strategies" :tasks="tasks" :tasks-loading="tasksLoading" />
    <template #footer>
      <a-space>
        <a-button @click="emit('close')">关闭</a-button>
        <a-button type="primary" :loading="starting" @click="emit('start')">开始回放</a-button>
      </a-space>
    </template>
  </a-drawer>
</template>

<script setup lang="ts">
import type { Strategy } from "@/api/strategy-types";
import type { FactorTask } from "@/api/factor/types";
import ReplayCreateForm from "@/views/strategy/replay/replay-create-form.vue";
import type { ReplayFormState } from "@/views/strategy/replay/replay-form-model";

// 表单草稿由页面持有：关闭再打开会保留已填内容；预填只在页面建立创建上下文时写入。
const form = defineModel<ReplayFormState>({ required: true });
defineProps<{
  visible: boolean;
  strategies: Strategy[];
  tasks: FactorTask[];
  tasksLoading: boolean;
  starting: boolean;
  errors: { strategies: string; tasks: string; start: string };
}>();
const emit = defineEmits<{ close: []; start: []; retryStrategies: []; retryTasks: [] }>();
</script>

<style scoped>
.drawer-alert {
  margin-bottom: var(--moox-space-2);
}
</style>
