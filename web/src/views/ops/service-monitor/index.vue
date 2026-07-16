<template>
  <div class="monitor-page">
    <div class="page-head">
      <div v-if="!embedded">
        <h2>服务监控</h2>
        <span>HTTP/TCP 可用性、告警与多实例协同状态。</span>
      </div>
      <a-space wrap>
        <a-button @click="syncSystemChecks" :loading="syncing">
          <template #icon><icon-sync /></template>
          同步系统服务
        </a-button>
        <a-button @click="openPeerDrawer">
          <template #icon><icon-apps /></template>
          监控实例
        </a-button>
        <a-button @click="openWebhookDrawer">
          <template #icon><icon-settings /></template>
          Webhook
        </a-button>
        <a-button type="primary" status="success" @click="openCreateCheck">
          <template #icon><icon-plus /></template>
          新增探测
        </a-button>
      </a-space>
    </div>

    <div class="status-grid">
      <section class="status-hero" :class="`tone-${overallStatus}`">
        <span class="status-label">当前状态</span>
        <strong>{{ overallStatusText }}</strong>
        <em>{{ overview.total_checks || 0 }} 个探测 · {{ overview.firing_alerts || 0 }} 个告警</em>
      </section>
      <section class="metric-card">
        <span>健康</span>
        <strong class="metric-ok">{{ overview.healthy_checks || 0 }}</strong>
      </section>
      <section class="metric-card">
        <span>降级</span>
        <strong class="metric-warn">{{ overview.degraded_checks || 0 }}</strong>
      </section>
      <section class="metric-card">
        <span>故障</span>
        <strong class="metric-down">{{ overview.down_checks || 0 }}</strong>
      </section>
      <section class="metric-card">
        <span>24h 成功率</span>
        <strong>{{ successRateText }}</strong>
      </section>
      <section class="metric-card">
        <span>P95 延迟</span>
        <strong>{{ overview.p95_latency_ms || 0 }}ms</strong>
      </section>
    </div>

    <div class="group-band" v-if="(overview.groups || []).length > 0">
      <section v-for="group in overview.groups" :key="group.group_name || 'default'" class="group-card">
        <div class="group-card-head">
          <strong>{{ group.group_name || 'default' }}</strong>
          <a-tag size="small" :color="groupToneColor(group)">{{ groupToneText(group) }}</a-tag>
        </div>
        <div class="group-card-body">
          <span>{{ group.total_checks || 0 }} checks</span>
          <span class="metric-ok">{{ group.healthy_checks || 0 }} ok</span>
          <span class="metric-warn">{{ group.degraded_checks || 0 }} degraded</span>
          <span class="metric-down">{{ group.down_checks || 0 }} down</span>
        </div>
      </section>
    </div>

    <div class="failing-band" v-if="failingChecks.length > 0">
      <div class="band-title">当前异常</div>
      <div class="failing-list">
        <button v-for="check in failingChecks" :key="checkKey(check)" class="failing-item" type="button" @click="openDetail(check)">
          <span>
            <strong>{{ check.name }}</strong>
            <em>{{ targetOf(check) }}</em>
          </span>
          <a-tag size="small" :color="statusColorOf(check)">{{ statusTextOf(check) }}</a-tag>
        </button>
      </div>
    </div>

    <section class="monitor-panel">
      <div class="panel-head">
        <a-space wrap>
          <a-input v-model="filters.group_name" allow-clear placeholder="分组" @press-enter="reloadFirstPage" />
          <a-select v-model="filters.source" allow-clear placeholder="来源" style="width: 140px" @change="reloadFirstPage">
            <a-option value="sysdeploy">sysdeploy</a-option>
            <a-option value="manual">manual</a-option>
          </a-select>
          <a-button @click="reloadFirstPage">查询</a-button>
        </a-space>
        <span class="polling-note">overview 15s · table 30s</span>
      </div>

      <a-table
        row-key="check_id"
        size="small"
        :bordered="{ cell: true }"
        :loading="loading"
        :data="checks"
        :pagination="pagination"
        :scroll="{ x: 'max-content' }"
        @page-change="onPageChange"
        @page-size-change="onPageSizeChange"
      >
        <template #columns>
          <a-table-column title="名称" :width="180">
            <template #cell="{ record }">
              <div class="name-cell">
                <strong>{{ record.name }}</strong>
                <span>{{ record.check_id }}</span>
              </div>
            </template>
          </a-table-column>
          <a-table-column title="分组" data-index="group_name" :width="130" />
          <a-table-column title="类型" :width="90">
            <template #cell="{ record }">
              <a-tag size="small" color="blue">{{ kindLabel(record.kind) }}</a-tag>
            </template>
          </a-table-column>
          <a-table-column title="目标" :width="260" :ellipsis="true" :tooltip="true">
            <template #cell="{ record }">{{ targetOf(record) }}</template>
          </a-table-column>
          <a-table-column title="状态" :width="100">
            <template #cell="{ record }">
              <a-tag size="small" :color="statusColorOf(record)">{{ statusTextOf(record) }}</a-tag>
            </template>
          </a-table-column>
          <a-table-column title="延迟" :width="90">
            <template #cell="{ record }">{{ latencyOf(record) }}</template>
          </a-table-column>
          <a-table-column title="最近探测" :width="180">
            <template #cell="{ record }">{{ latestCheckedAt(record) }}</template>
          </a-table-column>
          <a-table-column title="来源" :width="110">
            <template #cell="{ record }">
              <a-tag size="small" :color="record.source === 'sysdeploy' ? 'arcoblue' : 'gray'">{{ record.source || 'manual' }}</a-tag>
            </template>
          </a-table-column>
          <a-table-column title="启用" :width="90" align="center">
            <template #cell="{ record }">
              <a-switch :model-value="record.enabled" size="small" @change="toggleCheck(record)" />
            </template>
          </a-table-column>
          <a-table-column title="操作" :width="260" align="center" :fixed="'right'">
            <template #cell="{ record }">
              <a-space>
                <a-button size="mini" type="text" @click="runOnce(record)">运行</a-button>
                <a-button size="mini" type="text" @click="openDetail(record)">详情</a-button>
                <a-button size="mini" type="text" @click="openRuleDrawer(record)">告警</a-button>
                <a-button size="mini" type="text" @click="openEditCheck(record)">编辑</a-button>
                <a-popconfirm content="确认删除该探测？" @ok="deleteCheck(record)">
                  <a-button size="mini" type="text" status="danger">删除</a-button>
                </a-popconfirm>
              </a-space>
            </template>
          </a-table-column>
        </template>
      </a-table>
    </section>

    <a-drawer
      v-model:visible="checkDrawerVisible"
      width="780px"
      :title="checkDrawerTitle"
      :footer="false"
      :body-style="{ padding: '18px 24px 20px' }"
      unmount-on-close
    >
      <a-form class="monitor-form" :model="checkForm" layout="vertical">
        <a-form-item field="kind" label="探测类型">
          <a-radio-group v-model="checkForm.kind" type="button">
            <a-radio :value="CHECK_KIND_HTTP">HTTP</a-radio>
            <a-radio :value="CHECK_KIND_TCP">TCP</a-radio>
          </a-radio-group>
        </a-form-item>
        <a-form-item field="name" label="名称" required>
          <a-input v-model="checkForm.name" placeholder="api-health" />
        </a-form-item>
        <a-form-item field="group_name" label="分组">
          <a-input v-model="checkForm.group_name" placeholder="moox-system" />
        </a-form-item>
        <a-form-item v-if="isHttpForm" class="form-span-2" field="url" label="URL" required>
          <a-input v-model="checkForm.url" placeholder="https://example.com/healthz" />
        </a-form-item>
        <a-form-item v-if="isHttpForm" field="method" label="Method">
          <a-select v-model="checkForm.method">
            <a-option value="GET">GET</a-option>
            <a-option value="POST">POST</a-option>
            <a-option value="PUT">PUT</a-option>
            <a-option value="HEAD">HEAD</a-option>
          </a-select>
        </a-form-item>
        <a-form-item v-if="isHttpForm" field="expected_status" label="期望状态码">
          <a-input v-model="checkForm.expected_status" placeholder="200-299" />
        </a-form-item>
        <a-form-item v-if="isTcpForm" field="tcp_host" label="TCP Host" required>
          <a-input v-model="checkForm.tcp_host" placeholder="127.0.0.1" />
        </a-form-item>
        <a-form-item v-if="isTcpForm" field="tcp_port" label="TCP Port" required>
          <a-input-number v-model="checkForm.tcp_port" :min="1" :max="65535" />
        </a-form-item>
        <a-form-item field="interval_seconds" label="间隔秒">
          <a-input-number v-model="checkForm.interval_seconds" :min="5" />
        </a-form-item>
        <a-form-item field="timeout_ms" label="超时毫秒">
          <a-input-number v-model="checkForm.timeout_ms" :min="100" :max="60000" />
        </a-form-item>
        <a-form-item field="max_response_ms" label="最大响应毫秒">
          <a-input-number v-model="checkForm.max_response_ms" :min="0" />
        </a-form-item>
        <a-form-item v-if="isHttpForm" field="body_contains" label="正文包含">
          <a-input v-model="checkForm.body_contains" allow-clear />
        </a-form-item>
        <a-form-item v-if="isHttpForm" class="form-span-2" field="headers_json" label="Headers JSON">
          <a-textarea v-model="checkForm.headers_json" :auto-size="{ minRows: 3, maxRows: 6 }" />
        </a-form-item>
        <a-form-item v-if="isHttpForm" class="form-span-2" field="body" label="Body">
          <a-textarea v-model="checkForm.body" :auto-size="{ minRows: 2, maxRows: 5 }" />
        </a-form-item>
        <a-form-item class="form-span-2" field="labels_json" label="Labels JSON">
          <a-textarea v-model="checkForm.labels_json" :auto-size="{ minRows: 2, maxRows: 5 }" />
        </a-form-item>
        <a-form-item class="form-span-2" field="description" label="说明">
          <a-textarea v-model="checkForm.description" :auto-size="{ minRows: 2, maxRows: 4 }" />
        </a-form-item>
        <a-form-item field="enabled" label="启用">
          <a-switch v-model="checkForm.enabled" />
        </a-form-item>
      </a-form>
      <div class="drawer-actions">
        <a-button @click="checkDrawerVisible = false">取消</a-button>
        <a-button type="primary" @click="submitCheck">保存</a-button>
      </div>
    </a-drawer>

    <a-drawer
      v-model:visible="detailDrawerVisible"
      width="980px"
      class="detail-drawer"
      :title="detailTitle"
      :footer="false"
      :body-style="{ padding: 0 }"
      unmount-on-close
    >
      <div class="detail-drawer-content">
      <a-tabs default-active-key="results" type="rounded" class="detail-tabs">
        <a-tab-pane key="results" title="结果">
          <a-descriptions :column="2" bordered size="small" v-if="selectedCheck">
            <a-descriptions-item label="类型">{{ kindLabel(selectedCheck.kind) }}</a-descriptions-item>
            <a-descriptions-item label="目标">{{ targetOf(selectedCheck) }}</a-descriptions-item>
            <a-descriptions-item label="间隔">{{ selectedCheck.interval_seconds || 60 }}s</a-descriptions-item>
            <a-descriptions-item label="超时">{{ selectedCheck.timeout_ms || 3000 }}ms</a-descriptions-item>
            <a-descriptions-item label="状态码">{{ selectedCheck.expected_status || '-' }}</a-descriptions-item>
            <a-descriptions-item label="正文包含">{{ selectedCheck.body_contains || '-' }}</a-descriptions-item>
          </a-descriptions>
          <a-table class="detail-table" size="small" row-key="result_id" :pagination="false" :data="detailResults" :scroll="{ x: 'max-content', y: detailTableHeight }">
            <template #columns>
              <a-table-column title="时间" :width="190">
                <template #cell="{ record }">{{ formatTime(record.checked_at) }}</template>
              </a-table-column>
              <a-table-column title="状态" :width="100">
                <template #cell="{ record }">
                  <a-tag size="small" :color="resultStatusColor(record.status)">{{ resultStatusText(record.status) }}</a-tag>
                </template>
              </a-table-column>
              <a-table-column title="HTTP" data-index="http_status" :width="90" />
              <a-table-column title="Connected" :width="110">
                <template #cell="{ record }">{{ record.connected ? 'yes' : '-' }}</template>
              </a-table-column>
              <a-table-column title="延迟" :width="90">
                <template #cell="{ record }">{{ record.latency_ms || 0 }}ms</template>
              </a-table-column>
              <a-table-column title="错误" data-index="error_message" :width="260" :ellipsis="true" :tooltip="true" />
              <a-table-column title="实例" data-index="instance_id" :width="140" />
            </template>
          </a-table>
        </a-tab-pane>
        <a-tab-pane key="alerts" title="告警">
          <a-space class="tab-actions">
            <a-button type="primary" status="success" @click="openRuleDrawer(selectedCheck)">
              <template #icon><icon-plus /></template>
              新增规则
            </a-button>
          </a-space>
          <a-table size="small" row-key="event_id" :pagination="false" :data="detailEvents" :scroll="{ x: 'max-content', y: detailTableHeight }">
            <template #columns>
              <a-table-column title="时间" :width="190">
                <template #cell="{ record }">{{ formatTime(record.created_at) }}</template>
              </a-table-column>
              <a-table-column title="类型" :width="120">
                <template #cell="{ record }">{{ eventTypeText(record.event_type) }}</template>
              </a-table-column>
              <a-table-column title="状态" :width="120">
                <template #cell="{ record }">
                  <a-tag size="small" :color="alertStatusColor(record.status)">{{ alertStatusText(record.status) }}</a-tag>
                </template>
              </a-table-column>
              <a-table-column title="Owner" data-index="owner_instance_id" :width="150" />
              <a-table-column title="消息" data-index="message" :width="360" :ellipsis="true" :tooltip="true" />
            </template>
          </a-table>
        </a-tab-pane>
        <a-tab-pane key="peers" title="实例">
          <a-table size="small" row-key="instance_id" :pagination="false" :data="instances" :scroll="{ x: 'max-content', y: detailTableHeight }">
            <template #columns>
              <a-table-column title="实例" data-index="instance_id" :width="180" />
              <a-table-column title="Base URL" data-index="base_url" :width="260" />
              <a-table-column title="状态" :width="110">
                <template #cell="{ record }">
                  <a-tag size="small" :color="instanceColor(record.status)">{{ record.status || '-' }}</a-tag>
                </template>
              </a-table-column>
              <a-table-column title="最近心跳" :width="190">
                <template #cell="{ record }">{{ formatTime(record.last_seen_at) }}</template>
              </a-table-column>
              <a-table-column title="本机" :width="90">
                <template #cell="{ record }">{{ record.is_local ? 'yes' : '-' }}</template>
              </a-table-column>
            </template>
          </a-table>
        </a-tab-pane>
      </a-tabs>
      </div>
    </a-drawer>

    <a-drawer
      v-model:visible="webhookDrawerVisible"
      width="900px"
      title="Webhook 通道"
      :footer="false"
      :body-style="{ padding: '18px 24px 20px' }"
      unmount-on-close
    >
      <div class="drawer-toolbar">
        <a-radio-group v-model="webhookScope" type="button" @change="loadWebhooks">
          <a-radio value="system">系统</a-radio>
          <a-radio value="space">当前空间</a-radio>
        </a-radio-group>
        <a-button type="primary" status="success" @click="openCreateWebhook">
          <template #icon><icon-plus /></template>
          新增通道
        </a-button>
      </div>
      <a-table size="small" row-key="webhook_id" :pagination="false" :data="webhooks">
        <template #columns>
          <a-table-column title="名称" data-index="name" :width="170" />
          <a-table-column title="Method" data-index="method" :width="100" />
          <a-table-column title="URL" data-index="url" :width="300" :ellipsis="true" :tooltip="true" />
          <a-table-column title="启用" :width="90">
            <template #cell="{ record }">{{ record.enabled ? 'yes' : 'no' }}</template>
          </a-table-column>
          <a-table-column title="操作" :width="150" align="center">
            <template #cell="{ record }">
              <a-space>
                <a-button size="mini" type="text" @click="openEditWebhook(record)">编辑</a-button>
                <a-popconfirm content="确认删除该 Webhook？" @ok="deleteWebhook(record)">
                  <a-button size="mini" type="text" status="danger">删除</a-button>
                </a-popconfirm>
              </a-space>
            </template>
          </a-table-column>
        </template>
      </a-table>
      <a-form v-if="webhookFormVisible" class="monitor-form inline-form" :model="webhookForm" layout="vertical">
        <a-form-item field="name" label="名称" required>
          <a-input v-model="webhookForm.name" />
        </a-form-item>
        <a-form-item field="method" label="Method">
          <a-select v-model="webhookForm.method">
            <a-option value="POST">POST</a-option>
            <a-option value="PUT">PUT</a-option>
          </a-select>
        </a-form-item>
        <a-form-item class="form-span-2" field="url" label="URL" required>
          <a-input v-model="webhookForm.url" />
        </a-form-item>
        <a-form-item class="form-span-2" field="headers_json" label="Headers JSON">
          <a-textarea v-model="webhookForm.headers_json" :auto-size="{ minRows: 2, maxRows: 5 }" />
        </a-form-item>
        <a-form-item class="form-span-2" field="body_template" label="Body Template">
          <a-textarea v-model="webhookForm.body_template" :auto-size="{ minRows: 4, maxRows: 8 }" />
        </a-form-item>
        <a-form-item field="enabled" label="启用">
          <a-switch v-model="webhookForm.enabled" />
        </a-form-item>
        <div class="form-span-2 drawer-actions compact-actions">
          <a-button @click="webhookFormVisible = false">取消</a-button>
          <a-button type="primary" @click="submitWebhook">保存</a-button>
        </div>
      </a-form>
    </a-drawer>

    <a-drawer
      v-model:visible="ruleDrawerVisible"
      width="920px"
      :title="ruleDrawerTitle"
      :footer="false"
      :body-style="{ padding: '18px 24px 20px' }"
      unmount-on-close
    >
      <div class="drawer-toolbar">
        <a-button type="primary" status="success" @click="openCreateRule">
          <template #icon><icon-plus /></template>
          新增规则
        </a-button>
      </div>
      <a-table size="small" row-key="rule_id" :pagination="false" :data="rules">
        <template #columns>
          <a-table-column title="Webhook" :width="180">
            <template #cell="{ record }">{{ webhookName(record.webhook_id) }}</template>
          </a-table-column>
          <a-table-column title="失败阈值" data-index="failure_threshold" :width="100" />
          <a-table-column title="恢复阈值" data-index="success_threshold" :width="100" />
          <a-table-column title="提醒间隔" :width="110">
            <template #cell="{ record }">{{ record.minimum_reminder_interval_seconds || 0 }}s</template>
          </a-table-column>
          <a-table-column title="恢复通知" :width="100">
            <template #cell="{ record }">{{ record.send_on_resolved ? 'yes' : 'no' }}</template>
          </a-table-column>
          <a-table-column title="启用" :width="80">
            <template #cell="{ record }">{{ record.enabled ? 'yes' : 'no' }}</template>
          </a-table-column>
          <a-table-column title="操作" :width="150" align="center">
            <template #cell="{ record }">
              <a-space>
                <a-button size="mini" type="text" @click="openEditRule(record)">编辑</a-button>
                <a-popconfirm content="确认删除该规则？" @ok="deleteRule(record)">
                  <a-button size="mini" type="text" status="danger">删除</a-button>
                </a-popconfirm>
              </a-space>
            </template>
          </a-table-column>
        </template>
      </a-table>
      <a-form v-if="ruleFormVisible" class="monitor-form inline-form" :model="ruleForm" layout="vertical">
        <a-form-item field="webhook_id" label="Webhook" required>
          <a-select v-model="ruleForm.webhook_id" placeholder="选择通道">
            <a-option v-for="item in webhooks" :key="item.webhook_id" :value="item.webhook_id">{{ item.name }}</a-option>
          </a-select>
        </a-form-item>
        <a-form-item field="failure_threshold" label="失败阈值">
          <a-input-number v-model="ruleForm.failure_threshold" :min="1" />
        </a-form-item>
        <a-form-item field="success_threshold" label="恢复阈值">
          <a-input-number v-model="ruleForm.success_threshold" :min="1" />
        </a-form-item>
        <a-form-item field="minimum_reminder_interval_seconds" label="提醒间隔秒">
          <a-input-number v-model="ruleForm.minimum_reminder_interval_seconds" :min="0" />
        </a-form-item>
        <a-form-item field="send_on_resolved" label="恢复通知">
          <a-switch v-model="ruleForm.send_on_resolved" />
        </a-form-item>
        <a-form-item field="enabled" label="启用">
          <a-switch v-model="ruleForm.enabled" />
        </a-form-item>
        <a-form-item class="form-span-2" field="description" label="说明">
          <a-textarea v-model="ruleForm.description" :auto-size="{ minRows: 2, maxRows: 4 }" />
        </a-form-item>
        <div class="form-span-2 drawer-actions compact-actions">
          <a-button @click="ruleFormVisible = false">取消</a-button>
          <a-button type="primary" @click="submitRule">保存</a-button>
        </div>
      </a-form>
    </a-drawer>

    <a-drawer
      v-model:visible="peerDrawerVisible"
      width="860px"
      title="监控实例"
      :footer="false"
      :body-style="{ padding: '18px 24px 20px' }"
      unmount-on-close
    >
      <a-table size="small" row-key="instance_id" :pagination="false" :data="instances">
        <template #columns>
          <a-table-column title="实例" data-index="instance_id" :width="180" />
          <a-table-column title="Base URL" data-index="base_url" :width="280" />
          <a-table-column title="状态" :width="100">
            <template #cell="{ record }">
              <a-tag size="small" :color="instanceColor(record.status)">{{ record.status || '-' }}</a-tag>
            </template>
          </a-table-column>
          <a-table-column title="最近心跳" :width="190">
            <template #cell="{ record }">{{ formatTime(record.last_seen_at) }}</template>
          </a-table-column>
          <a-table-column title="本机" :width="80">
            <template #cell="{ record }">{{ record.is_local ? 'yes' : '-' }}</template>
          </a-table-column>
        </template>
      </a-table>
    </a-drawer>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, onUnmounted, reactive, ref } from 'vue';
