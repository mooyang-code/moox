<template>
  <div class="moox-page">
    <div class="moox-inner">
      <div class="page-head">
        <h2>处理记录</h2>
        <span class="muted">每 30 秒自动刷新</span>
      </div>
      <div class="filters">
        <a-space wrap>
          <a-select v-model="filters.rule_id" placeholder="全部订阅" allow-clear style="width: 220px" @change="onFilterChange">
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
          <a-table-column title="时间" :width="200">
            <template #cell="{ record }">{{ formatInstant(record.created_at_ms, "UTC") }}</template>
          </a-table-column>
          <a-table-column title="订阅" data-index="rule_name" :width="180" :ellipsis="true" :tooltip="true" />
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
                  @click="requestAct('retry', record)"
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
        :visible="drawerVisible"
        :execution-id="detailId"
        :refresh-token="refreshToken"
        :events="events"
        :busy="Boolean(acting)"
        @update:visible="onDrawerVisible"
        @action="(kind, item) => requestAct(kind, item)"
      />
    </div>
  </div>
</template>

<script setup lang="ts">
// 事件中心和空间无关，时间都按 UTC 显示
import { formatInstant } from "@/utils/time-zone";
import { onMounted, reactive, ref, watch } from "vue";
import { useRoute, useRouter } from "vue-router";
import { Message, Modal } from "@arco-design/web-vue";
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
import { usePolling } from "@/hooks/usePolling";
import ExecutionDrawer from "./execution-drawer.vue";
import {
  EXECUTION_STATUS_FILTERS,
  canAcknowledge,
  canCancel,
  cancelConfirmText,
  cancelLabel,
  canRetry,
  eventTitle,
  executionQuery,
  parseExecutionQuery,
  executionStatusText,
  executionStatusTone,
  formatDuration,
  pageAfterShrink,
  systemRetryConfirmText
} from "../shared/event-center-utils";

defineOptions({ name: "EventExecutions" });

const route = useRoute();
const router = useRouter();

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

// 列表请求的序号：换筛选、翻页、操作后重读等连续请求只认最后一次，迟到的旧结果不会覆盖新的。
let loadSeq = 0;
/** background：自动刷新，不转加载态、不弹提示，失败时保留上次的列表。 */
async function load(background = false) {
  const seq = ++loadSeq;
  if (!background) loading.value = true;
  try {
    const rsp = await listEventExecutions(
      {
        rule_id: filters.rule_id || undefined,
        status: filters.status || undefined,
        page: { page: pagination.current, size: pagination.pageSize }
      },
      { silentError: background }
    );
    if (seq !== loadSeq) return;
    const total = rsp.page_result?.total ?? rsp.executions.length;
    // 按状态筛选时，取消、确认可能让最后一页仅有的一行移出筛选结果：回到现在的最后一页再读一次。
    const page = pageAfterShrink(pagination.current, pagination.pageSize, total, rsp.executions.length);
    if (page !== null) {
      pagination.current = page;
      void load(background);
      return;
    }
    rows.value = rsp.executions;
    pagination.total = total;
  } catch (error) {
    if (seq === loadSeq && !background) reportControlError(error);
  } finally {
    if (seq === loadSeq) loading.value = false;
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

/** 筛选和打开的记录都在 URL 里：刷新浏览器、从告警跳进来都能恢复。 */
function writeQuery() {
  const query = executionQuery({ ...filters, execution: drawerVisible.value ? detailId.value : "" });
  void router.replace({ path: route.path, query });
}

/** 按 URL 恢复；筛选变了才重读列表（自己写回 URL 时不重复读）。 */
function applyQuery() {
  const next = parseExecutionQuery(route.query);
  const filtersChanged = next.rule_id !== filters.rule_id || next.status !== filters.status;
  filters.rule_id = next.rule_id;
  filters.status = next.status;
  if (next.execution) detailId.value = next.execution;
  drawerVisible.value = Boolean(next.execution);
  if (filtersChanged) {
    pagination.current = 1;
    void load();
  }
}

watch(
  () => route.query,
  () => {
    if (route.path === "/events/executions") applyQuery();
  }
);

function onFilterChange() {
  pagination.current = 1;
  void load();
  writeQuery();
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
  writeQuery();
}

function onDrawerVisible(visible: boolean) {
  drawerVisible.value = visible;
  writeQuery();
}

/** 系统动作的“重新执行”会再次禁止买入并平仓，先确认；其余操作直接执行（取消在列表里已有气泡确认）。 */
function requestAct(kind: "retry" | "cancel" | "ack", item: EventExecution) {
  if (kind !== "retry" || item.action_type !== "ACTION_TYPE_SYSTEM") {
    void act(kind, item);
    return;
  }
  Modal.confirm({
    title: `重新执行「${item.subject_id}」的下架处理？`,
    content: systemRetryConfirmText(item),
    okText: "重新执行",
    okButtonProps: { status: "danger" },
    cancelText: "不执行",
    onOk: () => act(kind, item)
  });
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
  } catch (error) {
    reportControlError(error);
  } finally {
    acting.value = "";
    // 不管成败，列表和抽屉里的详情都按最新状态重读：取消可能已生效而善后失败，失败时列表上的状态也已经变了。
    refreshToken.value += 1;
    void load();
  }
}

onMounted(() => {
  const initial = parseExecutionQuery(route.query);
  filters.rule_id = initial.rule_id;
  filters.status = initial.status;
  detailId.value = initial.execution;
  drawerVisible.value = Boolean(initial.execution);
  void loadOptions();
  void load();
});

// 每 30 秒自动刷新；打开详情时暂停，免得列表在背后跳动。
usePolling(
  () => load(true),
  30_000,
  () => !drawerVisible.value
);
</script>

<style scoped>
.page-head {
  display: flex;
  align-items: baseline;
  gap: 12px;
  margin-bottom: 8px;
}

.page-head h2 {
  margin: 0;
  font-size: 20px;
  font-weight: 600;
}

.muted {
  color: var(--color-text-3);
  font-size: 12px;
}

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
