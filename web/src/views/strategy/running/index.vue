<template>
  <div class="moox-page">
    <div class="moox-inner">
      <div class="page-head">
        <div>
          <h2>策略实例</h2>
          <span>同一策略可创建多个实例；实例只负责绑定输入和投递目标，不保存交易状态。</span>
        </div>
        <a-space>
          <a-button :loading="store.loading" @click="refresh"
            ><template #icon><icon-refresh /></template>刷新</a-button
          >
          <a-button type="primary" status="success" :disabled="!spaceStore.selectedSpaceId" @click="openCreate"
            ><template #icon><icon-plus /></template>新增实例</a-button
          >
        </a-space>
      </div>
      <a-alert v-if="store.error" type="error" show-icon class="top-alert">{{ store.error }}</a-alert>
      <div class="filters">
        <a-input v-model="filters.strategy_id" allow-clear placeholder="策略 ID" @press-enter="reloadFirst" />
        <a-select v-model="filters.enabled" allow-clear placeholder="状态" @change="reloadFirst">
          <a-option :value="true">运行中</a-option>
          <a-option :value="false">已停用</a-option>
        </a-select>
        <a-button type="primary" @click="reloadFirst">查询</a-button>
      </div>
      <a-table
        row-key="instance_id"
        :data="store.instances"
        :loading="store.loading"
        :pagination="pagination"
        @page-change="changePage"
      >
        <template #columns>
          <a-table-column title="实例">
            <template #cell="{ record }">
              <a-link @click="openDetail(record.instance_id)">{{ record.instance_id }}</a-link>
              <div class="muted">{{ record.strategy_id }}</div>
            </template>
          </a-table-column>
          <a-table-column title="组合账户">
            <template #cell="{ record }">{{ record.logical_account_id || "观察模式" }}</template>
          </a-table-column>
          <a-table-column title="状态" :width="110">
            <template #cell="{ record }"><StatusBadge :status="record.enabled ? 'ENABLED' : 'DISABLED'" /></template>
          </a-table-column>
          <a-table-column title="运行会话" data-index="session_id" :ellipsis="true" :tooltip="true" />
          <a-table-column title="更新时间">
            <template #cell="{ record }">{{ formatTime(record.updated_at) }}</template>
          </a-table-column>
          <a-table-column title="操作" :width="110">
            <template #cell="{ record }">
              <a-button size="mini" type="text" @click="openDetail(record.instance_id)">详情</a-button>
            </template>
          </a-table-column>
        </template>
      </a-table>

      <a-modal v-model:visible="createVisible" title="新增策略实例" :width="640" @ok="create">
        <a-form :model="form" layout="vertical">
          <a-grid :cols="2" :col-gap="16">
            <a-form-item label="实例 ID" required><a-input v-model="form.instance_id" /></a-form-item>
            <a-form-item label="策略" required>
              <a-select v-model="form.strategy_id" allow-search>
                <a-option v-for="item in store.strategies" :key="item.strategy_id" :value="item.strategy_id">
                  {{ item.name }} ({{ item.strategy_id }})
                </a-option>
              </a-select>
            </a-form-item>
            <a-form-item label="组合账户编号">
              <a-input v-model="form.logical_account_id" placeholder="留空为观察模式" />
            </a-form-item>
          </a-grid>
          <a-form-item label="输入绑定 JSON">
            <a-textarea v-model="form.input_bindings_json" class="code-input" :auto-size="{ minRows: 4, maxRows: 8 }" />
          </a-form-item>
          <a-alert type="info">输入绑定决定行情 View 和 Factor 输出；观察模式只计算和保存结果，不投递 Trade。</a-alert>
        </a-form>
      </a-modal>
    </div>
  </div>
</template>

<script setup lang="ts">
import { onMounted, reactive, ref, watch } from "vue";
import { useRouter } from "vue-router";
import { Message } from "@arco-design/web-vue";
import { createInstance } from "@/api/strategy";
import { useSpaceStore } from "@/store/modules/space";
import { useStrategyStore } from "@/store/modules/strategy";
import StatusBadge from "@/views/strategy/components/strategy-status-badge.vue";

defineOptions({ name: "StrategyRunning" });
const router = useRouter();
const store = useStrategyStore();
const spaceStore = useSpaceStore();
const createVisible = ref(false);
const filters = reactive<{ strategy_id: string; enabled?: boolean }>({ strategy_id: "" });
const pagination = reactive({ current: 1, pageSize: 20, total: 0 });
const form = reactive({
  instance_id: "",
  strategy_id: "",
  logical_account_id: "",
  input_bindings_json: "{}"
});

async function refresh() {
  await store.loadInstances({
    ...filters,
    space_id: spaceStore.selectedSpaceId,
    page: pagination.current,
    page_size: pagination.pageSize
  });
  pagination.total = store.totalInstances;
}

async function openCreate() {
  if (!store.strategies.length) await store.loadStrategies({ page: 1, page_size: 100 });
  createVisible.value = true;
}

async function create() {
  if (!form.instance_id.trim() || !form.strategy_id || !form.input_bindings_json.trim()) {
    Message.warning("请填写所有必填字段");
    return false;
  }
  try {
    JSON.parse(form.input_bindings_json);
  } catch {
    Message.warning("输入绑定必须是合法 JSON");
    return false;
  }
  await createInstance({
    instance_id: form.instance_id.trim(),
    strategy_id: form.strategy_id,
    logical_account_id: form.logical_account_id.trim(),
    space_id: spaceStore.requireSpaceId(),
    input_bindings_json: form.input_bindings_json,
    enabled: false
  });
  createVisible.value = false;
  await refresh();
  return true;
}

function reloadFirst() {
  pagination.current = 1;
  refresh();
}
function changePage(page: number) {
  pagination.current = page;
  refresh();
}
function openDetail(instanceId: string) {
  router.push({ name: "strategy-detail", params: { instanceId } });
}
function formatTime(value: string) {
  return value ? new Date(value).toLocaleString() : "-";
}
onMounted(refresh);
watch(
  () => spaceStore.selectedSpaceId,
  () => reloadFirst()
);
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
.filters {
  display: flex;
  gap: 8px;
  margin-bottom: var(--moox-space-2);
}
.filters .arco-input-wrapper,
.filters .arco-select-view {
  width: 180px;
}
.top-alert {
  margin-bottom: var(--moox-space-2);
}
</style>
