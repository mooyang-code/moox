<template>
  <div class="moox-page host-workbench-page">
    <div class="moox-inner">
      <header class="page-head">
        <div>
          <h2>主机工作台</h2>
          <p>在同一个主机列表中查看资源状态、SSH 配置和在线会话。</p>
        </div>
        <a-button :loading="monitorLoading" @click="loadMonitorData">
          <template #icon><icon-refresh /></template>
          刷新监控
        </a-button>
      </header>

      <div class="summary-strip">
        <div><span>主机总数</span><strong>{{ rows.length }}</strong></div>
        <div><span>监控在线</span><strong class="healthy">{{ onlineCount }}</strong></div>
        <div><span>未配置 SSH</span><strong class="warning">{{ unmatchedMonitorCount }}</strong></div>
        <div><span>未接入监控</span><strong>{{ unmatchedSSHCount }}</strong></div>
      </div>
      <a-alert v-if="loadError" type="warning" show-icon class="load-alert">{{ loadError }}</a-alert>

      <a-tabs v-model:active-key="activeTab" type="rounded" @change="onTabChange">
        <a-tab-pane key="hosts" title="主机列表">
          <SshHosts embedded :monitor-by-host-id="monitorByHostId" :monitor-only-hosts="monitorOnlyHosts" @connect="openTerminal" />
        </a-tab-pane>
        <a-tab-pane key="sessions" title="在线会话">
          <SshSessions />
        </a-tab-pane>
      </a-tabs>

      <a-modal v-model:visible="terminalVisible" title="SSH 终端" :width="1100" :footer="false" :closable="false" :esc-to-close="false" unmount-on-close>
        <div class="terminal-modal-toolbar"><a-button size="small" @click="requestTerminalClose">关闭终端</a-button></div>
        <div class="terminal-modal-body"><SshTerminal v-if="terminalHostId" :initial-host-id="terminalHostId" disconnect-on-unmount /></div>
      </a-modal>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue';
import { useRoute, useRouter } from 'vue-router';
import { Modal } from '@arco-design/web-vue';
import SshHosts from '@/views/container/ssh-hosts/ssh-hosts.vue';
import SshSessions from '@/views/container/ssh-sessions/ssh-sessions.vue';
import SshTerminal from '@/views/container/ssh-terminal/ssh-terminal.vue';
import { getCurrentMetrics, type HostMetrics } from '@/api/modules/host-monitor';
import { getOnlineSessions, listSSHHosts, type SSHHost, type SessionInfo } from '@/api/modules/ssh';
import { mergeHostWorkbenchRows, type HostWorkbenchRow } from './host-workbench-utils';

const route = useRoute();
const router = useRouter();
const activeTab = ref(route.query.tab === 'sessions' ? 'sessions' : 'hosts');
if (route.query.tab !== undefined && route.query.tab !== 'sessions' && route.query.tab !== 'hosts') {
  void router.replace({ query: { ...route.query, tab: activeTab.value } });
}
const monitorLoading = ref(false);
const loadError = ref('');
const monitors = ref<HostMetrics[]>([]);
const sshHosts = ref<SSHHost[]>([]);
const sessions = ref<SessionInfo[]>([]);
const terminalVisible = ref(false);
const terminalHostId = ref<number>();
const rows = computed<HostWorkbenchRow[]>(() => mergeHostWorkbenchRows(monitors.value, sshHosts.value, sessions.value));
const monitorByHostId = computed(() => Object.fromEntries(rows.value.filter((row) => row.ssh?.id !== undefined && row.monitor).map((row) => [row.ssh!.id, row.monitor])));
const monitorOnlyHosts = computed(() => rows.value.filter((row) => row.monitor && !row.ssh).map((row) => row.monitor!));
const onlineCount = computed(() => monitors.value.filter((item) => item.status === 'online').length);
const unmatchedMonitorCount = computed(() => rows.value.filter((row) => row.monitor && !row.ssh).length);
const unmatchedSSHCount = computed(() => rows.value.filter((row) => row.ssh && !row.monitor).length);

async function loadMonitorData() {
  monitorLoading.value = true;
  loadError.value = '';
  try {
    const results = await Promise.allSettled([getCurrentMetrics(), listSSHHosts({ limit: 500 }), getOnlineSessions()]);
    const [monitorRsp, sshRsp, sessionRsp] = results;
    if (monitorRsp.status === 'fulfilled') monitors.value = monitorRsp.value.metrics;
    else loadError.value = '资源监控暂不可用，仍可管理 SSH 主机。';
    if (sshRsp.status === 'fulfilled') sshHosts.value = sshRsp.value.hosts || [];
    else loadError.value = `${loadError.value ? `${loadError.value} ` : ''}SSH 主机配置加载失败。`;
    if (sessionRsp.status === 'fulfilled') sessions.value = sessionRsp.value.sessions || [];
    else loadError.value = `${loadError.value ? `${loadError.value} ` : ''}在线会话加载失败。`;
  } finally {
    monitorLoading.value = false;
  }
}

function onTabChange(value: string | number) {
  activeTab.value = value === 'sessions' ? 'sessions' : 'hosts';
  void router.replace({ query: { ...route.query, tab: activeTab.value } });
}

function openTerminal(hostId: number) {
  terminalHostId.value = hostId;
  terminalVisible.value = true;
}

function requestTerminalClose() {
  Modal.warning({
    title: '关闭 SSH 终端',
    content: '关闭窗口将断开当前 SSH 会话，是否继续？',
    hideCancel: false,
    onOk: () => {
      terminalVisible.value = false;
      terminalHostId.value = undefined;
    },
  });
}

watch(() => route.query.tab, (value) => {
  activeTab.value = value === 'sessions' ? 'sessions' : 'hosts';
});

onMounted(async () => {
  await loadMonitorData();
  const hostId = Number(route.query.hostId);
  if (Number.isFinite(hostId) && hostId > 0) openTerminal(hostId);
});
</script>

<style scoped lang="scss">
.host-workbench-page { height: 100%; min-height: 0; }
.host-workbench-page > .moox-inner { min-height: 100%; }
.page-head { display:flex; align-items:flex-start; justify-content:space-between; gap:16px; margin-bottom:8px; }
.page-head h2 { margin:0 0 4px; font-size:20px; }
.page-head p { margin:0; color:var(--color-text-3); }
.summary-strip { display:grid; grid-template-columns:repeat(4,minmax(0,1fr)); gap:12px; margin:8px 0 12px; }
.summary-strip div { padding:12px 14px; border:1px solid var(--color-border-2); background:var(--color-bg-2); }
.summary-strip span,.sub-text { display:block; color:var(--color-text-3); font-size:12px; }
.summary-strip strong { display:block; margin-top:5px; font-size:20px; }
.healthy { color:#16a34a; }.warning { color:#d97706; }
.monitor-table-wrap { margin-top:4px; }
.ssh-management { margin-top:16px; border-top:1px solid var(--color-border-2); padding-top:12px; }
.ssh-management :deep(.moox-page) { padding:0; }
.terminal-modal-body { height: min(68vh, 720px); overflow:hidden; }
.terminal-modal-toolbar { display:flex; justify-content:flex-end; margin-bottom:8px; }
.terminal-modal-body :deep(.ssh-terminal-page) { height:100%; }
@media (max-width:760px) { .summary-strip { grid-template-columns:repeat(2,minmax(0,1fr)); } .page-head { align-items:stretch; flex-direction:column; } }
</style>
