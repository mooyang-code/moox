<template>
  <div class="moox-page health-monitor-page">
    <div class="moox-inner monitor">
      <header class="monitor-head">
        <div>
          <h2>监控告警</h2>
          <p class="muted">先看结论和进行中的告警；正常的部分自动收起</p>
        </div>
        <a-space>
          <span class="muted">告警推送：{{ notificationLabel }}</span>
          <a-button :loading="loading" @click="refresh">刷新</a-button>
          <a-button v-if="isAdmin" type="outline" @click="openNotification">消息通知设置</a-button>
        </a-space>
      </header>

      <section class="verdict" :class="`is-${conclusion.tone}`" role="status" aria-live="polite">
        <span class="verdict-icon"><component :is="toneIcon(conclusion.tone)" /></span>
        <div class="verdict-main">
          <h3>{{ conclusion.title }}</h3>
          <p v-if="conclusion.detail">{{ conclusion.detail }}</p>
          <p v-if="loaded && !overview.topology_known && groups.length">部署清单尚未完成同步，组件健康状态可能不完整</p>
        </div>
        <div class="verdict-meta">
          <span>{{ loadedAt ? `${relativeTime(loadedAt, clockMs)}更新` : "尚未读取" }}</span>
          <span>每 30 秒刷新</span>
        </div>
      </section>

      <section v-if="groups.length" class="health-section" aria-label="当前告警">
        <h3 class="section-title">当前告警<small>持续最久的在前；同一分钟内触发的合并为一组</small></h3>
        <div class="alert-list">
          <article v-for="group in groups" :key="group.key" class="tile alert-group">
            <header v-if="group.lines.length > 1" class="alert-group__head" :class="`is-${group.tone}`">
              <span>{{ group.lines.length }} 项同一时间触发，可能是同一个原因</span>
              <span class="duration">{{ group.duration }}</span>
            </header>
            <div v-for="line in group.lines" :key="line.key" class="alert-line" :class="`is-${line.tone}`">
              <button
                type="button"
                class="alert-line__main"
                :aria-expanded="expandedAlert === line.key"
                @click="expandedAlert = expandedAlert === line.key ? '' : line.key"
              >
                <span class="dot" />
                <span class="alert-line__title">{{ line.title }}</span>
                <span class="tag">{{ line.severity }}</span>
                <span v-if="line.recovering" class="tag tag--ok">已恢复，确认中</span>
                <span v-if="group.lines.length === 1" class="duration">{{ line.duration }}</span>
              </button>
              <p class="alert-line__reason">{{ line.reason }}</p>
              <div class="alert-line__foot">
                <span class="chip">{{ line.layer }}</span>
                <a class="locator" :href="line.link.href">{{ line.link.label }} →</a>
              </div>
              <div v-if="expandedAlert === line.key" class="alert-line__detail">
                <dl class="facts">
                  <template v-for="fact in line.facts" :key="fact.label">
                    <dt>{{ fact.label }}</dt>
                    <dd>{{ fact.value }}</dd>
                  </template>
                </dl>
                <RawError :value="line.rawError" />
              </div>
            </div>
          </article>
        </div>
      </section>

      <section v-if="loaded" class="health-section" aria-label="告警经过">
        <h3 class="section-title">
          最近 {{ timeline.windowHours }} 小时<small>{{ timelineSummary }}</small>
        </h3>
        <div class="tile timeline">
          <p v-if="!timeline.rows.length" class="empty">最近 {{ timeline.windowHours }} 小时没有告警</p>
          <div v-for="row in visibleIncidents" :key="row.key" class="incident" :class="`is-${row.tone}`">
            <span class="incident__time">{{ row.time }}</span>
            <span class="dot" />
            <span class="incident__title" :title="row.reason || row.title"
              >{{ row.title }}<em v-if="row.others.length"> + {{ othersText(row.others) }}</em></span
            >
            <span class="incident__flags">
              <span v-if="row.notifyFailed" class="chip chip--bad">推送失败</span>
              <span v-if="row.reminders" class="chip">提醒 {{ row.reminders }} 次</span>
            </span>
            <span class="incident__outcome" :class="{ 'is-active': row.active }"
              >{{ row.outcome }}<template v-if="row.flap"> · 闪断</template></span
            >
          </div>
          <button
            v-if="showAllIncidents || hiddenIncidents"
            type="button"
            class="link-button"
            @click="showAllIncidents = !showAllIncidents"
          >
            {{ showAllIncidents ? "收起" : hiddenIncidentsText }}
          </button>
        </div>
      </section>

      <section v-if="loaded" class="health-section" aria-label="分层健康">
        <h3 class="section-title">分层健康<small>只列出需要关注的项，点“展开”看全部</small></h3>
        <div class="layers">
          <article class="tile layer" aria-label="组件" :class="`is-${layers.components.tone}`">
            <header class="tile-head">
              <h4><span class="dot" />组件</h4>
              <span class="muted">{{ layerCountText(layers.components) }}</span>
            </header>
            <div class="host-rows">
              <div v-for="host in componentHosts" :key="host.hostId" class="host-row">
                <span class="host-row__name">{{ host.hostId }}</span>
                <span class="chips">
                  <button
                    v-for="chip in host.chips"
                    :key="chip.key"
                    type="button"
                    class="component-chip"
                    :class="`is-${chip.tone}`"
                    :title="`${chip.name} · ${chip.status}`"
                    :aria-label="`${chip.name}（${host.hostId}）：${chip.status}`"
                    @click="openComponent(chip.item)"
                  >
                    <span class="dot" /><span>{{ chip.name }}</span>
                  </button>
                </span>
              </div>
              <p v-if="!componentHosts.length" class="empty">暂无组件部署</p>
            </div>
            <ul v-if="componentIssues.length || unregistered.length" class="issues">
              <li v-for="chip in componentIssues" :key="chip.key" :class="`is-${chip.tone}`">
                <button type="button" @click="openComponent(chip.item)">
                  <span class="dot" />{{ chip.name }}（{{ chip.item.host_id }}）<em>{{ chip.reason || chip.status }}</em>
                </button>
              </li>
              <li v-for="item in unregistered" :key="`${item.host_id}/${item.component_id}`" class="is-warn">
                <button type="button" @click="openComponent(item)">
                  <span class="dot" />未登记进程 {{ item.name || item.component_id }}（{{ item.host_id }}）<em>{{
                    item.reason
                  }}</em>
                </button>
              </li>
            </ul>
          </article>

          <article class="tile layer" aria-label="主机" :class="`is-${layers.hosts.tone}`">
            <header class="tile-head">
              <h4><span class="dot" />主机</h4>
              <span class="muted">{{ layerCountText(layers.hosts) }}</span>
            </header>
            <div class="host-cards">
              <div v-for="host in hosts" :key="host.key" class="host-card" :class="`is-${host.tone}`">
                <div class="host-card__head">
                  <span class="dot" /><strong>{{ host.title }}</strong
                  ><span class="muted">{{ host.subtitle }}</span>
                  <a class="host-card__link" :href="host.href">主机监控 →</a>
                </div>
                <div class="meters">
                  <div v-for="item in host.meters" :key="item.label" class="meter-row" :class="`is-${item.tone}`">
                    <span class="meter-row__label">{{ item.label }}</span>
                    <span class="meter" :title="item.threshold ? `告警阈值 ${item.threshold}%` : '没有告警阈值'">
                      <i :style="{ width: `${Math.min(100, item.value ?? 0)}%` }" />
                      <b v-if="item.threshold" :style="{ left: `${item.threshold}%` }" />
                    </span>
                    <span class="meter-row__value">{{ item.value === null ? "暂无" : `${item.value.toFixed(1)}%` }}</span>
                    <span class="meter-row__capacity" :title="item.capacityTitle">{{ item.capacity }}</span>
                  </div>
                </div>
                <p v-if="host.tone !== 'ok'" class="host-card__reason">{{ host.reason }}</p>
                <p v-for="issue in host.gatewayIssues" :key="issue.label" class="host-card__reason">
                  {{ issue.label }}：{{ issue.value }}
                </p>
                <small class="muted">最后上报 {{ host.lastReported }}</small>
              </div>
              <p v-if="!hosts.length" class="empty">暂无主机</p>
            </div>
          </article>

          <article class="tile layer" aria-label="数据链路" :class="`is-${layers.pipeline.tone}`">
            <header class="tile-head">
              <h4><span class="dot" />数据链路</h4>
              <span class="muted">{{ layerCountText(layers.pipeline) }}</span>
            </header>
            <div class="stages">
              <template v-for="(stage, index) in stages" :key="stage.id">
                <icon-arrow-right v-if="index" class="stage-arrow" />
                <span class="stage" :class="`is-${stage.tone}`" :title="stage.note">
                  <span class="dot" />{{ stage.label }}
                  <em>{{ stage.total ? `${stage.healthy}/${stage.total}` : stage.note }}</em>
                </span>
              </template>
            </div>
            <div v-if="datasetGroups.length" class="table-scroll">
              <table class="dataset-table">
                <thead>
                  <tr>
                    <th>数据集</th>
                    <th>周期</th>
                    <th>最新K线</th>
                    <th>到达延迟</th>
                    <th>允许迟到</th>
                    <th>状态</th>
                  </tr>
                </thead>
                <tbody v-for="group in datasetGroups" :key="group.id" :aria-label="group.label">
                  <tr class="group-row">
                    <th colspan="6">
                      {{ group.label }}<small>{{ group.summary }}</small>
                    </th>
                  </tr>
                  <tr v-for="row in group.rows" :key="row.key" class="dataset-row" :class="`is-${row.tone}`">
                    <td>
                      <span class="dot" />{{ row.name }}<small>{{ row.spaceId }}</small>
                    </td>
                    <td>{{ row.freq }}</td>
                    <td>{{ row.latest }}</td>
                    <td class="lag" :class="`is-${row.delayTone}`" :title="row.delayTitle">{{ row.delay }}</td>
                    <td>{{ row.limit }}</td>
                    <td class="status-cell">{{ row.attention ? row.reason || row.status : row.status }}</td>
                  </tr>
                </tbody>
              </table>
            </div>
            <footer class="tile-foot">
              <span v-if="!datasetGroups.length" class="muted">{{ pipelineQuiet }}</span>
              <button
                v-if="allDatasetRows.length"
                type="button"
                class="link-button"
                @click="expanded.pipeline = !expanded.pipeline"
              >
                {{ expanded.pipeline ? "只看需要关注的" : `展开全部 ${allDatasetRows.length} 条` }}
              </button>
            </footer>
          </article>

          <article class="tile layer" aria-label="业务检查" :class="`is-${layers.business.tone}`">
            <header class="tile-head">
              <h4><span class="dot" />业务检查</h4>
              <span class="muted">{{ layerCountText(layers.business) }}</span>
            </header>
            <div class="kinds">
              <span v-for="kind in business" :key="kind.kind" class="stage" :class="`is-${kind.tone}`">
                <span class="dot" />{{ kind.label
                }}<em
                  >{{ kind.healthy }}/{{ kind.total - kind.inactive
                  }}<template v-if="kind.inactive"> · {{ kind.inactive }} 停用</template></em
                >
              </span>
            </div>
            <ul v-if="businessRows.length" class="issues issues--detail">
              <li v-for="item in businessRows" :key="item.key" :class="`is-${item.tone}`">
                <div>
                  <span class="dot" />{{ item.title }}<em>{{ item.attention ? item.reason : item.status }}</em>
                  <small class="muted">{{ item.spaceId }} · {{ item.checkedAt }}</small>
                </div>
                <RawError v-if="item.attention" :value="item.rawError" />
              </li>
            </ul>
            <footer class="tile-foot">
              <span v-if="!businessRows.length" class="muted">{{ businessQuiet }}</span>
              <button v-if="businessCount" type="button" class="link-button" @click="expanded.business = !expanded.business">
                {{ expanded.business ? "只看需要关注的" : `展开全部 ${businessCount} 项` }}
              </button>
            </footer>
          </article>
        </div>
      </section>
    </div>

    <a-modal v-model:visible="notificationVisible" title="消息通知设置" width="min(680px, 100vw)" :footer="false">
      <p class="muted notification-intro">告警会同时发到下面每个渠道；全部删除后，告警只在这里记录，不再推送到站外。</p>
      <ul class="notification-list" aria-label="通知渠道">
        <li v-for="channel in channels" :key="channel.channel_id">
          <span class="notification-list__type">{{ channelName(channel.channel_type) }}</span>
          <span class="notification-list__url">{{ channel.webhook_url }}</span>
          <a-button
            size="mini"
            type="text"
            status="danger"
            :loading="removing === channel.channel_id"
            @click="removeChannel(channel)"
            >删除</a-button
          >
        </li>
        <li v-if="!channels.length" class="notification-list__empty muted">还没有通知渠道，告警不会推送到站外平台</li>
      </ul>
      <section class="notification-add" aria-label="添加通知渠道">
        <h4>添加通知渠道</h4>
        <div class="notification-add__row">
          <a-select v-model="draft.channel_type" class="notification-add__type" aria-label="通知平台"
            ><a-option value="wecom">企业微信</a-option><a-option value="feishu">飞书</a-option></a-select
          >
          <a-input
            v-model="draft.webhook_url"
            aria-label="Webhook URL"
            placeholder="群机器人的 HTTPS Webhook URL"
            allow-clear
            @press-enter="addChannel"
          />
          <a-button type="primary" status="success" :loading="adding" @click="addChannel">添加</a-button>
        </div>
      </section>
    </a-modal>

    <a-drawer v-model:visible="detailVisible" title="组件详情" width="min(760px, 100vw)" :footer="false">
      <div v-if="selected" class="detail">
        <h3>
          {{ selected.name || selected.component_id }}<small>{{ selected.host_id }} · {{ selected.component_id }}</small>
        </h3>
        <p>{{ selected.reason }}</p>
        <dl class="facts">
          <dt>当前状态</dt>
          <dd>{{ statusLabel(selected.status) }}</dd>
          <dt>状态开始</dt>
          <dd>{{ formatCheckedAt(selected.status_since) }}</dd>
          <dt>探测地址</dt>
          <dd>{{ selected.probe_url || "不探测" }}</dd>
        </dl>
        <h4>健康探测 · {{ statusLabel(selected.probe?.status) }}</h4>
        <p>
          {{ selected.probe?.reason }}<small> · 最新检查 {{ formatCheckedAt(selected.probe?.checked_at) }}</small>
        </p>
        <RawError :value="selected.probe?.raw_error" />
        <div v-if="selected.recent_probes?.length" class="probe-strip" aria-label="最近探测">
          <span
            v-for="probe in selected.recent_probes"
            :key="probe.checked_at"
            class="probe"
            :class="`is-${classify(probe.status).tone}`"
            :title="`${formatCheckedAt(probe.checked_at)} · ${statusLabel(probe.status)} · ${probe.reason || ''}`"
          />
          <small class="muted">最近 {{ selected.recent_probes.length }} 次探测（最多 10 次）</small>
        </div>
        <h4>
          指标上报 · {{ selected.reporter?.status === "unchecked" ? "不要求上报" : statusLabel(selected.reporter?.status) }}
        </h4>
        <p>
          {{ selected.reporter?.reason }}<small> · 最后上报 {{ formatCheckedAt(selected.reporter?.checked_at) }}</small>
        </p>
        <div v-if="selected.instances?.length" class="table-scroll">
          <table>
            <thead>
              <tr>
                <th>实例</th>
                <th>版本</th>
                <th>状态</th>
                <th>最后上报</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="item in selected.instances" :key="`${item.instance_id}:${item.boot_id}`">
                <td>
                  {{ item.instance_id }}<small>{{ item.boot_id }}</small>
                </td>
                <td>{{ item.version }}</td>
                <td>{{ statusLabel(item.status) }}</td>
                <td>{{ formatCheckedAt(item.last_reported_at) }}</td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>
    </a-drawer>
  </div>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, reactive, ref, watch } from "vue";
