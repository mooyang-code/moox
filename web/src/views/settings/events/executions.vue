<template>
  <section>
    <div class="filters">
      <a-space wrap>
        <a-select v-model="filters.rule_id" placeholder="全部规则" allow-clear style="width: 220px" @change="onFilterChange">
          <a-option v-for="rule in rules" :key="rule.rule_id" :value="rule.rule_id">{{ rule.name }}</a-option>
        </a-select>
        <a-select v-model="filters.status" placeholder="全部状态" allow-clear style="width: 140px" @change="onFilterChange">
          <a-option v-for="item in EXECUTION_STATUS_FILTERS" :key="item.value" :value="item.value">
            {{ item.label }}
          </a-option>
        </a-select>
      </a-space>
    </div>

    <a-table
      row-key="execution_id"
      size="small"
      :bordered="{ cell: true }"
      :loading="loading"
      :data="rows"
      :pagination="pagination"
      :scroll="{ x: 'max-content' }"
      @page-change="onPageChange"
      @page-size-change="onPageSizeChange"
    >
      <template #columns>
        <a-table-column title="时间" :width="170">
          <template #cell="{ record }">{{ formatMs(record.created_at_ms) }}</template>
        </a-table-column>
        <a-table-column title="规则" data-index="rule_name" :width="180" :ellipsis="true" :tooltip="true" />
        <a-table-column title="事件" :width="240" :ellipsis="true" :tooltip="true">
          <template #cell="{ record }">{{ eventTitle(events, record.event_name) }}　{{ record.subject_id }}</template>
        </a-table-column>
        <a-table-column title="状态" :width="90">
          <template #cell="{ record }">
            <span :class="['status', `status-${statusTone(record.status)}`]">{{ statusText(record.status) }}</span>
          </template>
        </a-table-column>
        <a-table-column title="耗时" :width="100">
          <template #cell="{ record }">{{ formatDuration(record.duration_ms) }}</template>
        </a-table-column>
        <a-table-column title="失败原因" :width="260" :ellipsis="true" :tooltip="true">
          <template #cell="{ record }">{{ record.last_error || "-" }}</template>
        </a-table-column>
        <a-table-column title="操作" :width="220" align="center" fixed="right">
          <template #cell="{ record }">
            <a-space>
              <a-button size="mini" type="text" @click="openDetail(record)">详情</a-button>
              <a-button v-if="canRetry(record)" size="mini" type="text" @click="act('retry', record)">重新执行</a-button>
              <a-popconfirm v-if="canCancel(record)" :content="cancelConfirmText(record)" @ok="act('cancel', record)">
                <a-button size="mini" type="text" status="danger">{{ cancelLabel(record) }}</a-button>
              </a-popconfirm>
              <a-button v-if="canAcknowledge(record)" size="mini" type="text" @click="act('ack', record)"> 确认已处理 </a-button>
            </a-space>
          </template>
        </a-table-column>
      </template>
    </a-table>

    <ExecutionDrawer
      v-model:visible="drawerVisible"
      :execution-id="detailId"
      :refresh-token="refreshToken"
      :events="events"
      @action="(kind, item) => act(kind, item)"
    />
  </section>
</template>

<script setup lang="ts">
import { onMounted, reactive, ref } from "vue";
import { Message } from "@arco-design/web-vue";
import {
  acknowledgeEventExecution,
  cancelEventExecution,
  listEventExecutions,
  listEventRules,
  listSubscribableEvents,
  retryEventExecution,
  type EventExecution,
  type EventRule,
  type ExecutionStatus,
  type SubscribableEvent
} from "@/api/callback-dispatcher";
import { reportControlError } from "@/api/admin/http";
import { defaultPagination } from "@/views/data/shared/metadata-utils";
import ExecutionDrawer from "./execution-drawer.vue";
import {
  EXECUTION_STATUS_FILTERS,
  canAcknowledge,
  canCancel,
  cancelConfirmText,
  cancelLabel,
  canRetry,
  eventTitle,
  formatDuration,
  formatMs,
  statusText,
  statusTone
} from "./event-center-utils";

defineOptions({ name: "EventCenterExecutions" });

const rows = ref<EventExecution[]>([]);
const rules = ref<EventRule[]>([]);
const events = ref<SubscribableEvent[]>([]);
const loading = ref(false);
const pagination = reactive(defaultPagination());
const filters = reactive<{ rule_id: string; status: ExecutionStatus | "" }>({ rule_id: "", status: "" });
const drawerVisible = ref(false);
const detailId = ref("");
const refreshToken = ref(0);

async function load() {
  loading.value = true;
  try {
    const rsp = await listEventExecutions({
      rule_id: filters.rule_id || undefined,
      status: filters.status || undefined,
      page: { page: pagination.current, size: pagination.pageSize }
    });
    rows.value = rsp.executions;
    pagination.total = rsp.page_result?.total ?? rsp.executions.length;
  } catch (error) {
    reportControlError(error);
  } finally {
    loading.value = false;
  }
}

async function loadOptions() {
  try {
    [events.value, rules.value] = await Promise.all([
      listSubscribableEvents().then(catalog => catalog.events),
      listEventRules({ page: 1, size: 200 }).then(rsp => rsp.rules)
    ]);
  } catch (error) {
    reportControlError(error);
  }
}

function onFilterChange() {
  pagination.current = 1;
  void load();
}

function onPageChange(page: number) {
  pagination.current = page;
  void load();
}

function onPageSizeChange(size: number) {
  pagination.current = 1;
  pagination.pageSize = size;
  void load();
}

function openDetail(item: EventExecution) {
  detailId.value = item.execution_id;
  drawerVisible.value = true;
}

async function act(kind: "retry" | "cancel" | "ack", item: EventExecution) {
  try {
    if (kind === "retry") await retryEventExecution(item.execution_id);
    else if (kind === "cancel") await cancelEventExecution(item.execution_id);
    else await acknowledgeEventExecution(item.execution_id);
    Message.success(
      kind === "retry" ? "已重新开始执行" : kind === "cancel" ? (item.cleanup_pending ? "善后已完成" : "已取消") : "已确认"
    );
    await load();
  } catch (error) {
    reportControlError(error);
  } finally {
    // 不管成败，抽屉里的详情都按最新状态重读（取消可能已生效而善后失败）。
    refreshToken.value += 1;
  }
}

onMounted(() => {
  void loadOptions();
  void load();
});

defineExpose({ load });
</script>

<style scoped>
.filters {
  margin-bottom: var(--moox-space-toolbar-table);
}

.status-ok {
  color: rgb(var(--green-6));
}

.status-fail {
  color: rgb(var(--red-6));
}

.status-run,
.status-wait {
  color: rgb(var(--arcoblue-6));
}

.status-cancel {
  color: var(--color-text-3);
}
</style>