import { Message } from '@arco-design/web-vue';
import { monitorApi } from '@/api/monitor';
import type {
  AlertEvent,
  AlertRule,
  CheckKind,
  CheckResult,
  CheckStatus,
  GroupSummary,
  MonitorCheck,
  MonitorInstance,
  MonitorOverview,
  WebhookChannel,
} from '@/api/monitor';
import { useSpaceStore } from '@/store/modules/space';
import { applyPageResult, defaultPagination, formatTime, jsonText } from '@/views/data/shared/metadata-utils';

defineOptions({ name: 'OpsServiceMonitor' });
const props = defineProps<{ embedded?: boolean }>();
const embedded = computed(() => props.embedded === true);

const CHECK_KIND_HTTP = 1;
const CHECK_KIND_TCP = 2;

type StatusTone = 'ok' | 'degraded' | 'down' | 'pending';
type WebhookScope = 'system' | 'space';

const spaceStore = useSpaceStore();
const selectedSpaceId = computed(() => spaceStore.selectedSpaceId);
const loading = ref(false);
const syncing = ref(false);
const overview = ref<MonitorOverview>({});
const checks = ref<MonitorCheck[]>([]);
const latestResults = ref<Record<string, CheckResult>>({});
const webhooks = ref<WebhookChannel[]>([]);
const rules = ref<AlertRule[]>([]);
const instances = ref<MonitorInstance[]>([]);
const detailResults = ref<CheckResult[]>([]);
const detailEvents = ref<AlertEvent[]>([]);
const selectedCheck = ref<MonitorCheck | null>(null);
const pagination = reactive(defaultPagination());
const filters = reactive({ group_name: '', source: '' });