import { storeToRefs } from "pinia";
import { Message, Modal } from "@arco-design/web-vue";
import { monitorApi, type HealthComponent, type HealthOverview, type NotificationChannel } from "@/api/monitor";
import { listAllDatasets, listAllViews } from "@/api/storage/metadata";
import { usePolling } from "@/hooks/usePolling";
import { createLatestRequestGuard } from "@/utils/latest-request";
import { useUserInfoStore } from "@/store/modules/user-info";
import { createSubmitScope } from "@/utils/submit-scope";
import { relativeTime, toneIcon, type NameBook } from "@/views/home/home-model";
import { formatCheckedAt, statusLabel } from "./health-display";
import {
  alertGroups,
  businessKinds,
  classify,
  componentsByHost,
  hostCards,
  incidentTimeline,
  layerCountText,
  layerSummaries,
  pipelineStages,
  serverNow,
  verdict,
  type LayerKey,
  type LayerSummary
} from "./monitor-model";
import RawError from "./raw-error.vue";

const overview = ref<HealthOverview>({});
const loading = ref(false);
const loaded = ref(false);
const error = ref("");
const loadedAt = ref("");
const clockMs = ref(Date.now());
const names = reactive<NameBook>({ datasets: {}, views: {} });
const expandedAlert = ref("");
const showAllIncidents = ref(false);
const expanded = reactive({ pipeline: false, business: false });
const detailVisible = ref(false);
const selected = ref<HealthComponent | null>(null);
const notificationVisible = ref(false);
const channels = ref<NotificationChannel[]>([]);
const draft = reactive({ channel_type: "wecom", webhook_url: "" });
const adding = ref(false);
const removing = ref("");
// 完整的 Webhook URL 是推送凭据，读取接口只对全局管理员开放，其他用户不显示设置入口。
const { account } = storeToRefs(useUserInfoStore());
const isAdmin = computed(() => !!account.value.user?.admin);
const refreshGuard = createLatestRequestGuard();

