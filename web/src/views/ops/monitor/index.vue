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
                <span class="chips" :class="{ 'chips--labeled': expanded.components }">
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
                    <span class="dot" /><span v-if="expanded.components">{{ chip.name }}</span>
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
            <footer class="tile-foot">
              <button type="button" class="link-button" @click="expanded.components = !expanded.components">
                {{ expanded.components ? "只显示状态点" : "显示组件名称" }}
              </button>
            </footer>
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
            <div v-if="datasetRows.length" class="table-scroll">
              <table class="dataset-table">
                <thead>
                  <tr>
                    <th>数据集</th>
                    <th>周期</th>
                    <th>水位落后</th>
                    <th>允许落后</th>
                    <th>状态</th>
                  </tr>
                </thead>
                <tbody>
                  <tr v-for="row in datasetRows" :key="row.key" :class="`is-${row.tone}`">
                    <td>
                      <span class="dot" />{{ row.name }}<small>{{ row.producer }} · {{ row.spaceId }}</small>
                    </td>
                    <td>{{ row.freq }}</td>
                    <td class="lag" :class="`is-${row.lagTone}`">{{ row.lag }}</td>
                    <td>{{ row.limit }}</td>
                    <td class="status-cell">{{ row.attention ? row.reason || row.status : row.status }}</td>
                  </tr>
                </tbody>
              </table>
            </div>
            <footer class="tile-foot">
              <span v-if="!datasetRows.length" class="muted">{{ pipelineQuiet }}</span>
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

    <a-modal
      v-model:visible="notificationVisible"
      title="消息通知设置"
      width="min(640px, 100vw)"
      @ok="saveNotification"
      @cancel="notificationVisible = false"
    >
      <a-form layout="vertical"
        ><a-form-item label="通知平台"
          ><a-select v-model="notification.channel_type"
            ><a-option value="wecom">企业微信</a-option><a-option value="feishu">飞书</a-option></a-select
          ></a-form-item
        ><a-form-item label="机器人 Webhook URL"
          ><a-input
            v-model="notification.webhook_url"
            @input="notification.url_changed = true"
            placeholder="输入新的 HTTPS Webhook URL" /></a-form-item
        ><a-checkbox v-model="notification.clear_url">清空当前 URL，停止站外通知</a-checkbox>
        <section class="notification-current" aria-label="当前配置">
          <h4>当前配置</h4>
          <dl v-if="notification.current_url" class="facts">
            <dt>通知平台</dt>
            <dd>{{ channelName(notification.current_type) }}</dd>
            <dt>Webhook URL</dt>
            <dd class="notification-current__url">{{ notification.current_url }}</dd>
          </dl>
          <p v-else class="muted">未配置，告警不会推送到站外平台</p>
        </section></a-form
      >
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
import { monitorApi, type HealthComponent, type HealthOverview } from "@/api/monitor";
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
const expanded = reactive({ components: false, pipeline: false, business: false });
const detailVisible = ref(false);
const selected = ref<HealthComponent | null>(null);
const notificationVisible = ref(false);
// current_type / current_url 是已保存的配置，表单里的 channel_type / webhook_url 是待保存的修改。
const notification = reactive({
  channel_type: "wecom",
  webhook_url: "",
  current_type: "",
  current_url: "",
  clear_url: false,
  url_changed: false
});
// 完整的 Webhook URL 是推送凭据，读取接口只对全局管理员开放，其他用户不显示设置入口。
const { account } = storeToRefs(useUserInfoStore());
const isAdmin = computed(() => !!account.value.user?.admin);
let notificationInitialType = "wecom";
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
const stages = computed(() => pipelineStages(overview.value, names));
const allDatasetRows = computed(() => stages.value.flatMap(stage => stage.rows));
const datasetRows = computed(() =>
  expanded.pipeline ? allDatasetRows.value : allDatasetRows.value.filter(row => row.attention)
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
  const setting = overview.value.notification;
  if (!setting?.configured) return "未配置";
  return channelName(setting.channel_type) + " ✓";
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
    const rsp = await monitorApi.getNotification();
    const setting = rsp.channel || {};
    notification.channel_type = setting.channel_type || "wecom";
    notificationInitialType = notification.channel_type;
    notification.current_type = setting.channel_type || "";
    notification.current_url = setting.webhook_url || "";
    notification.webhook_url = "";
    notification.clear_url = false;
    notification.url_changed = false;
    notificationVisible.value = true;
  } catch (err) {
    Message.error(err instanceof Error ? err.message : "通知配置加载失败");
  }
}
async function saveNotification() {
  const clearing = notification.clear_url || (notification.url_changed && !notification.webhook_url.trim());
  if (!notification.url_changed && !notification.clear_url && notification.channel_type === notificationInitialType) {
    notificationVisible.value = false;
    return;
  }
  if (!notification.url_changed && !notification.clear_url) {
    Message.warning("修改通知平台前，请重新输入 Webhook URL 或勾选清空");
    return;
  }
  if (clearing) {
    if (!notification.clear_url) notification.clear_url = true;
    Modal.warning({
      title: "确认停用通知",
      content: "清空后系统将不再向站外平台发送告警，是否继续？",
      onOk: () => void persistNotification()
    });
    return;
  }
  await persistNotification();
}
async function persistNotification() {
  const submission = notificationScope.begin();
  try {
    const rsp = await monitorApi.updateNotification({
      channel_type: notification.channel_type,
      webhook_url: notification.clear_url ? "" : notification.webhook_url
    });
    overview.value.notification = { channel_type: rsp.channel?.channel_type, configured: !!rsp.channel?.webhook_url };
    // 配置已保存；但弹窗已被关闭/重新打开时，不能清空用户在新表单里刚输入的 Webhook，也不能关闭新弹窗
    if (!submission.isCurrent()) {
      Message.success("通知配置已保存");
      return;
    }
    notification.current_type = rsp.channel?.channel_type || "";
    notification.current_url = rsp.channel?.webhook_url || "";
    notification.webhook_url = "";
    notification.clear_url = false;
    notification.url_changed = false;
    notificationInitialType = notification.channel_type;
    notificationVisible.value = false;
    Message.success("通知配置已保存");
  } catch (err) {
    Message.error(err instanceof Error ? err.message : "通知配置保存失败");
  }
}

// 提交作用域（重新打开才使旧提交失效，@ok 自动关闭不影响正常保存）：迟到的响应不能再关闭或清空重新打开的表单
const notificationScope = createSubmitScope({ global: true });
watch(notificationVisible, open => {
  if (open) notificationScope.invalidate();
});

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
.notification-current {
  margin-top: 16px;
  padding: 10px 12px;
  border-radius: 6px;
  background: var(--color-fill-1);
}
.notification-current h4 {
  margin-bottom: 6px;
  color: var(--color-text-2);
  font-size: 13px;
  font-weight: 600;
}
.notification-current__url {
  font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace;
  user-select: all;
  word-break: break-all;
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
  padding: 3px;
  border: 0;
  border-radius: 4px;
  background: none;
  cursor: pointer;
}
.component-chip .dot {
  width: 10px;
  height: 10px;
  margin: 0;
}
.chips--labeled .component-chip {
  gap: 6px;
  padding: 2px 8px;
  background: var(--color-fill-1);
  color: var(--color-text-1);
  font-size: 12px;
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
  grid-template-columns: 32px minmax(0, 1fr) 52px;
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