const checkDrawerVisible = ref(false);
const detailDrawerVisible = ref(false);
const webhookDrawerVisible = ref(false);
const webhookFormVisible = ref(false);
const ruleDrawerVisible = ref(false);
const ruleFormVisible = ref(false);
const peerDrawerVisible = ref(false);
const editingCheck = ref(false);
const editingWebhook = ref(false);
const editingRule = ref(false);
const webhookScope = ref<WebhookScope>('system');

const checkForm = reactive<MonitorCheck>({
  kind: CHECK_KIND_HTTP,
  name: '',
  group_name: '',
  url: '',
  method: 'GET',
  headers_json: '{}',
  body: '',
  tcp_host: '',
  tcp_port: 443,
  interval_seconds: 60,
  timeout_ms: 3000,
  expected_status: '200-299',
  max_response_ms: 0,
  body_contains: '',
  enabled: true,
  source: 'manual',
  labels_json: '{}',
  description: '',
});

const webhookForm = reactive<WebhookChannel>({
  name: '',
  url: '',
  method: 'POST',
  headers_json: '{}',
  body_template: '{"check_id":"{{check_id}}","status":"{{status}}","event_type":"{{event_type}}","dedupe_key":"{{dedupe_key}}"}',
  enabled: true,
});

const ruleForm = reactive<AlertRule>({
  check_id: '',
  webhook_id: '',
  failure_threshold: 3,
  success_threshold: 2,
  minimum_reminder_interval_seconds: 1800,
  send_on_resolved: true,
  enabled: true,
  description: '',
});

