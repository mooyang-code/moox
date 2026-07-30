<template>
  <div class="moox-page">
    <div class="moox-inner">
      <div class="page-head">
        <div>
          <h2>策略</h2>
          <span>不可变策略代码与运行清单。</span>
        </div>
        <a-space>
          <a-tag :color="store.engineReady ? 'green' : 'orange'">
            Worker {{ store.engine.ready_workers }}/{{ store.engine.workers }}
          </a-tag>
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
          <a-table-column title="源码 Hash" :ellipsis="true" :tooltip="true">
            <template #cell="{ record }">{{ record.source_hash || "-" }}</template>
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
          <a-grid :cols="2" :col-gap="16">
            <a-form-item label="策略 ID" required><a-input v-model="form.strategy_id" /></a-form-item>
            <a-form-item label="名称" required><a-input v-model="form.name" /></a-form-item>
          </a-grid>
          <a-form-item label="Manifest YAML" required>
            <a-textarea v-model="form.manifest_yaml" class="code-input" :auto-size="{ minRows: 5, maxRows: 10 }" />
          </a-form-item>
          <a-form-item label="Python 源码" required>
            <a-textarea v-model="form.source_code" class="code-input" :auto-size="{ minRows: 12, maxRows: 24 }" />
          </a-form-item>
        </a-form>
      </a-modal>

      <a-drawer v-model:visible="artifactVisible" :width="720" title="策略定义">
        <a-descriptions v-if="selected" :column="1" bordered>
          <a-descriptions-item label="策略 ID">{{ selected.strategy_id }}</a-descriptions-item>
          <a-descriptions-item label="名称">{{ selected.name }}</a-descriptions-item>
          <a-descriptions-item label="源码 Hash">{{ selected.source_hash }}</a-descriptions-item>
        </a-descriptions>
        <h3>Manifest</h3>
        <pre>{{ selected?.manifest_yaml }}</pre>
        <h3>Python</h3>
        <pre>{{ selected?.source_code }}</pre>
      </a-drawer>
    </div>
  </div>
</template>

<script setup lang="ts">
import { onMounted, reactive, ref } from "vue";
import { Message } from "@arco-design/web-vue";
import { createStrategy } from "@/api/strategy";
import type { Strategy } from "@/api/strategy-types";
import { useStrategyStore } from "@/store/modules/strategy";

defineOptions({ name: "StrategyOverview" });
const store = useStrategyStore();
const createVisible = ref(false);
const artifactVisible = ref(false);
const selected = ref<Strategy | null>(null);
const pagination = reactive({ current: 1, pageSize: 20, total: 0 });
const form = reactive({
  strategy_id: "",
  name: "",
  manifest_yaml: "entrypoint: strategy\\n",
  source_code: 'def strategy(data, params, context):\\n    return {"action": "hold"}\\n'
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
  if (!form.strategy_id.trim() || !form.name.trim() || !form.manifest_yaml.trim() || !form.source_code.trim()) {
    Message.warning("请填写完整策略定义");
    return false;
  }
  await createStrategy({
    ...form,
    strategy_id: form.strategy_id.trim(),
    name: form.name.trim(),
    source_hash: "",
    created_at: ""
  });
  createVisible.value = false;
  Object.assign(form, { strategy_id: "", name: "", manifest_yaml: "entrypoint: strategy\\n" });
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
