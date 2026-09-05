<template>
  <div class="moox-page">
    <div class="moox-inner">
      <div class="page-head">
        <div>
          <h2>策略</h2>
          <span>不可变策略代码与运行清单。</span>
        </div>
        <a-space>
          <a-tag color="blue">声明式权重策略</a-tag>
          <a-button :loading="store.loading" @click="refresh"
            ><template #icon><icon-refresh /></template>刷新</a-button
          >
          <a-button type="primary" status="success" @click="createVisible = true"
            ><template #icon><icon-plus /></template>新增策略</a-button
          >
        </a-space>
      </div>

      <a-alert v-if="store.error" type="error" show-icon class="top-alert">{{ store.error }}</a-alert>
      <a-table
        row-key="strategy_id"
        :data="store.strategies"
        :loading="store.loading"
        :pagination="pagination"
        @page-change="changePage"
      >
        <template #columns>
          <a-table-column title="策略">
            <template #cell="{ record }">
              <strong>{{ record.name }}</strong>
              <div class="muted">{{ record.strategy_id }}</div>
            </template>
          </a-table-column>
          <a-table-column title="创建时间">
            <template #cell="{ record }">{{ formatTime(record.created_at) }}</template>
          </a-table-column>
          <a-table-column title="操作" :width="120">
            <template #cell="{ record }">
              <a-button size="mini" type="text" @click="showArtifact(record)">查看定义</a-button>
            </template>
          </a-table-column>
        </template>
      </a-table>

      <a-modal v-model:visible="createVisible" title="新增策略" :width="720" @ok="create">
        <a-form :model="form" layout="vertical">
          <a-form-item label="策略 ID" required><a-input v-model="form.strategy_id" /></a-form-item>
          <a-form-item label="DSL YAML（名称由 name 派生）" required>
            <a-textarea v-model="form.dsl_yaml" class="code-input" :auto-size="{ minRows: 8, maxRows: 16 }" />
          </a-form-item>
        </a-form>
      </a-modal>

      <a-drawer v-model:visible="artifactVisible" :width="720" title="策略定义">
        <a-descriptions v-if="selected" :column="1" bordered>
          <a-descriptions-item label="策略 ID">{{ selected.strategy_id }}</a-descriptions-item>
          <a-descriptions-item label="名称">{{ selected.name }}</a-descriptions-item>
          </a-descriptions>
        <h3>DSL YAML</h3>
        <pre>{{ selected?.dsl_yaml }}</pre>
      </a-drawer>
    </div>
  </div>
</template>

<script setup lang="ts">
import { onMounted, reactive, ref } from "vue";
import { Message } from "@arco-design/web-vue";
import { createStrategy } from "@/api/strategy";
import type { Strategy } from "@/api/strategy-types";
import { useSpaceStore } from "@/store/modules/space";
import { useStrategyStore } from "@/store/modules/strategy";

defineOptions({ name: "StrategyOverview" });
const store = useStrategyStore();
const spaceStore = useSpaceStore();
const createVisible = ref(false);
const artifactVisible = ref(false);
const selected = ref<Strategy | null>(null);
const pagination = reactive({ current: 1, pageSize: 20, total: 0 });
const defaultDSL = `name: momentum_demo
triggers:
  schedule: {cron: "5 * * * *", timezone: UTC}
data: {bar: 1h, calendar: crypto_24x7}
rules:
  momentum:
    pool: {udf: spot_symbols}
    score: "return_20"
    select: {top: 10}
    weight: 0.60
    filter_after: "return_20 > 0"
`;
const form = reactive({
  strategy_id: "",
  dsl_yaml: defaultDSL
});

async function refresh() {
  await store.loadStrategies({ page: pagination.current, page_size: pagination.pageSize });
  pagination.total = store.totalStrategies;
}

function changePage(page: number) {
  pagination.current = page;
  refresh();
}

function showArtifact(strategy: Strategy) {
  selected.value = strategy;
  artifactVisible.value = true;
}

async function create() {
  if (!form.strategy_id.trim() || !form.dsl_yaml.trim() || !spaceStore.selectedSpaceId) {
    Message.warning("请填写完整策略定义");
    return false;
  }
  await createStrategy({
    ...form,
    dsl_yaml: form.dsl_yaml,
    name: "",
    strategy_id: form.strategy_id.trim(),
    created_at: ""
  });
  createVisible.value = false;
  Object.assign(form, {
    strategy_id: "",
    dsl_yaml: defaultDSL
  });
  await refresh();
  return true;
}

function formatTime(value: string) {
  return value ? new Date(value).toLocaleString() : "-";
}

onMounted(refresh);
</script>

<style scoped>
.page-head {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 16px;
  margin-bottom: var(--moox-space-2);
}
.page-head h2 {
  margin: 0 0 4px;
}
.page-head span,
.muted {
  color: var(--color-text-3);
  font-size: 12px;
}
.top-alert {
  margin-bottom: var(--moox-space-2);
}
.code-input,
pre {
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
}
pre {
  overflow: auto;
  padding: 12px;
  background: var(--color-fill-2);
  white-space: pre-wrap;
}
</style>