let overviewTimer: ReturnType<typeof setInterval> | undefined;
let tableTimer: ReturnType<typeof setInterval> | undefined;

const activeWebhookSpaceId = computed(() => (webhookScope.value === 'space' ? selectedSpaceId.value : ''));
const overallStatus = computed<StatusTone>(() => {
  if (!overview.value.total_checks) return 'pending';
  if ((overview.value.down_checks || 0) > 0 || (overview.value.firing_alerts || 0) > 0) return 'down';
  if ((overview.value.degraded_checks || 0) > 0) return 'degraded';
  return 'ok';
});
const overallStatusText = computed(() => {
  if (overallStatus.value === 'ok') return 'Healthy';
  if (overallStatus.value === 'degraded') return 'Degraded';
  if (overallStatus.value === 'down') return 'Down';
  return 'Pending';
});
const successRateText = computed(() => `${((overview.value.success_rate_24h || 0) * 100).toFixed(1)}%`);
const failingChecks = computed(() => checks.value.filter((item) => ['down', 'degraded'].includes(statusToneOf(item))).slice(0, 6));
const checkDrawerTitle = computed(() => (editingCheck.value ? '编辑探测' : '新增探测'));
const detailTitle = computed(() => (selectedCheck.value ? `探测详情：${selectedCheck.value.name || selectedCheck.value.check_id}` : '探测详情'));
const detailTableHeight = computed(() => Math.max(220, (typeof window === 'undefined' ? 900 : window.innerHeight) - 430));
const ruleDrawerTitle = computed(() => (selectedCheck.value ? `告警规则：${selectedCheck.value.name || selectedCheck.value.check_id}` : '告警规则'));
const isHttpForm = computed(() => isHttpKind(checkForm.kind));
const isTcpForm = computed(() => isTcpKind(checkForm.kind));
const pollingPaused = computed(() => checkDrawerVisible.value || webhookFormVisible.value || ruleFormVisible.value);

