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
        <a-table-column title="状态" :width="120">
          <template #cell="{ record }">
            <span :class="['status', `status-${executionStatusTone(record)}`]">{{ executionStatusText(record) }}</span>
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
              <a-button
                v-if="canRetry(record)"
                size="mini"
                type="text"
                :disabled="Boolean(acting)"
                :loading="acting === `retry/${record.execution_id}`"
                @click="act('retry', record)"
              >
                重新执行
              </a-button>
              <a-popconfirm v-if="canCancel(record)" :content="cancelConfirmText(record)" @ok="act('cancel', record)">
                <a-button
                  size="mini"
                  type="text"
                  status="danger"
                  :disabled="Boolean(acting)"
                  :loading="acting === `cancel/${record.execution_id}`"
                >
                  {{ cancelLabel(record) }}
                </a-button>
              </a-popconfirm>
              <a-button
                v-if="canAcknowledge(record)"
                size="mini"
                type="text"
                :disabled="Boolean(acting)"
                :loading="acting === `ack/${record.execution_id}`"
                @click="act('ack', record)"
              >
                确认已处理
              </a-button>
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
      :busy="Boolean(acting)"
      @action="(kind, item) => act(kind, item)"
    />
  </section>
</template>

<script setup lang="ts">
import { onActivated, onMounted, reactive, ref } from "vue";
import { Message } from "@arco-design/web-vue";
import {
  acknowledgeEventExecution,
  cancelEventExecution,
  listAllEventRules,
  listEventExecutions,
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
  executionStatusText,
  executionStatusTone,
  formatDuration,
  formatMs
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
// 正在做的操作（“动作/记录编号”）：做完之前其余操作按钮都不能点，防止连点重复提交。
const acting = ref("");

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
      listAllEventRules()
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
  if (acting.value) return;
  acting.value = `${kind}/${item.execution_id}`;
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
    acting.value = "";
    // 不管成败，抽屉里的详情都按最新状态重读（取消可能已生效而善后失败）。
    refreshToken.value += 1;
  }
}

/** 页面上的“刷新”：规则可能在另一个页签里改过，筛选用的规则和事件也一起重读。 */
function refresh() {
  void loadOptions();
  void load();
}

onMounted(() => void load());
// 页签切回来时（keep-alive）规则可能已经增删，重读筛选项；首次显示时也会调用。
onActivated(() => void loadOptions());

defineExpose({ load, refresh });
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