const nowMs = computed(() => serverNow(overview.value, clockMs.value));
const groups = computed(() => alertGroups(overview.value, names, nowMs.value));
const layerList = computed(() => layerSummaries(overview.value));
const layers = computed(
  () => Object.fromEntries(layerList.value.map(item => [item.key, item])) as Record<LayerKey, LayerSummary>
);
const conclusion = computed(() =>
  verdict({
    overview: overview.value,
    loaded: loaded.value,
    error: error.value,
    groups: groups.value,
    layers: layerList.value,
    nowMs: clockMs.value
  })
);
const timeline = computed(() => incidentTimeline(overview.value, names, nowMs.value));
// 默认只看最近几条非闪断的经过；其余（含 1 分钟内自愈的闪断）收起，避免长列表把分层健康挤到很远。
const INCIDENT_PREVIEW_ROWS = 8;
const visibleIncidents = computed(() =>
  showAllIncidents.value ? timeline.value.rows : timeline.value.rows.filter(row => !row.flap).slice(0, INCIDENT_PREVIEW_ROWS)
);
const hiddenIncidents = computed(() => timeline.value.rows.length - visibleIncidents.value.length);
const hiddenIncidentsText = computed(() => {
  const flaps = timeline.value.flaps;
  return `展开其余 ${hiddenIncidents.value} 条${flaps ? `（含 ${flaps} 条 1 分钟内自愈的闪断）` : ""}`;
});
const timelineSummary = computed(() => {
  const { total, rows, flaps, active } = timeline.value;
  if (!total) return "没有告警";
  const parts = [`${total} 次告警`];
  if (rows.length < total) parts.push(`同一时间触发的合并为 ${rows.length} 条`);
  if (flaps) parts.push(`${flaps} 条 1 分钟内自愈`);
  if (active) parts.push(`${active} 条进行中`);
  return parts.join(" · ");
});
const componentHosts = computed(() => componentsByHost(overview.value));
const componentIssues = computed(() => componentHosts.value.flatMap(host => host.chips.filter(chip => chip.attention)));
const unregistered = computed(() => overview.value.unregistered || []);
const hosts = computed(() => hostCards(overview.value, nowMs.value));
const stages = computed(() => pipelineStages(overview.value, names, nowMs.value));
const allDatasetRows = computed(() => stages.value.flatMap(stage => stage.rows));
// 数据集按环节分组：展开时列出全部，收起时只列需要关注的行，没有需要关注的环节不出现。
const datasetGroups = computed(() =>
  stages.value
    .map(stage => {
      const attention = stage.rows.filter(row => row.attention).length;
      return {
        id: stage.id,
        label: stage.label,
        summary: `${stage.rows.length} 个数据集${attention ? ` · ${attention} 个需要关注` : ""}`,
        rows: expanded.pipeline ? stage.rows : stage.rows.filter(row => row.attention)
      };
    })
    .filter(group => group.rows.length)
);
const pipelineQuiet = computed(() =>
  allDatasetRows.value.length ? `${allDatasetRows.value.length} 条数据链路都在按时更新` : "暂无数据集观测"
);
const business = computed(() => businessKinds(overview.value, names, nowMs.value));
const businessCount = computed(() => business.value.reduce((sum, kind) => sum + kind.total, 0));
const businessRows = computed(() =>
  business.value.flatMap(kind => (expanded.business ? kind.items : kind.items.filter(item => item.attention)))
);
const businessQuiet = computed(() => (businessCount.value ? "没有需要关注的业务检查" : "暂无业务检查"));
const notificationLabel = computed(() => {
  const summary = overview.value.notification;
  const count = summary?.channel_count || 0;
  if (!count) return "未配置";
  const platforms = [...new Set((summary?.channel_types || []).map(type => channelName(type)))].join("、");
  return count > 1 ? `${platforms}（${count} 个渠道）` : `${platforms} ✓`;
});

