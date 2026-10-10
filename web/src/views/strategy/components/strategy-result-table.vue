<template>
  <div class="result-toolbar">
    <a-radio-group :model-value="scope" type="button" @change="changeScope"
      ><a-radio value="session">本次启用</a-radio><a-radio value="all">全部历史</a-radio></a-radio-group
    >
    <span class="muted">投递状态不代表成交状态</span>
  </div>
  <a-table
    row-key="result_id"
    size="small"
    :data="results"
    :pagination="pagination"
    :scroll="{ x: 980 }"
    @page-change="changePage"
  >
    <template #columns>
      <a-table-column title="K 线结束" :width="170"
        ><template #cell="{ record }">{{ formatStrategyTime(record.bar_end_time) }}</template></a-table-column
      >
      <a-table-column title="状态" :width="90"
        ><template #cell="{ record }"
          ><a-tag size="small" :color="record.status === 'ok' ? 'green' : 'orange'">{{
            record.status === "ok" ? "ok" : "跳过"
          }}</a-tag></template
        ></a-table-column
      >
      <a-table-column title="跳过原因" :width="190" :ellipsis="true" :tooltip="true"
        ><template #cell="{ record }">{{
          record.status === "skipped" ? skipReasonLabel(record.skip_reason) : "-"
        }}</template></a-table-column
      >
      <a-table-column title="目标数" :width="80"
        ><template #cell="{ record }">{{ record.status === "ok" ? record.targets.length : "-" }}</template></a-table-column
      >
      <a-table-column title="gross" :width="90"
        ><template #cell="{ record }">{{
          record.status === "ok" ? percent(parseSummary(record.summary_json).gross) : "-"
        }}</template></a-table-column
      >
      <a-table-column title="现金" :width="90"
        ><template #cell="{ record }">{{
          record.status === "ok" ? percent(parseSummary(record.summary_json).cash) : "-"
        }}</template></a-table-column
      >
      <a-table-column title="换手" :width="90"
        ><template #cell="{ record }">{{
          record.status === "ok" ? percent(parseSummary(record.summary_json).turnover) : "-"
        }}</template></a-table-column
      >
      <a-table-column title="投递状态" :width="110"
        ><template #cell="{ record }"
          ><a-tag size="small" :color="publishColor(record.publish_status)">{{
            publishLabel(record.publish_status)
          }}</a-tag></template
        ></a-table-column
      >
      <a-table-column title="操作" :width="90" fixed="right"
        ><template #cell="{ record }"
          ><a-button type="text" size="small" @click="explain(record)">解释</a-button></template
        ></a-table-column
      >
    </template>
  </a-table>
  <a-empty v-if="!results.length" description="暂无策略结果" />
  <a-drawer v-model:visible="explainVisible" :width="'min(980px, 100vw)'" :footer="false" unmount-on-close :title="explainTitle">
    <a-spin :loading="explainLoading" class="explain-body">
      <a-alert v-if="explainError" type="error" show-icon>{{ explainError }}</a-alert>
      <template v-else-if="detail">
        <a-tabs default-active-key="items">
          <a-tab-pane key="items" title="逐标的解释"><ResultItems :result="detail.result" :items="detail.items" /></a-tab-pane>
          <a-tab-pane key="targets" title="目标权重"><TargetList :targets="detail.result.targets" /></a-tab-pane>
          <a-tab-pane key="binding" title="当时的绑定"
            ><ResolvedTable :resolved-json="detail.resolved_json" empty-text="会话快照缺失"
          /></a-tab-pane>
          <a-tab-pane key="dsl" title="当时的 DSL">
            <pre class="code-block">{{ detail.dsl_yaml || "DSL 版本缺失" }}</pre>
          </a-tab-pane>
        </a-tabs>
      </template>
    </a-spin>
  </a-drawer>
</template>

<script setup lang="ts">
import { computed, defineComponent, h, ref } from "vue";
import type { InstrumentTarget, StrategyResult, StrategyResultDetail } from "@/api/strategy-types";
import { useStrategyStore } from "@/store/modules/strategy";
import ResolvedTable from "@/views/strategy/components/strategy-resolved-table.vue";
import ResultItems from "@/views/strategy/components/strategy-result-items.vue";
import { formatStrategyTime, parseSummary, percent, skipReasonLabel } from "@/views/strategy/model";

const props = defineProps<{
  results: StrategyResult[];
  total: number;
  page: number;
  pageSize: number;
  scope: "session" | "all";
}>();
const emit = defineEmits<{ page: [number, "session" | "all"] }>();
const store = useStrategyStore();
const explainVisible = ref(false);
const explainLoading = ref(false);
const explainError = ref("");
const detail = ref<StrategyResultDetail | null>(null);
const explainTitle = ref("结果解释");
let explainRequest = 0;
const pagination = computed(() => ({ current: props.page, pageSize: props.pageSize, total: props.total }));
const scope = computed(() => props.scope);

const TargetList = defineComponent({
  props: { targets: { type: Array as () => InstrumentTarget[], required: true } },
  setup(listProps) {
    return () =>
      listProps.targets.length
        ? h(
            "div",
            { class: "target-list" },
            listProps.targets.map(target =>
              h("div", { class: "target-row" }, [h("span", target.instrument_id), h("strong", percent(target.target_weight))])
            )
          )
        : h("div", { class: "muted" }, "无目标（转为现金或本期跳过）");
  }
});

function changePage(page: number) {
  emit("page", page, props.scope);
}
function changeScope(value: string | number | boolean) {
  emit("page", 1, value === "all" ? "all" : "session");
}
async function explain(record: StrategyResult) {
  const requestId = ++explainRequest;
  explainTitle.value = `结果解释 · ${formatStrategyTime(record.bar_end_time)}`;
  explainVisible.value = true;
  explainLoading.value = true;
  explainError.value = "";
  detail.value = null;
  try {
    const loaded = await store.loadResultDetail(record.result_id);
    if (requestId === explainRequest) detail.value = loaded;
  } catch (err) {
    if (requestId === explainRequest) explainError.value = err instanceof Error ? err.message : "解释明细加载失败";
  } finally {
    if (requestId === explainRequest) explainLoading.value = false;
  }
}
function publishLabel(value?: string) {
  return (
    ({ none: "无需投递", pending: "待投递", sent: "已发送", cancelled: "已取消投递" } as Record<string, string>)[value || ""] ||
    "未知"
  );
}
function publishColor(value?: string) {
  return ({ pending: "orange", sent: "green", cancelled: "gray" } as Record<string, string>)[value || ""] || "blue";
}
</script>

<style scoped>
.result-toolbar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  margin-bottom: 12px;
}
.muted {
  color: var(--color-text-3);
  font-size: 12px;
}
.explain-body {
  display: block;
  width: 100%;
  min-height: 200px;
}
.code-block {
  max-height: 560px;
  overflow: auto;
  margin: 0;
  padding: 12px;
  background: var(--color-fill-2);
  white-space: pre-wrap;
  font:
    12px/1.6 ui-monospace,
    SFMono-Regular,
    Menlo,
    monospace;
}
:deep(.target-list) {
  display: grid;
  gap: 6px;
}
:deep(.target-row) {
  display: flex;
  justify-content: space-between;
  max-width: 360px;
  padding: 6px 10px;
  background: var(--color-fill-2);
}
@media (max-width: 640px) {
  .result-toolbar {
    align-items: flex-start;
    flex-direction: column;
  }
}
</style>
