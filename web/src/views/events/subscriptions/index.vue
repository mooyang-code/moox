<template>
  <div class="subscriptions-tab">
    <div class="tab-toolbar">
      <span class="tab-intro">某类事件发生时，向你自己的接口发一条通知，或者由系统自动处理；每次触发都会留下执行记录。</span>
      <a-space>
        <a-link @click="guideVisible = true">接收说明</a-link>
        <a-button type="primary" status="success" @click="openCreate()">
          <template #icon><icon-plus /></template>
          新建订阅
        </a-button>
      </a-space>
    </div>

    <a-alert v-if="systemActionNotice(systemActionEnabled)" type="warning" class="system-action-alert">
      {{ systemActionNotice(systemActionEnabled) }}
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
            <a-tooltip v-if="record.system" :content="SYSTEM_RULE_TIP">
              <span class="system-mark">系统</span>
            </a-tooltip>
          </template>
        </a-table-column>
        <a-table-column title="触发事件" :width="140">
          <template #cell="{ record }">
            <a-link v-if="eventOf(record.event_name)" @click="infoEvent = eventOf(record.event_name)">
              {{ eventTitle(events, record.event_name) }}
            </a-link>
            <template v-else>{{ eventTitle(events, record.event_name) }}</template>
          </template>
        </a-table-column>
        <a-table-column title="限定范围" :width="220" :ellipsis="true" :tooltip="true">
          <template #cell="{ record }">{{ describeScope(record.scope, fieldsOf(record.event_name)) }}</template>
        </a-table-column>
        <a-table-column title="动作" :width="280" :ellipsis="true" :tooltip="true">
          <template #cell="{ record }">
            {{ actionText(record) }}<span v-if="secretHintText(record)" class="muted">（{{ secretHintText(record) }}）</span>
          </template>
        </a-table-column>
        <a-table-column title="启用" :width="80" align="center">
          <template #cell="{ record }">
            <a-switch
              size="small"
              :model-value="record.enabled"
              :loading="toggling === record.rule_id"
              @change="(value: boolean | string | number) => requestToggle(record, Boolean(value))"
            />
          </template>
        </a-table-column>
        <a-table-column title="最近一次执行" :width="240">
          <template #cell="{ record }">
            <a-link v-if="record.last_execution" :href="executionsHref(record.rule_id)">
              <span :class="['status', `status-${statusTone(record.last_execution.status)}`]">
                {{ statusText(record.last_execution.status) }}
              </span>
              <span class="muted">{{ formatInstant(record.last_execution.at_ms, "UTC") }}</span>
            </a-link>
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
              <a-popconfirm content="确认删除这条订阅？已结束的执行记录会保留，还没结束的会被取消。" @ok="remove(record)">
                <a-button size="mini" type="text" status="danger">删除</a-button>
              </a-popconfirm>
            </a-space>
          </template>
        </a-table-column>
      </template>
    </a-table>

    <RuleDialog v-model:visible="ruleVisible" :rule="editingRule" :events="events" :preset-event="presetEvent" @saved="onSaved" />
    <SecretDialog v-model:visible="secretVisible" :rule-name="createdName" :secret="createdSecret" />
    <a-drawer
      :visible="Boolean(infoEvent)"
      :title="infoEvent?.title"
      width="min(640px, 100vw)"
      :footer="false"
      @cancel="infoEvent = null"
    >
      <EventInfo v-if="infoEvent" :event="infoEvent" />
    </a-drawer>
    <a-drawer v-model:visible="guideVisible" title="接收说明" width="min(760px, 100vw)" :footer="false">
      <ReceiverGuide full />
    </a-drawer>
  </div>
</template>

<script setup lang="ts">
// 事件中心和空间无关，时间都按 UTC 显示
import { formatInstant } from "@/utils/time-zone";
import { reactive, ref, watch } from "vue";
import { onShown } from "@/hooks/useOnShown";
import { useRoute, useRouter } from "vue-router";
import { Message, Modal } from "@arco-design/web-vue";
import {
  deleteEventRule,
  listEventRules,
  testEventRule,
  updateEventRule,
  type EventRule,
  type SubscribableEvent
} from "@/api/callback-dispatcher";
import { reportControlError } from "@/api/admin/http";
import { defaultPagination } from "@/views/data/shared/metadata-utils";
import RuleDialog from "./rule-dialog.vue";
import SecretDialog from "./secret-dialog.vue";
import EventInfo from "../shared/event-info.vue";
import ReceiverGuide from "../shared/receiver-guide.vue";
import { useEventCatalog } from "../shared/use-event-catalog";
import {
  SYSTEM_RULE_DISABLE_TEXT,
  SYSTEM_RULE_TIP,
  actionText,
  executionsHref,
  describeScope,
  eventTitle,
  formatDuration,
  pageAfterShrink,
  secretHintText,
  statusText,
  statusTone,
  systemActionNotice
} from "../shared/event-center-utils";

