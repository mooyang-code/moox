<template>
  <div class="moox-page">
    <div class="moox-inner">
      <div class="page-head">
        <div>
          <h2>运行实例</h2>
          <span>实例绑定一个定义、一个 View 和可选的组合账户；启用后由 View 就绪事件驱动计算。</span>
        </div>
        <a-space
          ><a-button :loading="store.listLoading" aria-label="刷新实例" @click="refresh"
            ><template #icon><icon-refresh /></template>刷新</a-button
          ><a-button type="primary" status="success" :disabled="!spaceStore.selectedSpaceId" @click="createVisible = true"
            ><template #icon><icon-plus /></template>新建实例</a-button
          ></a-space
        >
      </div>
      <a-alert v-if="store.error" type="error" show-icon class="top-alert"
        >实例加载失败：{{ store.error
        }}<template #action><a-button size="small" @click="refresh">重试</a-button></template></a-alert
      >
      <div class="filters">
        <a-select v-model="filters.strategy_id" allow-clear allow-search placeholder="策略定义" @change="reloadFirst"
          ><a-option v-for="item in store.strategies" :key="item.strategy_id" :value="item.strategy_id"
            >{{ item.name }}（{{ item.strategy_id }}）</a-option
          ></a-select
        ><a-select v-model="filters.enabled" allow-clear placeholder="启用状态" @change="reloadFirst"
          ><a-option :value="true">已启用</a-option><a-option :value="false">已停用</a-option></a-select
        ><a-button @click="reloadFirst">查询</a-button>
      </div>
      <a-table
        row-key="instance_id"
        :data="store.instances"
        :loading="store.listLoading"
        :pagination="pagination"
        :scroll="{ x: 1000 }"
        @page-change="changePage"
      >
        <template #columns>
          <a-table-column title="实例" :width="260"
            ><template #cell="{ record }"
              ><a-link @click="openDetail(record.instance_id)">{{ record.instance_id }}</a-link>
              <div class="muted">{{ strategyName(record.strategy_id) }} · {{ record.strategy_id }}</div></template
            ></a-table-column
          >
          <a-table-column title="View" :width="240" :ellipsis="true" :tooltip="true"
            ><template #cell="{ record }"
              ><span class="mono">{{ record.view_id }}</span></template
            ></a-table-column
          >
          <a-table-column title="模式" :width="120"
            ><template #cell="{ record }"
              ><a-tag :color="record.logical_account_id ? 'orange' : 'blue'">{{
                record.logical_account_id ? "交易模式" : "仅计算"
              }}</a-tag></template
            ></a-table-column
          >
          <a-table-column title="状态" :width="160"
            ><template #cell="{ record }"><StatusBadge :enabled="record.enabled" :health="record.health" /></template
          ></a-table-column>
          <a-table-column title="更新时间" :width="180"
            ><template #cell="{ record }">{{ formatStrategyTime(record.updated_at) }}</template></a-table-column
          >
          <a-table-column title="操作" :width="140" fixed="right">
            <template #cell="{ record }">
              <a-button type="text" size="small" @click="openDetail(record.instance_id)">详情</a-button>
              <a-popconfirm
                v-if="!record.enabled"
                content="删除后实例从列表与路由中移除，历史结果与解释仍可查看。确认删除？"
                @ok="remove(record.instance_id)"
              >
                <a-button type="text" size="small" status="danger" :disabled="Boolean(record.session_id)">删除</a-button>
              </a-popconfirm>
            </template>
          </a-table-column>
        </template>
      </a-table>
      <a-empty v-if="!store.listLoading && !store.error && !store.instances.length" description="暂无策略实例" />
      <StrategyInstanceCreate
        v-model:visible="createVisible"
        :strategies="store.strategies"
        :space-id="spaceStore.selectedSpaceId"
        @created="created"
      />
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref, watch } from "vue";
import { Message } from "@arco-design/web-vue";
import { useRouter } from "vue-router";
import type { Strategy } from "@/api/strategy-types";
import { useSpaceStore } from "@/store/modules/space";
import { useStrategyStore } from "@/store/modules/strategy";
import StatusBadge from "@/views/strategy/components/strategy-status-badge.vue";
import StrategyInstanceCreate from "@/views/strategy/components/strategy-instance-create.vue";
import { formatStrategyTime } from "@/views/strategy/model";

defineOptions({ name: "StrategyRunning" });
const router = useRouter();
const store = useStrategyStore();
const spaceStore = useSpaceStore();
const createVisible = ref(false);
const filters = reactive<{ strategy_id?: string; enabled?: boolean }>({});
const pagination = reactive({ current: 1, pageSize: 20, total: computed(() => store.totalInstances) });
async function refresh() {
  await Promise.all([
    store.loadInstances({ ...filters, page: pagination.current, page_size: pagination.pageSize }),
    store.strategiesComplete ? Promise.resolve() : store.loadAllStrategies(200)
  ]).catch(() => undefined);
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
function strategyName(id: string) {
  return store.strategies.find((item: Strategy) => item.strategy_id === id)?.name || "定义已删除或未加载";
}
function created(instanceId: string) {
  createVisible.value = false;
  openDetail(instanceId);
}
async function remove(instanceId: string) {
  try {
    await store.removeInstance(instanceId);
    Message.success("实例已删除");
  } catch (err) {
    Message.error(err instanceof Error ? err.message : "删除失败");
  }
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
.mono {
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  font-size: 12px;
}
.filters {
  display: flex;
  gap: 8px;
  margin-bottom: var(--moox-space-2);
}
.filters .arco-select-view {
  width: 220px;
}
.top-alert {
  margin-bottom: var(--moox-space-2);
}
@media (max-width: 640px) {
  .page-head {
    align-items: flex-start;
    flex-direction: column;
  }
  .filters {
    flex-wrap: wrap;
  }
  .filters .arco-select-view {
    width: 100%;
  }
}
</style>
