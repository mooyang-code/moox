<template>
  <section>
    <a-alert v-if="!systemActionEnabled" type="warning" class="system-action-alert">
      {{ SYSTEM_ACTION_DISABLED_TEXT }}
    </a-alert>
    <a-table
      row-key="rule_id"
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
        <a-table-column title="名称" :width="200">
          <template #cell="{ record }">
            <span>{{ record.name }}</span>
            <span v-if="record.system" class="system-mark">系统</span>
          </template>
        </a-table-column>
        <a-table-column title="触发事件" :width="140">
          <template #cell="{ record }">{{ eventTitle(events, record.event_name) }}</template>
        </a-table-column>
        <a-table-column title="限定范围" :width="220" :ellipsis="true" :tooltip="true">
          <template #cell="{ record }">{{ describeScope(record.scope, fieldsOf(record.event_name)) }}</template>
        </a-table-column>
        <a-table-column title="动作" :width="280" :ellipsis="true" :tooltip="true">
          <template #cell="{ record }">{{ actionText(record, systemActionEnabled) }}</template>
        </a-table-column>
        <a-table-column title="启用" :width="80" align="center">
          <template #cell="{ record }">
            <a-switch
              size="small"
              :model-value="record.enabled"
              :loading="toggling === record.rule_id"
              @change="(value: boolean | string | number) => toggle(record, Boolean(value))"
            />
          </template>
        </a-table-column>
        <a-table-column title="最近一次执行" :width="190">
          <template #cell="{ record }">
            <template v-if="record.last_execution">
              <span :class="['status', `status-${statusTone(record.last_execution.status)}`]">
                {{ statusText(record.last_execution.status) }}
              </span>
              <span class="muted">{{ formatMs(record.last_execution.at_ms) }}</span>
            </template>
            <span v-else class="muted">还没有执行过</span>
          </template>
        </a-table-column>
        <a-table-column title="操作" :width="200" align="center" fixed="right">
          <template #cell="{ record }">
            <a-space v-if="!record.system">
              <a-button size="mini" type="text" :loading="testing === record.rule_id" @click="sendTest(record)">
                发一条测试
              </a-button>
              <a-button size="mini" type="text" @click="openEdit(record)">编辑</a-button>
              <a-popconfirm content="确认删除这条规则？已有的执行记录会保留。" @ok="remove(record)">
                <a-button size="mini" type="text" status="danger">删除</a-button>
              </a-popconfirm>
            </a-space>
            <span v-else class="muted">只能启用或停用</span>
          </template>
        </a-table-column>
      </template>
    </a-table>

    <RuleDialog v-model:visible="ruleVisible" :rule="editingRule" :events="events" @saved="onSaved" />
    <SecretDialog v-model:visible="secretVisible" :rule-name="createdName" :secret="createdSecret" />
    <DelistDialog v-model:visible="delistVisible" :system-action-enabled="systemActionEnabled" />
  </section>
</template>

<script setup lang="ts">
import { onMounted, reactive, ref } from "vue";
import { Message, Modal } from "@arco-design/web-vue";
import {
  deleteEventRule,
  listEventRules,
  listSubscribableEvents,
  testEventRule,
  updateEventRule,
  type EventRule,
  type SubscribableEvent
} from "@/api/callback-dispatcher";
import { reportControlError } from "@/api/admin/http";
import { defaultPagination } from "@/views/data/shared/metadata-utils";
import RuleDialog from "./rule-dialog.vue";
import SecretDialog from "./secret-dialog.vue";
import DelistDialog from "./delist-dialog.vue";
import {
  actionText,
  describeScope,
  eventTitle,
  formatDuration,
  formatMs,
  statusText,
  statusTone,
  SYSTEM_ACTION_DISABLED_TEXT
} from "./event-center-utils";

defineOptions({ name: "EventCenterRules" });

const rows = ref<EventRule[]>([]);
const events = ref<SubscribableEvent[]>([]);
// 目录没加载出来时按已启用处理：不能因为一次加载失败就对用户说“尚未启用”。
const systemActionEnabled = ref(true);
const loading = ref(false);
const pagination = reactive(defaultPagination());
const toggling = ref("");
const testing = ref("");

const ruleVisible = ref(false);
const editingRule = ref<EventRule | null>(null);
const secretVisible = ref(false);
const createdName = ref("");
const createdSecret = ref("");
const delistVisible = ref(false);

const fieldsOf = (eventName: string) => events.value.find(event => event.event_name === eventName)?.scope_fields ?? [];

async function load() {
  loading.value = true;
  try {
    const rsp = await listEventRules({ page: pagination.current, size: pagination.pageSize });
    rows.value = rsp.rules;
    pagination.total = rsp.page_result?.total ?? rsp.rules.length;
  } catch (error) {
    reportControlError(error);
  } finally {
    loading.value = false;
  }
}

async function loadEvents() {
  try {
    const catalog = await listSubscribableEvents();
    events.value = catalog.events;
    systemActionEnabled.value = catalog.availableSystemActions.includes("SYSTEM_ACTION_SELL_DELISTED_HOLDINGS");
  } catch (error) {
    reportControlError(error);
  }
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

function openCreate() {
  editingRule.value = null;
  ruleVisible.value = true;
}

function openEdit(rule: EventRule) {
  editingRule.value = rule;
  ruleVisible.value = true;
}

function openDelist() {
  delistVisible.value = true;
}

async function onSaved(payload: { rule: EventRule; signingSecret: string }) {
  if (payload.signingSecret) {
    createdName.value = payload.rule.name;
    createdSecret.value = payload.signingSecret;
    secretVisible.value = true;
  }
  await load();
}

async function toggle(rule: EventRule, enabled: boolean) {
  toggling.value = rule.rule_id;
  try {
    await updateEventRule({ rule_id: rule.rule_id, enabled });
    Message.success(enabled ? "已启用" : "已停用");
    await load();
  } catch (error) {
    reportControlError(error);
  } finally {
    toggling.value = "";
  }
}

async function remove(rule: EventRule) {
  try {
    await deleteEventRule(rule.rule_id);
    Message.success("规则已删除");
    await load();
  } catch (error) {
    reportControlError(error);
  }
}

async function sendTest(rule: EventRule) {
  testing.value = rule.rule_id;
  try {
    const result = await testEventRule(rule.rule_id);
    const status = result.http_status ? `对方返回 ${result.http_status}` : "没有连上对方";
    Modal.info({
      title: result.success ? "测试通过" : "测试没有通过",
      content: `${status}，用时 ${formatDuration(result.duration_ms)}${result.response_snippet ? `。${result.response_snippet}` : ""}`
    });
  } catch (error) {
    reportControlError(error);
  } finally {
    testing.value = "";
  }
}

onMounted(() => {
  void loadEvents();
  void load();
});

defineExpose({ openCreate, openDelist, load });
</script>

<style scoped>
.system-action-alert {
  margin-bottom: 12px;
}

.system-mark {
  margin-left: 8px;
  color: var(--color-text-3);
  font-size: 12px;
}

.muted {
  color: var(--color-text-3);
}

.status + .muted {
  margin-left: 8px;
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
