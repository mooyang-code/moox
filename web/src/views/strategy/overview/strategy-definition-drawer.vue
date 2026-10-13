<template>
  <a-drawer
    :visible="Boolean(definitionId)"
    :width="'min(860px, 100vw)'"
    :footer="false"
    unmount-on-close
    title="策略定义"
    @cancel="emit('close')"
    @close="emit('close')"
  >
    <a-alert v-if="error" type="error" show-icon class="drawer-alert"
      >{{ error }}<template #action><a-button size="small" @click="load">重试</a-button></template></a-alert
    >
    <a-spin v-else :loading="loading" class="drawer-body">
      <template v-if="definition">
        <a-descriptions :column="2" bordered size="small">
          <a-descriptions-item label="名称">{{ definition.name || "未命名策略" }}</a-descriptions-item>
          <a-descriptions-item label="策略 ID"
            ><span class="mono">{{ definition.strategy_id }}</span></a-descriptions-item
          >
          <a-descriptions-item label="版本"
            ><span class="mono" :title="definition.dsl_hash">{{ shortHash(definition.dsl_hash) }}</span></a-descriptions-item
          >
          <a-descriptions-item label="更新时间">{{
            formatStrategyTime(definition.updated_at || definition.created_at)
          }}</a-descriptions-item>
          <a-descriptions-item label="摘要" :span="2">{{ summarizeDSL(definition.dsl_yaml) }}</a-descriptions-item>
        </a-descriptions>
        <a-space class="drawer-actions" wrap>
          <a-button type="primary" @click="emit('edit', definition.strategy_id)">编辑定义</a-button>
          <a-button @click="emit('instances', definition.strategy_id)">查看当前空间实例</a-button>
          <a-button @click="emit('createInstance', definition.strategy_id)">创建实例</a-button>
          <a-button @click="emit('replay', definition.strategy_id)">发起回放</a-button>
        </a-space>
        <div class="section-title">DSL</div>
        <CodeBlock :code="definition.dsl_yaml" language="yaml" />
      </template>
    </a-spin>
  </a-drawer>
</template>

<script setup lang="ts">
import { ref, watch } from "vue";
import { getStrategy } from "@/api/strategy";
import type { Strategy } from "@/api/strategy-types";
import CodeBlock from "@/components/code-block/index.vue";
import { RequestGate } from "@/utils/request-gate";
import { summarizeDSL } from "@/views/strategy/dsl";
import { formatStrategyTime, shortHash } from "@/views/strategy/model";

// definitionId 为空表示抽屉关闭；每次换对象先清空旧内容，再按 ID 读取，迟到的响应由 gate 丢弃。
const props = defineProps<{ definitionId: string }>();
const emit = defineEmits<{
  close: [];
  edit: [string];
  instances: [string];
  createInstance: [string];
  replay: [string];
}>();

const definition = ref<Strategy | null>(null);
const loading = ref(false);
const error = ref("");
const gate = new RequestGate();

async function load() {
  const token = gate.next();
  definition.value = null;
  error.value = "";
  if (!props.definitionId) {
    loading.value = false;
    return;
  }
  loading.value = true;
  try {
    const loaded = await getStrategy(props.definitionId);
    if (gate.isCurrent(token)) definition.value = loaded;
  } catch (err) {
    if (gate.isCurrent(token)) error.value = err instanceof Error ? `策略定义读取失败：${err.message}` : "策略定义读取失败";
  } finally {
    if (gate.isCurrent(token)) loading.value = false;
  }
}

watch(() => props.definitionId, load, { immediate: true });
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
  margin: 16px 0;
}
.section-title {
  margin-bottom: 8px;
  font-weight: 600;
}
.mono {
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  font-size: 12px;
}
</style>
