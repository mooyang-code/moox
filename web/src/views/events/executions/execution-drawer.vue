<template>
  <a-drawer
    :visible="visible"
    width="min(760px, 100vw)"
    title="处理详情"
    :footer="false"
    unmount-on-close
    @update:visible="(value: boolean) => emit('update:visible', value)"
  >
    <a-spin :loading="loading" class="drawer-spin">
      <template v-if="detail">
        <a-descriptions :column="2" size="small" bordered>
          <a-descriptions-item label="订阅">{{ execution.rule_name }}</a-descriptions-item>
          <a-descriptions-item label="状态">
            <span :class="['status', `status-${executionStatusTone(execution)}`]">{{ executionStatusText(execution) }}</span>
          </a-descriptions-item>
          <a-descriptions-item label="事件">{{ eventTitle(events, execution.event_name) }}</a-descriptions-item>
          <a-descriptions-item label="对象">{{ execution.subject_id }}</a-descriptions-item>
          <a-descriptions-item label="事件发生时间">{{ formatInstant(execution.occurred_at_ms, "UTC") }}</a-descriptions-item>
          <a-descriptions-item label="记录创建时间">{{ formatInstant(execution.created_at_ms, "UTC") }}</a-descriptions-item>
          <a-descriptions-item label="尝试次数">{{ execution.attempts }}</a-descriptions-item>
          <a-descriptions-item label="下一次处理">{{ formatInstant(execution.next_run_at_ms, "UTC") }}</a-descriptions-item>
          <a-descriptions-item label="结束时间">{{ formatInstant(execution.finished_at_ms, "UTC") }}</a-descriptions-item>
          <a-descriptions-item label="耗时">{{ formatDuration(execution.duration_ms) }}</a-descriptions-item>
          <a-descriptions-item v-if="execution.status === 'EXECUTION_STATUS_FAILED'" label="确认处理" :span="2">
            {{ execution.acknowledged ? "已确认处理，不再告警" : "还没确认，会一直告警" }}
          </a-descriptions-item>
          <a-descriptions-item label="事件编号" :span="2">{{ execution.event_id }}</a-descriptions-item>
          <a-descriptions-item v-if="execution.last_error" label="失败原因" :span="2">
            {{ execution.last_error }}
          </a-descriptions-item>
        </a-descriptions>

        <a-space class="drawer-actions">
          <a-button v-if="canRetry(execution)" size="small" :disabled="busy" @click="emitAction('retry')">重新执行</a-button>
          <a-popconfirm v-if="canCancel(execution)" :content="cancelConfirmText(execution)" @ok="emitAction('cancel')">
            <a-button size="small" status="danger" :disabled="busy">{{ cancelLabel(execution) }}</a-button>
          </a-popconfirm>
          <a-button v-if="canAcknowledge(execution)" size="small" :disabled="busy" @click="emitAction('ack')"
            >确认已处理</a-button
          >
        </a-space>

        <template v-if="execution.action_type === 'ACTION_TYPE_NOTIFY_URL'">
          <h4 class="section-title">每次尝试</h4>
          <a-table :data="detail.attempts" row-key="attempt" size="small" :pagination="false" :bordered="{ cell: true }">
            <template #columns>
              <a-table-column title="第几次" data-index="attempt" :width="80" />
              <a-table-column title="时间" :width="200">
                <template #cell="{ record }">{{ formatInstant(record.at_ms, "UTC") }}</template>
              </a-table-column>
              <a-table-column title="状态码" :width="90">
                <template #cell="{ record }">{{ record.http_status || "没有连上" }}</template>
              </a-table-column>
              <a-table-column title="耗时" :width="100">
                <template #cell="{ record }">{{ formatDuration(record.duration_ms) }}</template>
              </a-table-column>
              <a-table-column title="对方返回 / 失败原因" :ellipsis="true" :tooltip="true">
                <template #cell="{ record }">{{ record.error || record.response_snippet || "-" }}</template>
              </a-table-column>
            </template>
          </a-table>
        </template>

        <template v-else>
          <h4 class="section-title">下架处理的各步</h4>
          <a-table
            :data="detail.delist_steps"
            :row-key="stepKey"
            size="small"
            :pagination="false"
            :bordered="{ cell: true }"
            :scroll="{ x: 'max-content' }"
          >
            <template #columns>
              <a-table-column title="阶段" :width="150">
                <template #cell="{ record }">{{ roundText(record.round) }}</template>
              </a-table-column>
              <a-table-column title="动作" :width="90">
                <template #cell="{ record }">{{ stepKindText(record.kind) }}</template>
              </a-table-column>
              <a-table-column title="受影响的组合" :width="220" :ellipsis="true" :tooltip="true">
                <template #cell="{ record }">
                  <a-link v-if="record.logical_account_id" @click="openAccount(record)">
                    {{ record.space_id }} / {{ record.logical_account_id }}
                  </a-link>
                  <template v-else>-</template>
                </template>
              </a-table-column>
              <a-table-column title="状态" :width="110">
                <template #cell="{ record }">{{ stepStatusText(record.status) }}</template>
              </a-table-column>
              <a-table-column title="次数" data-index="attempts" :width="70" />
              <a-table-column title="交易操作号" :width="200" :ellipsis="true" :tooltip="true">
                <template #cell="{ record }">{{ record.operator_action_id || "-" }}</template>
              </a-table-column>
              <a-table-column title="结果 / 零头 / 失败原因" :width="260" :ellipsis="true" :tooltip="true">
                <template #cell="{ record }">{{ record.error || record.result || "-" }}</template>
              </a-table-column>
              <a-table-column title="更新时间" :width="200">
                <template #cell="{ record }">{{ formatInstant(record.updated_at_ms, "UTC") }}</template>
              </a-table-column>
            </template>
          </a-table>
        </template>

        <h4 class="section-title">事件内容</h4>
        <CodeBlock :code="payloadText" language="json" />
      </template>
    </a-spin>
  </a-drawer>
