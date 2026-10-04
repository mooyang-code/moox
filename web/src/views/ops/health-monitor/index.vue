<template>
  <div class="moox-page health-monitor-page">
    <div class="moox-inner">
      <div class="health-toolbar">
        <div v-if="!props.embedded"><h2>健康监控</h2><span class="muted">系统会自动检查行情、因子、交易和核心服务</span></div>
        <a-space><a-button :loading="loading" @click="refresh"><icon-refresh /> 刷新</a-button><a-button type="outline" @click="openNotification"><icon-notification /> 消息通知设置</a-button></a-space>
      </div>
      <a-alert v-if="error" type="error" :closable="false" class="error">{{ error }}</a-alert>
      <section class="health-section"><div class="section-title"><h3>当前告警</h3><span>{{ overview.alerts?.length || 0 }} 项</span></div><div v-if="showClearState" class="clear-state" :class="systemHealthy ? 'clear-state--healthy' : 'clear-state--attention'" role="status" aria-live="polite"><component :is="systemHealthy ? IconCheckCircleFill : IconInfoCircleFill" class="clear-state__icon" /><div class="clear-state__body"><strong>{{ systemHealthy ? "系统一切正常" : "当前没有待处理告警" }}</strong><span>{{ systemHealthy ? "所有业务与核心服务均正常运行" : "暂无正在触发的告警，但仍有监控项需要关注" }}</span></div><a-tag :color="systemHealthy ? 'green' : 'orange'">{{ systemHealthy ? "正常" : "无告警" }}</a-tag></div><div v-else-if="overview.alerts?.length" class="alert-list"><a-card v-for="(item, index) in overview.alerts" :key="item.id || item.title || index" class="alert-card health-card--interactive" role="button" tabindex="0" @click="openAlert(item)" @keydown.enter.prevent="openAlert(item)" @keydown.space.prevent="openAlert(item)"><template #title><span><a-tag color="red">异常</a-tag>{{ item.title }}</span><span class="card-arrow">查看详情</span></template><div>{{ displayConclusion(item.reason) }}</div><small>最后上报：{{ formatCheckedAt(item.checked_at) }}</small></a-card></div></section>
      <section class="health-section"><div class="section-title"><h3>业务健康</h3></div><div class="item-grid"><a-card v-for="item in overview.business_items" :key="item.id" class="health-card health-card--interactive" role="button" tabindex="0" @click="openItem(item)" @keydown.enter.prevent="openItem(item)" @keydown.space.prevent="openItem(item)"><template #title><span>{{ item.name }}</span><span><a-tag :color="statusColor(item.status)">{{ statusLabel(item.status) }}</a-tag><span class="card-arrow">查看详情</span></span></template><p>{{ item.description }}</p><div class="reason">{{ displayConclusion(item.reason) || "暂无异常" }}</div><small>{{ item.omitted_instance_count ? `已合并 ${item.omitted_instance_count} 项 · ` : "" }}最后上报：{{ formatCheckedAt(item.checked_at) }}</small></a-card></div></section>
      <section class="health-section"><div class="section-title"><h3>核心服务</h3></div><div class="item-grid"><a-card v-for="item in overview.service_items" :key="item.id" class="health-card health-card--interactive" role="button" tabindex="0" @click="openItem(item)" @keydown.enter.prevent="openItem(item)" @keydown.space.prevent="openItem(item)"><template #title><span>{{ item.name }}</span><span><a-tag :color="statusColor(item.status)">{{ statusLabel(item.status) }}</a-tag><span class="card-arrow">查看详情</span></span></template><p>{{ item.description }}</p><div class="reason">{{ displayConclusion(item.reason) || "暂无异常" }}</div><small>{{ item.omitted_instance_count ? `已合并 ${item.omitted_instance_count} 项 · ` : "" }}最后上报：{{ formatCheckedAt(item.checked_at) }}</small></a-card></div></section>
    </div>
    <a-modal v-model:visible="notificationVisible" title="消息通知设置" @ok="saveNotification" @cancel="notificationVisible = false">
      <a-form layout="vertical"><a-form-item label="通知平台"><a-select v-model="notification.channel_type"><a-option value="wecom">企业微信</a-option><a-option value="feishu">飞书</a-option></a-select></a-form-item><a-form-item label="机器人 Webhook URL"><a-input v-model="notification.webhook_url" @input="notification.url_changed = true" placeholder="输入新的 HTTPS Webhook URL" /></a-form-item><a-checkbox v-model="notification.clear_url">清空当前 URL，停止站外通知</a-checkbox><div class="muted">当前配置：{{ notification.masked || "未配置" }}</div></a-form>
    </a-modal>
    <a-drawer v-model:visible="detailVisible" title="监控详情" width="min(860px, 100vw)"><div v-if="selectedItem"><h3>{{ selectedItem.name }}</h3><p>{{ selectedItem.description }}</p><div class="detail-meta"><span>当前状态：<a-tag :color="statusColor(selectedItem.status)">{{ statusLabel(selectedItem.status) }}</a-tag></span><span>结论：{{ displayConclusion(selectedItem.reason) || "暂无" }}</span><span>最后上报：{{ formatCheckedAt(selectedItem.checked_at) }}</span></div><a-table :data="selectedItem.instances || []" :pagination="false" size="small" :scroll="{ x: 760 }"><template #columns><a-table-column title="名称" data-index="name" :width="180" /><a-table-column title="节点" data-index="node_id" :width="150" /><a-table-column title="实例" data-index="instance_id" :width="150" /><a-table-column title="状态" data-index="status" :width="90"><template #cell="{ record }"><a-tag :color="statusColor(record.status)">{{ statusLabel(record.status) }}</a-tag></template></a-table-column><a-table-column title="结论" :width="240"><template #cell="{ record }">{{ displayConclusion(record.conclusion) || "暂无" }}</template></a-table-column><a-table-column title="最后上报" :width="170"><template #cell="{ record }">{{ formatCheckedAt(record.last_checked_at) }}</template></a-table-column></template></a-table></div><div v-else-if="selectedAlert"><h3>{{ selectedAlert.title || "当前告警" }}</h3><div class="detail-meta"><span>状态：{{ statusLabel(selectedAlert.status || "down") }}</span><span>结论：{{ displayConclusion(selectedAlert.reason) || "暂无" }}</span><span>最后上报：{{ formatCheckedAt(selectedAlert.checked_at) }}</span></div></div></a-drawer>
  </div>
