<template>
  <section>
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
        <a-table-column title="最近一次执行" :width="190">
          <template #cell="{ record }">
            <template v-if="record.last_execution">
              <span :class="['status', `status-${statusTone(record.last_execution.status)}`]">
                {{ statusText(record.last_execution.status) }}
              </span>
              <span class="muted">{{ formatInstant(record.last_execution.at_ms, "UTC") }}</span>
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
              <a-popconfirm content="确认删除这条规则？已结束的执行记录会保留，还没结束的会被取消。" @ok="remove(record)">
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
    <DelistDialog
      v-model:visible="delistVisible"
      :system-action-enabled="systemActionEnabled"
      :system-rule-enabled="systemRuleEnabled"
    />
  </section>
</template>

<script setup lang="ts">
// 事件中心和空间无关，时间都按 UTC 显示
import { formatInstant } from "@/utils/time-zone";
import { computed, onActivated, onMounted, reactive, ref, watch } from "vue";
import { Message, Modal } from "@arco-design/web-vue";
import {
  deleteEventRule,
  listAllEventRules,
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
  SYSTEM_RULE_DISABLE_TEXT,
  actionText,
  describeScope,
  eventTitle,
  formatDuration,
  pageAfterShrink,
  secretHintText,
  statusText,
  statusTone,
  systemActionNotice
} from "./event-center-utils";

defineOptions({ name: "EventCenterRules" });

const rows = ref<EventRule[]>([]);
const events = ref<SubscribableEvent[]>([]);
// true 已启用；null 是还在加载或加载失败（不知道），不能当成已启用。
const systemActionEnabled = ref<boolean | null>(null);
// 系统规则当前是否启用：停用只影响新的下架，已经在处理的照常处理。从全部规则里找，不只看当前页；没读到时是 null。
const systemRule = ref<EventRule | null>(null);
const systemRuleEnabled = computed(() => systemRule.value?.enabled ?? null);
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

// 密钥弹窗关掉后就把密钥从页面状态里清掉，不在内存里留着。
watch(secretVisible, visible => {
  if (!visible) {
    createdName.value = "";
    createdSecret.value = "";
  }
});

const fieldsOf = (eventName: string) => events.value.find(event => event.event_name === eventName)?.scope_fields ?? [];

// 列表请求的序号：翻页、删除后重读等连续请求只认最后一次，迟到的旧页不会覆盖新页。
let loadSeq = 0;
async function load() {
  const seq = ++loadSeq;
  loading.value = true;
  void loadSystemRule();
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

let systemRuleSeq = 0;
async function loadSystemRule() {
  const seq = ++systemRuleSeq;
  try {
    const rules = await listAllEventRules();
    if (seq === systemRuleSeq) systemRule.value = rules.find(rule => rule.system) ?? null;
  } catch {
    // 列表本身的错误由 load 提示；这里读不到就当作不知道。
    if (seq === systemRuleSeq) systemRule.value = null;
  }
}

async function loadEvents() {
  try {
    const catalog = await listSubscribableEvents();
    events.value = catalog.events;
    systemActionEnabled.value = catalog.availableSystemActions.includes("SYSTEM_ACTION_SELL_DELISTED_HOLDINGS") ? true : null;
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
  if (!events.value.length) void loadEvents();
}

function openEdit(rule: EventRule) {
  editingRule.value = rule;
  ruleVisible.value = true;
  if (!events.value.length) void loadEvents();
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

/** 停用系统规则之后发现的下架都不会再处理，先说明后果再停用；其余启停直接生效。 */
function requestToggle(rule: EventRule, enabled: boolean) {
  if (!rule.system || enabled) {
    void toggle(rule, enabled);
    return;
  }
  Modal.confirm({
    title: `停用系统规则「${rule.name}」？`,
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

/** 页面上的“刷新”：规则列表、事件目录和系统规则状态一起重读。 */
function refresh() {
  void loadEvents();
  void load();
}

onMounted(refresh);
// 页签切回来时（keep-alive）执行记录页可能已经让规则的最近一次执行变了，重读列表；首次显示时 onMounted 已经读过。
let activatedOnce = false;
onActivated(() => {
  if (activatedOnce) refresh();
  activatedOnce = true;
});

defineExpose({ openCreate, openDelist, load, refresh });
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