defineOptions({ name: "EventSubscriptions" });

const route = useRoute();
const router = useRouter();

const rows = ref<EventRule[]>([]);
const { events, systemActionEnabled, loadEvents } = useEventCatalog();
const loading = ref(false);
const pagination = reactive(defaultPagination());
const toggling = ref("");
const testing = ref("");

const ruleVisible = ref(false);
const editingRule = ref<EventRule | null>(null);
const secretVisible = ref(false);
const createdName = ref("");
const createdSecret = ref("");
const guideVisible = ref(false);
// 正在看说明的事件（列表里点事件名打开）。
const infoEvent = ref<SubscribableEvent | null>(null);

// 密钥弹窗关掉后就把密钥从页面状态里清掉，不在内存里留着。
watch(secretVisible, visible => {
  if (!visible) {
    createdName.value = "";
    createdSecret.value = "";
  }
});

const eventOf = (eventName: string) => events.value.find(event => event.event_name === eventName) ?? null;
const fieldsOf = (eventName: string) => eventOf(eventName)?.scope_fields ?? [];

// 列表请求的序号：翻页、删除后重读等连续请求只认最后一次，迟到的旧页不会覆盖新页。
let loadSeq = 0;
async function load() {
  const seq = ++loadSeq;
  loading.value = true;
  try {
    const rsp = await listEventRules({ page: pagination.current, size: pagination.pageSize });
    if (seq !== loadSeq) return;
    const total = rsp.page_result?.total ?? rsp.rules.length;
    // 删掉最后一页仅有的一行后，本页读回来是空的：回到现在的最后一页再读一次。
    const page = pageAfterShrink(pagination.current, pagination.pageSize, total, rsp.rules.length);
    if (page !== null) {
      pagination.current = page;
      void load();
      return;
    }
    rows.value = rsp.rules;
    pagination.total = total;
  } catch (error) {
    if (seq === loadSeq) reportControlError(error);
  } finally {
    if (seq === loadSeq) loading.value = false;
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

// 新建时预选的触发事件（从事件页签点“订阅”进来时带 ?new=事件名）
const presetEvent = ref("");

function openCreate(eventName = "") {
  editingRule.value = null;
  presetEvent.value = eventName;
  ruleVisible.value = true;
  if (!events.value.length) void loadEvents();
}

// 事件页签的“订阅”按钮：带着 ?new= 切过来，打开新建并预选这个事件，随后把 new 从地址里去掉
watch(
  () => route.query.new,
  value => {
    if (typeof value !== "string" || !value || route.query.tab !== "subscriptions") return;
    openCreate(value);
    void router.replace({ path: route.path, query: { tab: "subscriptions" } });
  },
  { immediate: true }
);

function openEdit(rule: EventRule) {
  editingRule.value = rule;
  ruleVisible.value = true;
  if (!events.value.length) void loadEvents();
}

async function onSaved(payload: { rule: EventRule; signingSecret: string }) {
  if (payload.signingSecret) {
    createdName.value = payload.rule.name;
    createdSecret.value = payload.signingSecret;
    secretVisible.value = true;
  }
  await load();
}

/** 停用系统规则之后发现的下架都不会再处理，先说明后果再停用；其余启停直接生效。 */
function requestToggle(rule: EventRule, enabled: boolean) {
  if (!rule.system || enabled) {
    void toggle(rule, enabled);
    return;
  }
  Modal.confirm({
    title: `停用系统订阅「${rule.name}」？`,
    content: SYSTEM_RULE_DISABLE_TEXT,
    okText: "停用",
    okButtonProps: { status: "danger" },
    cancelText: "不停用",
    onOk: () => toggle(rule, false)
  });
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
    Message.success("订阅已删除");
    await load();
  } catch (error) {
    reportControlError(error);
  }
}

async function sendTest(rule: EventRule) {
  testing.value = rule.rule_id;
  try {
    const result = await testEventRule(rule.rule_id);
    const status = result.http_status ? `对方返回 ${result.http_status}` : "没有收到对方的响应";
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

// 每次切回都重读列表：执行记录页签里重新执行、取消之后，“最近一次执行”要跟上
onShown(() => {
  if (!events.value.length) void loadEvents();
  void load();
});
</script>

<style scoped>
.tab-toolbar {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  justify-content: space-between;
  gap: var(--moox-space-3);
  margin-bottom: var(--moox-space-toolbar-table);
}

.tab-intro {
  color: var(--color-text-3);
  font-size: 13px;
}

.system-action-alert {
  margin-bottom: 12px;
}

.system-mark {
  margin-left: 8px;
  white-space: nowrap;
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