async function refreshAll() {
  await Promise.all([loadOverview(), loadChecks(), loadInstances()]);
}

async function loadOverview() {
  const rsp = await monitorApi.getOverview();
  overview.value = rsp.overview || {};
}

async function loadChecks() {
  loading.value = true;
  try {
    const rsp = await monitorApi.listChecks({
      group_name: filters.group_name || undefined,
      source: filters.source || undefined,
      page: { page: pagination.current, size: pagination.pageSize },
    });
    checks.value = rsp.checks || [];
    applyPageResult(pagination, { total: rsp.page_result?.total || checks.value.length });
    await loadLatestResults(checks.value);
  } finally {
    loading.value = false;
  }
}

async function loadLatestResults(rows: MonitorCheck[]) {
  const pairs = await Promise.all(
    rows.map(async (check) => {
      if (!check.check_id) return null;
      try {
        const rsp = await monitorApi.listResults({ space_id: check.space_id, check_id: check.check_id, limit: 1 });
        return [checkKey(check), rsp.results?.[0]] as const;
      } catch {
        return null;
      }
    }),
  );
  const next: Record<string, CheckResult> = {};
  pairs.forEach((pair) => {
    if (pair?.[1]) next[pair[0]] = pair[1];
  });
  latestResults.value = next;
}

async function loadWebhooks() {
  const rsp = await monitorApi.listWebhookChannels({ space_id: activeWebhookSpaceId.value });
  webhooks.value = rsp.channels || [];
}

async function loadRules(check: MonitorCheck | null = selectedCheck.value) {
  if (!check?.check_id) {
    rules.value = [];
    return;
  }
  const rsp = await monitorApi.listAlertRules({ space_id: check.space_id, check_id: check.check_id });
  rules.value = rsp.rules || [];
}

async function loadInstances() {
  const rsp = await monitorApi.listMonitorInstances();
  instances.value = rsp.instances || [];
}

async function loadDetail(check: MonitorCheck) {
  selectedCheck.value = check;
  const [resultsRsp, eventsRsp] = await Promise.all([
    monitorApi.listResults({ space_id: check.space_id, check_id: check.check_id || '', limit: 30 }),
    monitorApi.listAlertEvents({ space_id: check.space_id, limit: 80 }),
    loadInstances(),
    loadRules(check),
  ]);
  detailResults.value = resultsRsp.results || [];
  detailEvents.value = (eventsRsp.events || []).filter((event) => event.check_id === check.check_id);
}

function reloadFirstPage() {
  pagination.current = 1;
  void loadChecks();
}

function onPageChange(page: number) {
  pagination.current = page;
  void loadChecks();
}

function onPageSizeChange(pageSize: number) {
  pagination.current = 1;
  pagination.pageSize = pageSize;
  void loadChecks();
}

function resetCheckForm() {
  Object.assign(checkForm, {
    space_id: selectedSpaceId.value,
    check_id: '',
    kind: CHECK_KIND_HTTP,
    name: '',
    group_name: '',
    url: '',
    method: 'GET',
    headers_json: '{}',
    body: '',
    tcp_host: '',
    tcp_port: 443,
    interval_seconds: 60,
    timeout_ms: 3000,
    expected_status: '200-299',
    max_response_ms: 0,
    body_contains: '',
    enabled: true,
    source: 'manual',
    labels_json: '{}',
    description: '',
  });
}