function channelName(type?: string) {
  return ({ wecom: "企业微信", feishu: "飞书" } as Record<string, string>)[type || ""] || type || "";
}

function othersText(titles: string[]) {
  return titles.length > 2 ? `${titles.slice(0, 2).join("、")} 等 ${titles.length} 项` : titles.join("、");
}

async function refresh() {
  const request = refreshGuard.begin();
  loading.value = true;
  try {
    const rsp = await monitorApi.getOverview();
    if (!rsp.overview) throw new Error("健康概览响应缺失");
    if (request.isLatest()) {
      overview.value = rsp.overview;
      loaded.value = true;
      error.value = "";
      loadedAt.value = new Date().toISOString();
    }
  } catch (err) {
    if (request.isLatest()) error.value = err instanceof Error ? err.message : "健康数据加载失败";
  } finally {
    if (request.isLatest()) loading.value = false;
  }
}

// 数据集、View 名称按概览里出现的空间加载：每个空间只读一次，显式带上该空间的请求头（概览不限于当前所选空间）；
// 读取失败的空间保留内部 ID，不随轮询重试。
const nameSpaces = computed(() => {
  const spaces = new Set<string>();
  for (const stage of overview.value.pipeline || [])
    for (const item of stage.datasets || []) if (item.space_id) spaces.add(item.space_id);
  for (const item of [...(overview.value.alerts || []), ...(overview.value.incidents || [])])
    if (item.object?.space_id) spaces.add(item.object.space_id);
  for (const item of overview.value.business_checks || []) if (item.space_id) spaces.add(item.space_id);
  return [...spaces].sort();
});
const requestedNameSpaces = new Set<string>();
watch(nameSpaces, spaces => {
  for (const space of spaces) {
    if (requestedNameSpaces.has(space)) continue;
    requestedNameSpaces.add(space);
    void Promise.all([listAllDatasets(space, { spaceId: space }), listAllViews(space, { spaceId: space })])
      .then(([datasets, views]) => {
        for (const item of datasets) if (item.dataset_id) names.datasets[item.dataset_id] = item.name || item.dataset_id;
        for (const item of views) if (item.view_id) names.views[item.view_id] = item.name || item.view_id;
      })
      .catch(() => undefined);
  }
});

