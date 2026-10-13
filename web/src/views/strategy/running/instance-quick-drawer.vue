<template>
  <a-drawer
    :visible="Boolean(instanceId)"
    :width="'min(560px, 100vw)'"
    :footer="false"
    unmount-on-close
    title="实例快速详情"
    @cancel="emit('close')"
    @close="emit('close')"
  >
    <a-alert v-if="error" type="error" show-icon class="drawer-alert"
      >{{ error }}<template #action><a-button size="small" @click="load">重试</a-button></template></a-alert
    >
    <a-spin v-else :loading="loading" class="drawer-body">
      <template v-if="instance">
        <a-descriptions :column="1" bordered size="small">
          <a-descriptions-item label="实例 ID"
            ><span class="mono">{{ instance.instance_id }}</span></a-descriptions-item
          >
          <a-descriptions-item label="策略定义"
            >{{ strategyName }} · <span class="mono">{{ instance.strategy_id }}</span></a-descriptions-item
          >
          <a-descriptions-item label="状态"
            ><StatusBadge :enabled="instance.enabled" :health="instance.health"
          /></a-descriptions-item>
          <a-descriptions-item label="View"
            ><span class="mono">{{ instance.view_id }}</span></a-descriptions-item
          >
          <a-descriptions-item label="模式"
            >{{ instance.logical_account_id ? "交易模式" : "仅计算" }}
            <span v-if="instance.logical_account_id" class="mono">· {{ instance.logical_account_id }}</span></a-descriptions-item
          >
          <a-descriptions-item label="当前会话"
            ><span class="mono">{{ instance.session_id || "无当前会话" }}</span></a-descriptions-item
          >
          <a-descriptions-item label="更新时间">{{ formatStrategyTime(instance.updated_at) }}</a-descriptions-item>
          <a-descriptions-item label="读取时间">{{ readAt }}</a-descriptions-item>
        </a-descriptions>
        <a-space class="drawer-actions" wrap>
          <a-button type="primary" @click="emit('openDetail', instance.instance_id)">打开完整详情</a-button>
          <a-button @click="emit('replay', instance)">按实例会话回放</a-button>
          <a-button :loading="loading" @click="load">刷新</a-button>
        </a-space>
        <div class="muted">启停与目标、历史结果在完整详情中查看和操作。</div>
      </template>
    </a-spin>
  </a-drawer>
</template>

<script setup lang="ts">
import { computed, ref, watch } from "vue";
import { getInstance } from "@/api/strategy";
import type { Strategy, StrategyInstance } from "@/api/strategy-types";
import { RequestGate } from "@/utils/request-gate";
import StatusBadge from "@/views/strategy/components/strategy-status-badge.vue";
import { formatStrategyTime } from "@/views/strategy/model";

// 快速详情只读取实例本身，不加载定义、目标与历史结果；换对象先清空旧内容，迟到响应由 gate 丢弃。
const props = defineProps<{ instanceId: string; strategies: Strategy[] }>();
const emit = defineEmits<{ close: []; openDetail: [string]; replay: [StrategyInstance] }>();

const instance = ref<StrategyInstance | null>(null);
const loading = ref(false);
const error = ref("");
const readAt = ref("-");
const gate = new RequestGate();
const strategyName = computed(
  () => props.strategies.find(item => item.strategy_id === instance.value?.strategy_id)?.name || "定义已删除或未加载"
);

async function load() {
  const token = gate.next();
  const keep = instance.value?.instance_id === props.instanceId ? instance.value : null;
  instance.value = keep;
  error.value = "";
  if (!props.instanceId) {
    loading.value = false;
    return;
  }
  loading.value = true;
  try {
    const loaded = await getInstance(props.instanceId);
    if (!gate.isCurrent(token)) return;
    instance.value = loaded;
    readAt.value = new Date().toLocaleTimeString("zh-CN", { hour12: false });
  } catch (err) {
    if (gate.isCurrent(token)) {
      instance.value = null;
      error.value = err instanceof Error ? `实例读取失败：${err.message}` : "实例读取失败";
    }
  } finally {
    if (gate.isCurrent(token)) loading.value = false;
  }
}

watch(
  () => props.instanceId,
  () => {
    instance.value = null;
    void load();
  },
  { immediate: true }
);
</script>

<style scoped>
.drawer-alert {
  margin-bottom: var(--moox-space-2);
}
.drawer-body {
  display: block;
  width: 100%;
}
.drawer-actions {
  margin: 16px 0 8px;
}
.muted {
  color: var(--color-text-3);
  font-size: 12px;
}
.mono {
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  font-size: 12px;
}
</style>