function openCreateCheck() {
  if (!selectedSpaceId.value) {
    Message.warning('请先选择空间');
    return;
  }
  editingCheck.value = false;
  resetCheckForm();
  checkDrawerVisible.value = true;
}

function openEditCheck(record: MonitorCheck) {
  editingCheck.value = true;
  Object.assign(checkForm, {
    ...record,
    kind: normalizeKind(record.kind),
    headers_json: jsonText(record.headers_json),
    labels_json: jsonText(record.labels_json),
    method: record.method || 'GET',
    expected_status: record.expected_status || '200-299',
    interval_seconds: record.interval_seconds || 60,
    timeout_ms: record.timeout_ms || 3000,
    tcp_port: record.tcp_port || 443,
  });
  checkDrawerVisible.value = true;
}

async function submitCheck() {
  const payload = prepareCheckPayload();
  if (!payload) return;
  if (editingCheck.value) await monitorApi.updateCheck(payload);
  else await monitorApi.createCheck(payload);
  Message.success('探测已保存');
  checkDrawerVisible.value = false;
  await refreshAll();
}

function prepareCheckPayload() {
  if (!checkForm.name?.trim()) {
    Message.warning('请填写名称');
    return null;
  }
  if (isHttpForm.value && !checkForm.url?.trim()) {
    Message.warning('请填写 HTTP URL');
    return null;
  }
  if (isTcpForm.value && (!checkForm.tcp_host?.trim() || !checkForm.tcp_port)) {
    Message.warning('请填写 TCP Host 和 Port');
    return null;
  }
  const headers = normalizeJson(checkForm.headers_json, '{}');
  const labels = normalizeJson(checkForm.labels_json, '{}');
  if (headers === null || labels === null) return null;
  return {
    ...checkForm,
    kind: normalizeKind(checkForm.kind),
    name: checkForm.name.trim(),
    group_name: checkForm.group_name?.trim(),
    url: isHttpForm.value ? checkForm.url?.trim() : '',
    method: isHttpForm.value ? checkForm.method || 'GET' : '',
    headers_json: isHttpForm.value ? headers : '{}',
    body: isHttpForm.value ? checkForm.body || '' : '',
    tcp_host: isTcpForm.value ? checkForm.tcp_host?.trim() : '',
    tcp_port: isTcpForm.value ? checkForm.tcp_port : 0,
    expected_status: isHttpForm.value ? checkForm.expected_status || '200-299' : '',
    labels_json: labels,
    source: checkForm.source || 'manual',
  };
}

async function runOnce(record: MonitorCheck) {
  if (!record.check_id) return;
  const rsp = await monitorApi.runCheckOnce({ space_id: record.space_id, check_id: record.check_id });
  if (rsp.result) latestResults.value = { ...latestResults.value, [checkKey(record)]: rsp.result };
  Message.success('探测已完成');
  await loadOverview();
}

async function toggleCheck(record: MonitorCheck) {
  await monitorApi.updateCheck({ ...record, enabled: !record.enabled });
  Message.success(record.enabled ? '探测已停用' : '探测已启用');
  await refreshAll();
}

async function deleteCheck(record: MonitorCheck) {
  if (!record.check_id) return;
  await monitorApi.deleteCheck({ space_id: record.space_id, check_id: record.check_id });
  Message.success('探测已删除');
  await refreshAll();
}

async function syncSystemChecks() {
  syncing.value = true;
  try {
    const rsp = await monitorApi.syncSystemChecks();
    Message.success(`已同步 ${rsp.synced || 0} 个系统探测`);
    await refreshAll();
  } finally {
    syncing.value = false;
  }
}

async function openDetail(record: MonitorCheck) {
  detailDrawerVisible.value = true;
  await loadDetail(record);
}

async function openWebhookDrawer() {
  webhookDrawerVisible.value = true;
  webhookFormVisible.value = false;
  await loadWebhooks();
}

function resetWebhookForm() {
  Object.assign(webhookForm, {
    space_id: activeWebhookSpaceId.value,
    webhook_id: '',
    name: '',
    url: '',
    method: 'POST',
    headers_json: '{}',
    body_template: '{"check_id":"{{check_id}}","status":"{{status}}","event_type":"{{event_type}}","dedupe_key":"{{dedupe_key}}"}',
    enabled: true,
  });
}

function openCreateWebhook() {
  if (webhookScope.value === 'space' && !selectedSpaceId.value) {
    Message.warning('请先选择空间');
    return;
  }
  editingWebhook.value = false;
  resetWebhookForm();
  webhookFormVisible.value = true;
}

function openEditWebhook(record: WebhookChannel) {
  editingWebhook.value = true;
  Object.assign(webhookForm, {
    ...record,
    headers_json: jsonText(record.headers_json),
    body_template: record.body_template || '{}',
  });
  webhookFormVisible.value = true;
}

async function submitWebhook() {
  if (!webhookForm.name?.trim() || !webhookForm.url?.trim()) {
    Message.warning('请填写 Webhook 名称和 URL');
    return;
  }
  const headers = normalizeJson(webhookForm.headers_json, '{}');
  if (headers === null) return;
  const payload = { ...webhookForm, space_id: webhookForm.space_id || activeWebhookSpaceId.value, headers_json: headers };
  if (editingWebhook.value) await monitorApi.updateWebhookChannel(payload);
  else await monitorApi.createWebhookChannel(payload);
  Message.success('Webhook 已保存');
  webhookFormVisible.value = false;
  await loadWebhooks();
}

async function deleteWebhook(record: WebhookChannel) {
  if (!record.webhook_id) return;
  await monitorApi.deleteWebhookChannel({ space_id: record.space_id, webhook_id: record.webhook_id });
  Message.success('Webhook 已删除');
  await loadWebhooks();
}

async function openRuleDrawer(record: MonitorCheck | null) {
  if (!record?.check_id) return;
  selectedCheck.value = record;
  webhookScope.value = record.space_id ? 'space' : 'system';
  ruleDrawerVisible.value = true;
  ruleFormVisible.value = false;
  await Promise.all([loadWebhooks(), loadRules(record)]);
}