function openComponent(item: HealthComponent) {
  selected.value = item;
  detailVisible.value = true;
}

async function openNotification() {
  try {
    const rsp = await monitorApi.listNotificationChannels();
    channels.value = rsp.channels || [];
    syncNotificationSummary();
    draft.webhook_url = "";
    notificationVisible.value = true;
  } catch (err) {
    Message.error(err instanceof Error ? err.message : "通知渠道加载失败");
  }
}
// 页头的“告警推送”跟着弹窗里的增删即时更新，不等下一次刷新概览。
function syncNotificationSummary() {
  overview.value.notification = {
    channel_count: channels.value.length,
    channel_types: channels.value.map(channel => channel.channel_type || "")
  };
}
async function addChannel() {
  const url = draft.webhook_url.trim();
  if (!url) {
    Message.warning("请填写群机器人的 Webhook URL");
    return;
  }
  const submission = notificationScope.begin();
  adding.value = true;
  try {
    const rsp = await monitorApi.addNotificationChannel({ channel_type: draft.channel_type, webhook_url: url });
    if (!submission.isCurrent()) return;
    if (rsp.channel) channels.value = [...channels.value, rsp.channel];
    syncNotificationSummary();
    draft.webhook_url = "";
    Message.success("已添加通知渠道");
  } catch (err) {
    Message.error(err instanceof Error ? err.message : "添加通知渠道失败");
  } finally {
    if (submission.isCurrent()) adding.value = false;
  }
}
function removeChannel(channel: NotificationChannel) {
  const last = channels.value.length === 1;
  Modal.warning({
    title: "删除通知渠道",
    content: last
      ? "这是最后一个渠道，删除后告警只在这里记录，不再推送到站外。是否继续？"
      : `删除后不再向这个${channelName(channel.channel_type)}群推送告警，是否继续？`,
    hideCancel: false,
    onOk: () => void deleteChannel(channel)
  });
}
async function deleteChannel(channel: NotificationChannel) {
  const id = channel.channel_id || "";
  const submission = notificationScope.begin();
  removing.value = id;
  try {
    await monitorApi.deleteNotificationChannel({ channel_id: id });
    if (!submission.isCurrent()) return;
    channels.value = channels.value.filter(item => item.channel_id !== id);
    syncNotificationSummary();
    Message.success("已删除通知渠道");
  } catch (err) {
    Message.error(err instanceof Error ? err.message : "删除通知渠道失败");
  } finally {
    if (submission.isCurrent()) removing.value = "";
  }
}

