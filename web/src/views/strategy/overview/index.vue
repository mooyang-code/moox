<template>
  <div class="moox-page">
    <div class="moox-inner">
      <div class="page-head">
        <div>
          <h2>策略定义</h2>
          <span>DSL 是唯一策略配置来源，保存定义不会启用任何实例。</span>
        </div>
        <a-space>
          <a-button :loading="store.listLoading" aria-label="刷新策略定义" @click="refresh"
            ><template #icon><icon-refresh /></template>刷新</a-button
          >
          <a-button @click="router.push({ name: 'strategy-replay' })">回放</a-button>
          <a-button type="primary" status="success" @click="router.push({ name: 'strategy-definition-new' })"
            ><template #icon><icon-plus /></template>新建策略</a-button
          >
        </a-space>
      </div>
      <a-alert v-if="store.error" type="error" show-icon class="top-alert"
        >策略定义加载失败：{{ store.error
        }}<template #action><a-button size="small" @click="refresh">重试</a-button></template></a-alert
      >
      <a-table
        row-key="strategy_id"
        :data="store.strategies"
        :loading="store.listLoading"
        :pagination="pagination"
        :scroll="{ x: 900 }"
        @page-change="changePage"
      >
        <template #columns>
          <a-table-column title="策略名称" :width="240"
            ><template #cell="{ record }"
              ><a-link @click="edit(record.strategy_id)">{{ record.name || "未命名策略" }}</a-link>
              <div class="muted">{{ record.strategy_id }}</div></template
            ></a-table-column
          >
          <a-table-column title="DSL 摘要"
            ><template #cell="{ record }"
              ><span class="summary">{{ summarizeDSL(record.dsl_yaml) }}</span></template
            ></a-table-column
          >
          <a-table-column title="版本" :width="140"
            ><template #cell="{ record }"
              ><a-tooltip :content="record.dsl_hash"
                ><span class="mono">{{ shortHash(record.dsl_hash) }}</span></a-tooltip
              ></template
            ></a-table-column
          >
          <a-table-column title="更新时间" :width="180"
            ><template #cell="{ record }">{{
              formatStrategyTime(record.updated_at || record.created_at)
            }}</template></a-table-column
          >
          <a-table-column title="操作" :width="200" fixed="right">
            <template #cell="{ record }">
              <a-button type="text" size="small" @click="edit(record.strategy_id)">编辑</a-button>
              <a-button
                type="text"
                size="small"
                @click="router.push({ name: 'strategy-replay', query: { strategy_id: record.strategy_id } })"
                >回放</a-button
              >
              <a-popconfirm
                content="删除后定义从列表消失，已有会话与历史结果保留。仍被实例引用的定义不能删除。确认删除？"
                @ok="remove(record.strategy_id)"
              >
                <a-button type="text" size="small" status="danger">删除</a-button>
              </a-popconfirm>
            </template>
          </a-table-column>
        </template>
      </a-table>
      <a-empty v-if="!store.listLoading && !store.error && !store.strategies.length" description="暂无策略定义" />
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive } from "vue";
import { Message } from "@arco-design/web-vue";
import { useRouter } from "vue-router";
import { useStrategyStore } from "@/store/modules/strategy";
import { summarizeDSL } from "@/views/strategy/dsl";
import { formatStrategyTime, shortHash } from "@/views/strategy/model";

defineOptions({ name: "StrategyOverview" });
const router = useRouter();
const store = useStrategyStore();
const pagination = reactive({ current: 1, pageSize: 20, total: computed(() => store.totalStrategies) });
async function refresh() {
  await store.loadStrategies({ page: pagination.current, page_size: pagination.pageSize }).catch(() => undefined);
}
function changePage(page: number) {
  pagination.current = page;
  refresh();
}
function edit(strategyId: string) {
  router.push({ name: "strategy-definition-edit", params: { strategyId } });
}
async function remove(strategyId: string) {
  try {
    await store.removeStrategy(strategyId);
    Message.success("策略定义已删除");
  } catch (err) {
    Message.error(err instanceof Error ? err.message : "删除失败");
  }
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
.summary {
  color: var(--color-text-2);
}
.mono {
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  font-size: 12px;
}
@media (max-width: 640px) {
  .page-head {
    align-items: flex-start;
    flex-direction: column;
  }
}
</style>