</template>

<script setup lang="ts">
// 事件中心和空间无关，时间都按 UTC 显示
import { formatInstant } from "@/utils/time-zone";
import { computed, ref, watch } from "vue";
import { useRouter } from "vue-router";
import { useSpaceStore } from "@/store/modules/space";
import {
  getEventExecution,
  type DelistStep,
  type EventExecution,
  type EventExecutionDetail,
  type SubscribableEvent
} from "@/api/callback-dispatcher";
import { reportControlError } from "@/api/admin/http";
import CodeBlock from "@/components/code-block/index.vue";
import {
  canAcknowledge,
  canCancel,
  cancelConfirmText,
  cancelLabel,
  canRetry,
  eventTitle,
  executionStatusText,
  executionStatusTone,
  formatDuration,
  prettyPayload,
  roundText,
  stepStatusText,
  stepKindText
} from "../shared/event-center-utils";

defineOptions({ name: "EventExecutionDrawer" });

const props = defineProps<{
  visible: boolean;
  executionId: string;
  events: SubscribableEvent[];
  refreshToken: number;
  /** 父页面正在做某个操作：做完之前按钮不能点。 */
  busy?: boolean;
}>();
const emit = defineEmits<{
  "update:visible": [value: boolean];
  action: [kind: "retry" | "cancel" | "ack", item: EventExecution];
}>();

const router = useRouter();
const spaceStore = useSpaceStore();

const detail = ref<EventExecutionDetail | null>(null);
const loading = ref(false);

const execution = computed(() => detail.value?.execution as EventExecution);
const payloadText = computed(() => prettyPayload(detail.value?.payload_json ?? ""));
const stepKey = (step: DelistStep) => `${step.round}/${step.kind}/${step.space_id}/${step.logical_account_id}`;

// 每次读取编一个序号，只有最新一次的结果能写进来：换了记录或关掉抽屉后，迟到的响应直接丢弃。
let loadSeq = 0;

async function load() {
  const seq = ++loadSeq;
  const id = props.executionId;
  loading.value = true;
  try {
    const result = await getEventExecution(id);
    if (seq === loadSeq) detail.value = result;
  } catch (error) {
    if (seq !== loadSeq) return;
    detail.value = null;
    reportControlError(error);
  } finally {
    if (seq === loadSeq) loading.value = false;
  }
}

/** 组合账户属于某个空间：先切到那个空间再打开账户详情。 */
function openAccount(step: DelistStep) {
  spaceStore.setSelectedSpace(step.space_id);
  void router.push(`/trading/accounts/${encodeURIComponent(step.logical_account_id)}`);
}

function emitAction(kind: "retry" | "cancel" | "ack") {
  emit("action", kind, execution.value);
}

// 父页面的操作完成之后（refreshToken 加一）才重新读取详情，不靠定时器猜时间。
watch(
  () => props.refreshToken,
  () => {
    if (props.visible && props.executionId) void load();
  }
);

// 换记录或重新打开时先清掉上一条的详情，不让旧内容和新记录的按钮混在一起。
watch(
  () => [props.visible, props.executionId] as const,
  ([visible, id]) => {
    loadSeq++;
    detail.value = null;
    loading.value = false;
    if (visible && id) void load();
  }
);
</script>

<style scoped>
.drawer-spin {
  display: block;
}

.drawer-actions {
  margin: 12px 0 0;
}

.section-title {
  margin: 20px 0 8px;
  font-size: 14px;
  font-weight: 600;
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