// 提交作用域：弹窗关闭或重新打开后，迟到的响应不能再改动重新加载的渠道列表
const notificationScope = createSubmitScope({ global: true });
watch(notificationVisible, () => notificationScope.invalidate());

// 编辑通知设置时暂停轮询，避免刷新打断表单。
usePolling(refresh, 30000, () => !notificationVisible.value);
let clock: number | undefined;
onMounted(() => {
  void refresh();
  clock = window.setInterval(() => (clockMs.value = Date.now()), 5000);
});
onBeforeUnmount(() => {
  if (clock !== undefined) window.clearInterval(clock);
  refreshGuard.invalidate();
});
</script>

<style scoped lang="scss">
// 状态色：正常绿、需关注橙、异常红、无观测灰；与首页一致，层次靠底色与留白。
.is-ok {
  --tone: rgb(var(--green-6));
  --tone-bg: rgb(var(--green-1));
}
.is-warn {
  --tone: rgb(var(--orange-6));
  --tone-bg: rgb(var(--orange-1));
}
.is-bad {
  --tone: rgb(var(--red-6));
  --tone-bg: rgb(var(--red-1));
}
.is-idle {
  --tone: var(--color-text-3);
  --tone-bg: var(--color-fill-2);
}

.health-monitor-page {
  height: 100%;
  overflow: auto;
}
.monitor {
  font-variant-numeric: tabular-nums;
}
h2,
h3,
h4,
p {
  margin: 0;
}
.muted,
small {
  color: var(--color-text-3);
}
small {
  font-size: 12px;
}
a {
  color: rgb(var(--primary-6));
}
.dot {
  display: inline-block;
  flex: none;
  width: 7px;
  height: 7px;
  margin-right: 6px;
  border-radius: 50%;
  background: var(--tone, var(--color-text-4));
  vertical-align: 1px;
}
.chip {
  padding: 0 6px;
  border-radius: 4px;
  background: var(--color-fill-2);
  color: var(--color-text-2);
  font-size: 12px;
  white-space: nowrap;
}
.chip--bad {
  background: rgb(var(--red-1));
  color: rgb(var(--red-6));
}
.link-button {
  padding: 0;
  border: 0;
  background: none;
  color: rgb(var(--primary-6));
  font-size: 12px;
  cursor: pointer;
}
.empty {
  padding: 8px 0;
  color: var(--color-text-3);
}
.tile {
  border-radius: 8px;
  background: var(--color-bg-2);
  box-shadow: 0 0 0 1px var(--color-neutral-3);
}

.monitor-head {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  justify-content: space-between;
  gap: 16px;
  margin-bottom: var(--moox-space-4);
}
.monitor-head p {
  margin-top: 4px;
}

.verdict {
  display: flex;
  flex-wrap: wrap;
  align-items: flex-start;
  gap: 12px;
  padding: var(--moox-space-4);
  border-radius: 8px;
  background: var(--tone-bg);
}
.verdict-icon {
  display: inline-flex;
  font-size: 24px;
  color: var(--tone);
}
.verdict-main {
  display: grid;
  flex: 1;
  min-width: 240px;
  gap: 4px;
}
.verdict-main h3 {
  color: var(--tone);
  font-size: 20px;
  font-weight: 600;
}
.verdict-main p {
  color: var(--color-text-2);
}
.verdict-meta {
  display: grid;
  gap: 2px;
  color: var(--color-text-3);
  font-size: 12px;
  text-align: right;
}

