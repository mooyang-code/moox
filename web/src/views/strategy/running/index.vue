<template>
  <div class="moox-page">
    <div class="moox-inner">
      <div class="page-head">
        <div>
          <h2>Strategy Runner</h2>
          <span>同一策略可创建多个运行实例，每个运行实例最多关联一个组合账户。</span>
        </div>
        <a-space>
          <a-button :loading="store.loading" @click="refresh"
            ><template #icon><icon-refresh /></template>刷新</a-button
          >
          <a-button type="primary" status="success" :disabled="!spaceStore.selectedSpaceId" @click="openCreate"
            ><template #icon><icon-plus /></template>新增 Runner</a-button
          >
        </a-space>
      </div>
      <a-alert v-if="store.error" type="error" show-icon class="top-alert">{{ store.error }}</a-alert>
      <div class="filters">
        <a-input v-model="filters.strategy_id" allow-clear placeholder="策略 ID" @press-enter="reloadFirst" />
        <a-select v-model="filters.status" allow-clear placeholder="状态" @change="reloadFirst">
          <a-option value="ENABLED">ENABLED</a-option>
          <a-option value="DISABLED">DISABLED</a-option>
        </a-select>
        <a-button type="primary" @click="reloadFirst">查询</a-button>
      </div>
      <a-table
        row-key="runner_id"
        :data="store.runners"
        :loading="store.loading"
        :pagination="pagination"
        @page-change="changePage"
      >
        <template #columns>
          <a-table-column title="Runner">
            <template #cell="{ record }">
              <a-link @click="openDetail(record.runner_id)">{{ record.runner_id }}</a-link>
              <div class="muted">{{ record.strategy_id }}</div>
            </template>
          </a-table-column>
          <a-table-column title="数据视图" data-index="view_id" />
          <a-table-column title="频率" data-index="frequency" :width="110" />
          <a-table-column title="组合账户">
            <template #cell="{ record }">{{ record.logical_account_id || "观察模式" }}</template>
          </a-table-column>
          <a-table-column title="状态" :width="110">
            <template #cell="{ record }"><StatusBadge :status="record.status" /></template>
          </a-table-column>
          <a-table-column title="最近成功">
            <template #cell="{ record }">{{ formatTime(record.last_success_at) }}</template>
          </a-table-column>
          <a-table-column title="最近错误" data-index="last_error" :ellipsis="true" :tooltip="true" />
          <a-table-column title="操作" :width="110">
            <template #cell="{ record }">
              <a-button size="mini" type="text" @click="openDetail(record.runner_id)">详情</a-button>
            </template>
          </a-table-column>
        </template>
      </a-table>

      <a-modal v-model:visible="createVisible" title="新增 Strategy Runner" :width="640" @ok="create">
        <a-form :model="form" layout="vertical">
          <a-grid :cols="2" :col-gap="16">
            <a-form-item label="Runner ID" required><a-input v-model="form.runner_id" /></a-form-item>
            <a-form-item label="策略" required>
              <a-select v-model="form.strategy_id" allow-search>
                <a-option v-for="item in store.strategies" :key="item.strategy_id" :value="item.strategy_id">
                  {{ item.name }} ({{ item.strategy_id }})
                </a-option>
              </a-select>
            </a-form-item>
            <a-form-item label="数据视图 ID" required><a-input v-model="form.view_id" /></a-form-item>
            <a-form-item label="频率" required><a-input v-model="form.frequency" placeholder="1m" /></a-form-item>
            <a-form-item label="组合账户编号">
              <a-input v-model="form.logical_account_id" placeholder="留空为观察模式" />
            </a-form-item>
          </a-grid>
          <a-form-item label="参数 JSON">
            <a-textarea v-model="form.params_json" :auto-size="{ minRows: 5, maxRows: 12 }" />
          </a-form-item>
        </a-form>
      </a-modal>
    </div>
  </div>
</template>

<script setup lang="ts">
import { onMounted, reactive, ref, watch } from "vue";
import { useRouter } from "vue-router";
import { Message } from "@arco-design/web-vue";
import { createRunner } from "@/api/strategy";
import type { StrategyRunnerStatus } from "@/api/strategy-types";
import { useSpaceStore } from "@/store/modules/space";
import { useStrategyStore } from "@/store/modules/strategy";
import StatusBadge from "@/views/strategy/components/strategy-status-badge.vue";

defineOptions({ name: "StrategyRunning" });
const router = useRouter();
const store = useStrategyStore();
const spaceStore = useSpaceStore();
const createVisible = ref(false);
const filters = reactive<{ strategy_id: string; status: StrategyRunnerStatus | "" }>({ strategy_id: "", status: "" });
const pagination = reactive({ current: 1, pageSize: 20, total: 0 });
const form = reactive({
  runner_id: "",
  strategy_id: "",
  view_id: "",
  frequency: "1m",
  params_json: "{}",
  logical_account_id: ""
});

async function refresh() {
  await store.loadRunners({
    ...filters,
    space_id: spaceStore.selectedSpaceId,
    page: pagination.current,
    page_size: pagination.pageSize
  });
  pagination.total = store.totalRunners;
}

async function openCreate() {
  if (!store.strategies.length) await store.loadStrategies({ page: 1, page_size: 100 });
  createVisible.value = true;
}

async function create() {
  if (!form.runner_id.trim() || !form.strategy_id || !form.view_id.trim() || !form.frequency.trim()) {
    Message.warning("请填写所有必填字段");
    return false;
  }
  try {
    JSON.parse(form.params_json || "{}");
  } catch {
    Message.warning("参数必须是合法 JSON");
    return false;
  }
  await createRunner({
    ...form,
    runner_id: form.runner_id.trim(),
    view_id: form.view_id.trim(),
    frequency: form.frequency.trim(),
    params_json: form.params_json || "{}",
    logical_account_id: form.logical_account_id.trim(),
    space_id: spaceStore.requireSpaceId(),
    status: "DISABLED",
    current_targets: [],
    command_sequence: "0",
    last_result_id: "",
    last_success_at: "",
    last_error: "",
    created_at: "",
    updated_at: ""
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
function openDetail(runnerId: string) {
  router.push({ name: "strategy-detail", params: { runnerId } });
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