</template>

<script setup lang="ts">
import { computed, onActivated, onDeactivated, onMounted, onUnmounted, reactive, ref } from "vue";
import { Message, Modal } from "@arco-design/web-vue";
import { IconCheckCircleFill, IconInfoCircleFill, IconNotification, IconRefresh } from "@arco-design/web-vue/es/icon";
import { healthMonitorApi, type HealthAlert, type HealthItem, type HealthOverview } from "@/api/health-monitor";
import { createLatestRequestGuard } from "@/utils/latest-request";
import { displayConclusion, formatCheckedAt, statusColor, statusLabel } from "./health-display";

const props = defineProps<{ embedded?: boolean }>();
const overview = ref<HealthOverview>({ alerts: [], business_items: [], service_items: [] });
const loading = ref(false); const error = ref(""); const overviewLoaded = ref(false); const notificationVisible = ref(false); const detailVisible = ref(false); const selectedItem = ref<HealthItem | null>(null); const selectedAlert = ref<HealthAlert | null>(null); const notification = reactive({ channel_type: "wecom", webhook_url: "", masked: "", clear_url: false, url_changed: false }); let notificationInitialType = "wecom"; let timer: number | undefined; const refreshGuard = createLatestRequestGuard();
const showClearState = computed(() => overviewLoaded.value && !overview.value.alerts?.length);
const systemHealthy = computed(() => {
  const items = [...(overview.value.business_items || []), ...(overview.value.service_items || [])];
  return items.length > 0 && items.every(item => item.status === "healthy" || item.status === "ok");
});
async function refresh() { const request = refreshGuard.begin(); loading.value = true; error.value = ""; try { const rsp = await healthMonitorApi.getOverview(); if (request.isLatest()) { overview.value = rsp.overview || { alerts: [], business_items: [], service_items: [] }; overviewLoaded.value = true; } } catch (err) { if (request.isLatest()) error.value = err instanceof Error ? err.message : "健康数据加载失败"; } finally { if (request.isLatest()) loading.value = false; } }
async function openNotification() { try { const rsp = await healthMonitorApi.getNotification(); const setting = rsp.channel || {}; notification.channel_type = setting.channel_type || "wecom"; notificationInitialType = notification.channel_type; notification.masked = setting.masked_url || ""; notification.webhook_url = ""; notification.clear_url = false; notification.url_changed = false; notificationVisible.value = true; } catch (err) { Message.error(err instanceof Error ? err.message : "通知配置加载失败"); } }
function openItem(item: HealthItem) { selectedAlert.value = null; selectedItem.value = item; detailVisible.value = true; }
function openAlert(item: HealthAlert) { selectedItem.value = null; selectedAlert.value = item; detailVisible.value = true; }
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
		Modal.warning({ title: "确认停用通知", content: "清空后系统将不再向站外平台发送告警，是否继续？", onOk: () => void persistNotification() });
    return;
  }
  await persistNotification();
}
async function persistNotification() {
  try {
    const rsp = await healthMonitorApi.updateNotification({ channel_type: notification.channel_type, webhook_url: notification.clear_url ? "" : notification.webhook_url });
    notification.masked = rsp.channel?.masked_url || "";
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
function startPolling() {
  if (timer) return;
  timer = window.setInterval(() => { if (!notificationVisible.value) void refresh(); }, 30000);
}
function stopPolling() {
  if (timer) window.clearInterval(timer);
  timer = undefined;
}
onMounted(() => { void refresh(); startPolling(); });
onActivated(() => { if (!timer) { void refresh(); startPolling(); } });
onDeactivated(stopPolling);
onUnmounted(stopPolling);
</script>

<style scoped lang="scss">
.health-monitor-page { height: 100%; overflow: auto; } .health-toolbar,.section-title { display:flex; align-items:center; justify-content:space-between; gap:16px; } h2,h3 { margin:0; } .muted,small { color:var(--color-text-3); } .error { margin:16px 0; } .health-section { margin-top:24px; } .section-title { margin-bottom:12px; } .alert-list,.item-grid { display:grid; gap:12px; grid-template-columns:repeat(auto-fit,minmax(240px,1fr)); } .clear-state { display:flex; align-items:center; gap:12px; min-height:64px; padding:14px 18px; border:1px solid var(--color-border-2); border-radius:8px; background:var(--color-fill-1); } .clear-state--healthy { border-color:rgb(var(--green-3)); background:rgb(var(--green-1)); } .clear-state--attention { border-color:rgb(var(--orange-3)); background:rgb(var(--orange-1)); } .clear-state__icon { flex:0 0 auto; font-size:24px; } .clear-state--healthy .clear-state__icon { color:rgb(var(--green-6)); } .clear-state--attention .clear-state__icon { color:rgb(var(--orange-6)); } .clear-state__body { display:grid; flex:1; gap:2px; min-width:0; } .clear-state__body strong { color:var(--color-text-1); font-size:15px; } .clear-state__body span { color:var(--color-text-3); font-size:13px; } .health-card,.alert-card { border-radius:8px; } .health-card--interactive { cursor:pointer; transition: border-color .16s ease, box-shadow .16s ease; } .health-card--interactive:hover,.health-card--interactive:focus-visible { border-color:rgb(var(--primary-6)); box-shadow:0 0 0 2px rgb(var(--primary-1)); outline:none; } .health-card :deep(.arco-card-header-title) { display:flex; align-items:center; justify-content:space-between; width:100%; gap:12px; } .health-card p { color:var(--color-text-2); margin:0 0 8px; } .reason { min-height:22px; margin-bottom:8px; } .card-arrow { color:var(--color-text-3); font-size:12px; font-weight:400; white-space:nowrap; } .detail-meta { display:flex; flex-wrap:wrap; gap:12px 24px; margin:16px 0; color:var(--color-text-2); }
</style>