.health-section {
  margin-top: 24px;
}
.section-title {
  display: flex;
  flex-wrap: wrap;
  align-items: baseline;
  gap: 8px;
  margin-bottom: var(--moox-space-2);
  font-size: 15px;
  font-weight: 600;
}
.section-title small {
  font-weight: 400;
}

.alert-list {
  display: grid;
  gap: var(--moox-space-2);
}
.alert-group__head {
  display: flex;
  justify-content: space-between;
  padding: 8px 14px;
  border-radius: 8px 8px 0 0;
  background: var(--tone-bg);
  color: var(--tone);
  font-size: 12px;
}
.alert-line {
  padding: 10px 14px;
}
.alert-line + .alert-line {
  border-top: 1px solid var(--color-neutral-3);
}
.alert-line__main {
  display: flex;
  width: 100%;
  align-items: center;
  gap: 8px;
  padding: 0;
  border: 0;
  background: none;
  color: var(--color-text-1);
  text-align: left;
  cursor: pointer;
}
.alert-line__main:focus-visible {
  outline: 2px solid rgb(var(--primary-6));
  outline-offset: 2px;
}
.alert-line__title {
  overflow: hidden;
  font-weight: 600;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.tag {
  flex: none;
  padding: 0 6px;
  border-radius: 4px;
  background: var(--tone-bg);
  color: var(--tone);
  font-size: 12px;
}
.tag--ok {
  background: rgb(var(--green-1));
  color: rgb(var(--green-6));
}
.duration {
  flex: none;
  margin-left: auto;
  color: var(--tone);
  font-weight: 600;
}
.alert-line__reason {
  margin: 4px 0 0 13px;
  color: var(--color-text-2);
  overflow-wrap: anywhere;
}
.alert-line__foot {
  display: flex;
  justify-content: space-between;
  margin: 6px 0 0 13px;
  font-size: 12px;
}
.alert-line__detail {
  margin: 8px 0 0 13px;
}
.facts {
  display: grid;
  grid-template-columns: max-content 1fr;
  gap: 4px 16px;
  margin: 0;
  font-size: 13px;
}
.facts dt {
  color: var(--color-text-3);
}
.facts dd {
  margin: 0;
  overflow-wrap: anywhere;
}
.notification-intro {
  margin-bottom: 12px;
  font-size: 13px;
}
.notification-list {
  display: grid;
  gap: 6px;
  margin: 0;
  padding: 0;
  list-style: none;
}
.notification-list li {
  display: grid;
  grid-template-columns: 64px minmax(0, 1fr) auto;
  align-items: center;
  gap: 10px;
  padding: 8px 10px;
  border-radius: 6px;
  background: var(--color-fill-1);
  font-size: 13px;
}
.notification-list .notification-list__empty {
  display: block;
}
.notification-list__type {
  color: var(--color-text-2);
  font-weight: 600;
}
.notification-list__url {
  font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace;
  word-break: break-all;
  user-select: all;
}
.notification-add {
  margin-top: 16px;
}
.notification-add h4 {
  margin-bottom: 8px;
  color: var(--color-text-2);
  font-size: 13px;
  font-weight: 600;
}
.notification-add__row {
  display: grid;
  grid-template-columns: 112px minmax(0, 1fr) auto;
  gap: 8px;
}
@media (max-width: 520px) {
  .notification-add__row {
    grid-template-columns: minmax(0, 1fr);
  }
}

.timeline {
  padding: 6px 14px;
}
.incident {
  display: grid;
  grid-template-columns: 86px auto minmax(0, 1fr) auto auto;
  align-items: center;
  gap: 8px;
  padding: 6px 0;
}
.incident + .incident {
  border-top: 1px solid var(--color-neutral-2);
}
.incident__time {
  color: var(--color-text-3);
  font-size: 12px;
}
.incident__title {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.incident__title em {
  color: var(--color-text-3);
  font-style: normal;
}
.incident__flags {
  display: inline-flex;
  gap: 4px;
}
.incident__outcome {
  color: var(--color-text-3);
  font-size: 12px;
  white-space: nowrap;
}
.incident__outcome.is-active {
  color: var(--tone);
  font-weight: 600;
}
.timeline .link-button {
  margin: 6px 0;
}

/* 各层整行排列：并排时两张卡片高度相差很大（主机卡片随主机数变高），较矮的一侧会留下大块空白。 */
.layers {
  display: grid;
  grid-template-columns: minmax(0, 1fr);
  gap: var(--moox-space-3);
}
.layer {
  display: flex;
  min-width: 0;
  flex-direction: column;
  padding: 12px 14px;
}
.tile-head {
  display: flex;
  align-items: baseline;
  justify-content: space-between;
  gap: 8px;
  margin-bottom: 10px;
  font-size: 12px;
}
.tile-head h4 {
  font-size: 14px;
  font-weight: 600;
}
.tile-foot {
  display: flex;
  justify-content: space-between;
  gap: 8px;
  margin-top: auto;
  padding-top: 10px;
  font-size: 12px;
}

.host-rows {
  display: grid;
  gap: 8px;
}
.host-row {
  display: grid;
  grid-template-columns: 80px minmax(0, 1fr);
  align-items: center;
  gap: 8px;
}
.host-row__name {
  color: var(--color-text-3);
  font-size: 12px;
}
.chips {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
}
.component-chip {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  padding: 2px 8px;
  border: 0;
  border-radius: 4px;
  background: var(--color-fill-1);
  color: var(--color-text-1);
  font-size: 12px;
  cursor: pointer;
}
.component-chip .dot {
  width: 8px;
  height: 8px;
  margin: 0;
}
.component-chip:hover,
.component-chip:focus-visible {
  outline: none;
  background: var(--color-fill-3);
}
.issues {
  display: grid;
  gap: 4px;
  margin: 10px 0 0;
  padding: 0;
  list-style: none;
}
.issues button {
  padding: 0;
  border: 0;
  background: none;
  color: var(--color-text-1);
  text-align: left;
  cursor: pointer;
}
.issues em {
  margin-left: 8px;
  color: var(--tone);
  font-style: normal;
}
.issues--detail li {
  padding: 4px 0;
}
.issues--detail small {
  margin-left: 8px;
}

/* 主机卡片在卡内横向平分整行，主机多时自动换行。 */
.host-cards {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(min(300px, 100%), 1fr));
  gap: 10px;
}
.host-card {
  padding: 8px 12px;
  border-radius: 6px;
  background: var(--color-fill-1);
}
/* 主机名、地址、链接各自不拆开，放不下时整段换到下一行；只有比整行还长的主机名才断开。 */
.host-card__head {
  display: flex;
  flex-wrap: wrap;
  align-items: baseline;
  gap: 2px 6px;
}
.host-card__head strong {
  min-width: 0;
  overflow-wrap: anywhere;
}
.host-card__link {
  margin-left: auto;
  font-size: 12px;
  white-space: nowrap;
}
.meters {
  display: grid;
  gap: 4px;
  margin: 6px 0 4px 13px;
}
.meter-row {
  display: grid;
  grid-template-columns: 32px minmax(0, 1fr) 46px 96px;
  align-items: center;
  gap: 8px;
  font-size: 12px;
}
.meter-row__label {
  color: var(--color-text-3);
}
.meter-row__value {
  text-align: right;
}
.meter-row__capacity {
  overflow: hidden;
  color: var(--color-text-3);
  text-align: right;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.meter {
  position: relative;
  height: 6px;
  border-radius: 3px;
  background: var(--color-fill-2);
}
.meter i {
  position: absolute;
  inset: 0 auto 0 0;
  border-radius: 3px;
  background: var(--tone);
}
.meter b {
  position: absolute;
  top: -3px;
  bottom: -3px;
  width: 2px;
  background: rgb(var(--red-6));
}
.host-card__reason {
  margin-left: 13px;
  color: var(--tone);
  font-size: 12px;
}
.host-card small {
  margin-left: 13px;
}

.stages,
.kinds {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 6px;
}
.stage {
  display: inline-flex;
  align-items: center;
  padding: 2px 8px;
  border-radius: 4px;
  background: var(--color-fill-1);
  font-size: 12px;
}
.stage em {
  margin-left: 6px;
  color: var(--color-text-3);
  font-style: normal;
}
.stage-arrow {
  color: var(--color-text-4);
}
.table-scroll {
  overflow: auto;
  margin-top: 10px;
}
table {
  width: 100%;
  border-collapse: collapse;
  font-size: 13px;
}
th,
td {
  padding: 6px 8px;
  border-bottom: 1px solid var(--color-neutral-2);
  text-align: left;
  white-space: nowrap;
}
th {
  color: var(--color-text-3);
  font-weight: 400;
}
td small {
  margin-left: 8px;
}
.dataset-table .lag.is-ok,
.dataset-table .lag.is-idle {
  color: var(--color-text-1);
}
.dataset-table .lag.is-warn,
.dataset-table .lag.is-bad {
  color: var(--tone);
  font-weight: 600;
}
.status-cell {
  white-space: normal;
}
.dataset-table .group-row th {
  padding-top: 12px;
  color: var(--color-text-1);
  font-weight: 600;
}
.dataset-table .group-row small {
  margin-left: 8px;
  color: var(--color-text-3);
  font-weight: 400;
}
.dataset-table tbody:first-of-type .group-row th {
  padding-top: 6px;
}

.detail {
  display: grid;
  gap: 10px;
}
.detail h3 small {
  margin-left: 8px;
  font-weight: 400;
}
.detail h4 {
  margin-top: 8px;
}
.probe-strip {
  display: flex;
  align-items: center;
  gap: 4px;
}
.probe {
  width: 10px;
  height: 18px;
  border-radius: 2px;
  background: var(--tone);
}
.probe-strip small {
  margin-left: 8px;
}

@media (max-width: 760px) {
  .incident {
    grid-template-columns: 70px auto minmax(0, 1fr);
  }
  .incident__flags,
  .incident__outcome {
    grid-column: 3;
  }
  .verdict-meta {
    text-align: left;
  }
}
</style>