function resetRuleForm() {
  Object.assign(ruleForm, {
    space_id: selectedCheck.value?.space_id || '',
    rule_id: '',
    check_id: selectedCheck.value?.check_id || '',
    webhook_id: webhooks.value[0]?.webhook_id || '',
    failure_threshold: 3,
    success_threshold: 2,
    minimum_reminder_interval_seconds: 1800,
    send_on_resolved: true,
    enabled: true,
    description: '',
  });
}

function openCreateRule() {
  if (!selectedCheck.value) return;
  if (webhooks.value.length === 0) {
    Message.warning('请先创建 Webhook 通道');
    return;
  }
  editingRule.value = false;
  resetRuleForm();
  ruleFormVisible.value = true;
}

function openEditRule(record: AlertRule) {
  editingRule.value = true;
  Object.assign(ruleForm, { ...record });
  ruleFormVisible.value = true;
}

async function submitRule() {
  if (!selectedCheck.value) return;
  if (!ruleForm.webhook_id) {
    Message.warning('请选择 Webhook 通道');
    return;
  }
  const payload = {
    ...ruleForm,
    space_id: selectedCheck.value.space_id,
    check_id: selectedCheck.value.check_id,
  };
  if (editingRule.value) await monitorApi.updateAlertRule(payload);
  else await monitorApi.createAlertRule(payload);
  Message.success('告警规则已保存');
  ruleFormVisible.value = false;
  await loadRules(selectedCheck.value);
}

async function deleteRule(record: AlertRule) {
  if (!record.rule_id) return;
  await monitorApi.deleteAlertRule({ space_id: record.space_id, rule_id: record.rule_id });
  Message.success('告警规则已删除');
  await loadRules(selectedCheck.value);
}

async function openPeerDrawer() {
  peerDrawerVisible.value = true;
  await loadInstances();
}

function normalizeJson(value: string | undefined, fallback: string) {
  const raw = (value || fallback).trim() || fallback;
  try {
    JSON.parse(raw);
    return raw;
  } catch {
    Message.warning('JSON 格式不正确');
    return null;
  }
}

function checkKey(check: MonitorCheck) {
  return `${check.space_id || ''}:${check.check_id || ''}`;
}

function latestResult(check: MonitorCheck) {
  return latestResults.value[checkKey(check)];
}

function statusToneOf(check: MonitorCheck): StatusTone {
  const result = latestResult(check);
  if (!result) return 'pending';
  const status = normalizeStatus(result.status);
  if (status === 'ok') return 'ok';
  if (status === 'degraded') return 'degraded';
  return 'down';
}

function statusColorOf(check: MonitorCheck) {
  return toneColor(statusToneOf(check));
}

function statusTextOf(check: MonitorCheck) {
  const tone = statusToneOf(check);
  if (tone === 'ok') return 'OK';
  if (tone === 'degraded') return 'Degraded';
  if (tone === 'down') return 'Down';
  return 'Pending';
}

function latencyOf(check: MonitorCheck) {
  const result = latestResult(check);
  return result ? `${result.latency_ms || 0}ms` : '-';
}

function latestCheckedAt(check: MonitorCheck) {
  const result = latestResult(check);
  return formatTime(result?.checked_at || check.last_checked_at);
}

function resultStatusText(status?: CheckStatus) {
  const tone = normalizeStatus(status);
  if (tone === 'ok') return 'OK';
  if (tone === 'degraded') return 'Degraded';
  if (tone === 'down') return 'Down';
  return 'Pending';
}

function resultStatusColor(status?: CheckStatus) {
  return toneColor(normalizeStatus(status));
}

function normalizeStatus(status?: CheckStatus): StatusTone {
  const value = String(status || '').toLowerCase();
  if (status === 1 || value === 'check_status_ok' || value === 'ok') return 'ok';
  if (status === 2 || value === 'check_status_degraded' || value === 'degraded') return 'degraded';
  if (status === 3 || value === 'check_status_down' || value === 'down') return 'down';
  return 'pending';
}

function toneColor(tone: StatusTone) {
  if (tone === 'ok') return 'green';
  if (tone === 'degraded') return 'orange';
  if (tone === 'down') return 'red';
  return 'gray';
}

function groupToneText(group: GroupSummary) {
  if ((group.down_checks || 0) > 0) return 'Down';
  if ((group.degraded_checks || 0) > 0) return 'Degraded';
  if ((group.total_checks || 0) > 0) return 'Healthy';
  return 'Pending';
}

function groupToneColor(group: GroupSummary) {
  if ((group.down_checks || 0) > 0) return 'red';
  if ((group.degraded_checks || 0) > 0) return 'orange';
  if ((group.total_checks || 0) > 0) return 'green';
  return 'gray';
}

function normalizeKind(kind?: CheckKind) {
  if (isTcpKind(kind)) return CHECK_KIND_TCP;
  return CHECK_KIND_HTTP;
}

function isHttpKind(kind?: CheckKind) {
  return kind === CHECK_KIND_HTTP || kind === 'CHECK_KIND_HTTP' || String(kind || '').toLowerCase() === 'http';
}

function isTcpKind(kind?: CheckKind) {
  return kind === CHECK_KIND_TCP || kind === 'CHECK_KIND_TCP' || String(kind || '').toLowerCase() === 'tcp';
}

function kindLabel(kind?: CheckKind) {
  return isTcpKind(kind) ? 'TCP' : 'HTTP';
}

function targetOf(check: MonitorCheck) {
  if (isTcpKind(check.kind)) return `${check.tcp_host || '-'}:${check.tcp_port || '-'}`;
  return check.url || '-';
}

function alertStatusText(status?: string | number) {
  if (status === 2 || status === 'ALERT_STATUS_FIRING' || status === 'firing') return 'Firing';
  if (status === 3 || status === 'ALERT_STATUS_RESOLVED' || status === 'resolved') return 'Resolved';
  if (status === 1 || status === 'ALERT_STATUS_OK' || status === 'ok') return 'OK';
  return 'Pending';
}

function alertStatusColor(status?: string | number) {
  if (status === 2 || status === 'ALERT_STATUS_FIRING' || status === 'firing') return 'red';
  if (status === 3 || status === 'ALERT_STATUS_RESOLVED' || status === 'resolved') return 'green';
  if (status === 1 || status === 'ALERT_STATUS_OK' || status === 'ok') return 'green';
  return 'gray';
}

function eventTypeText(eventType?: string | number) {
  if (eventType === 1 || eventType === 'ALERT_EVENT_TYPE_TRIGGERED' || eventType === 'triggered') return 'Triggered';
  if (eventType === 2 || eventType === 'ALERT_EVENT_TYPE_REMINDER' || eventType === 'reminder') return 'Reminder';
  if (eventType === 3 || eventType === 'ALERT_EVENT_TYPE_RESOLVED' || eventType === 'resolved') return 'Resolved';
  return '-';
}

function instanceColor(status?: string) {
  if (status === 'active') return 'green';
  if (status === 'down') return 'red';
  return 'gray';
}

function webhookName(webhookID?: string) {
  return webhooks.value.find((item) => item.webhook_id === webhookID)?.name || webhookID || '-';
}

function startPolling() {
  overviewTimer = setInterval(() => {
    if (!pollingPaused.value) void loadOverview();
  }, 15000);
  tableTimer = setInterval(() => {
    if (!pollingPaused.value) void loadChecks();
  }, 30000);
}

onMounted(async () => {
  await refreshAll();
  startPolling();
});

onUnmounted(() => {
  if (overviewTimer) clearInterval(overviewTimer);
  if (tableTimer) clearInterval(tableTimer);
});
</script>

<style scoped lang="less">
.monitor-page {
  height: 100%;
  min-height: 100%;
  padding: 16px;
  background: #f5f7fb;
  overflow-y: auto;
}

.page-head {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 16px;
  margin-bottom: 8px;
}

.page-head h2 {
  margin: 0;
  font-size: 20px;
  font-weight: 600;
  color: #1d2129;
}

.page-head span {
  color: #86909c;
}

.status-grid {
  display: grid;
  grid-template-columns: minmax(220px, 1.5fr) repeat(5, minmax(130px, 1fr));
  gap: 12px;
  margin-bottom: 8px;
}

.status-hero,
.metric-card,
.group-card,
.monitor-panel {
  border: 1px solid #e5e6eb;
  border-radius: 8px;
  background: #fff;
  box-shadow: 0 8px 18px rgb(29 33 41 / 4%);
}

.status-hero {
  min-height: 118px;
  padding: 18px;
  display: flex;
  flex-direction: column;
  justify-content: center;
  border-left: 5px solid #c9cdd4;
}

.status-hero strong {
  display: block;
  margin: 8px 0 4px;
  font-size: 32px;
  line-height: 36px;
  color: #1d2129;
}

.status-hero em,
.status-label {
  font-style: normal;
  color: #86909c;
}

.status-hero.tone-ok {
  border-left-color: #00b42a;
}

.status-hero.tone-degraded {
  border-left-color: #ff7d00;
}

.status-hero.tone-down {
  border-left-color: #f53f3f;
}

.metric-card {
  min-height: 118px;
  padding: 16px;
  display: flex;
  flex-direction: column;
  justify-content: space-between;
}

.metric-card span {
  color: #86909c;
}

.metric-card strong {
  font-size: 28px;
  color: #1d2129;
}

.metric-ok {
  color: #00b42a !important;
}

.metric-warn {
  color: #ff7d00 !important;
}

.metric-down {
  color: #f53f3f !important;
}

.group-band {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(220px, 1fr));
  gap: 12px;
  margin-bottom: 8px;
}

.group-card {
  padding: 14px 16px;
}

.group-card-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  margin-bottom: 8px;
}

.group-card-body {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 8px;
  color: #4e5969;
}

.failing-band {
  margin-bottom: 8px;
}

.band-title {
  margin-bottom: 8px;
  color: #4e5969;
  font-weight: 600;
}

.failing-list {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(260px, 1fr));
  gap: 10px;
}

.failing-item {
  height: 76px;
  padding: 12px 14px;
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  border: 1px solid #f2b6b6;
  border-radius: 8px;
  background: #fff;
  color: #1d2129;
  cursor: pointer;
  text-align: left;
}

.failing-item span {
  min-width: 0;
}

.failing-item strong,
.name-cell strong {
  display: block;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.failing-item em,
.name-cell span,
.polling-note {
  display: block;
  overflow: hidden;
  color: #86909c;
  font-style: normal;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.monitor-panel {
  padding: 14px;
}

.panel-head,
.drawer-toolbar,
.tab-actions {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  margin-bottom: 8px;
}

.name-cell {
  min-width: 0;
}

.detail-table {
  margin-top: 8px;
}

.detail-drawer :deep(.arco-drawer-body) {
  display: flex;
  min-height: 0;
  overflow: hidden;
}

.detail-drawer-content {
  display: flex;
  min-height: 0;
  flex: 1;
  flex-direction: column;
  padding: 18px 24px 20px;
}

.detail-tabs {
  min-height: 0;
  flex: 1;
}

.detail-tabs :deep(.arco-tabs-content) {
  min-height: 0;
  height: calc(100% - 44px);
  overflow: hidden;
}

.detail-tabs :deep(.arco-tabs-content-list),
.detail-tabs :deep(.arco-tabs-pane) {
  height: 100%;
  min-height: 0;
}

.monitor-form {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  column-gap: 16px;
}

.form-span-2 {
  grid-column: span 2;
}

.drawer-actions {
  display: flex;
  justify-content: flex-end;
  gap: 10px;
  padding-top: 12px;
  border-top: 1px solid #e5e6eb;
}

.compact-actions {
  padding-top: 0;
  border-top: 0;
}

.inline-form {
  margin-top: 18px;
  padding: 14px;
  border: 1px solid #e5e6eb;
  border-radius: 8px;
  background: #fbfcff;
}

@media (max-width: 1180px) {
  .status-grid {
    grid-template-columns: repeat(3, minmax(0, 1fr));
  }

  .status-hero {
    grid-column: span 3;
  }
}

@media (max-width: 760px) {
  .monitor-page {
    padding: 12px;
  }

  .page-head,
  .panel-head,
  .drawer-toolbar {
    flex-direction: column;
    align-items: stretch;
  }

  .status-grid,
  .group-band,
  .failing-list,
  .monitor-form {
    grid-template-columns: 1fr;
  }

  .status-hero,
  .form-span-2 {
    grid-column: span 1;
  }
}
</style>
