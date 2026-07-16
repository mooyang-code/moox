<template>
  <div class="moox-page host-workbench-page">
    <div class="moox-inner">
      <PageTitleTabs :model-value="activeTab" :items="tabs" aria-label="主机工作台" @change="onTabChange" />

      <a-alert v-if="loadError" type="warning" show-icon class="load-alert">{{ loadError }}</a-alert>

      <div class="workbench-content">
        <template v-if="activeTab === 'hosts'">
          <SshHosts embedded :monitor-by-host-id="monitorByHostId" :monitor-only-hosts="monitorOnlyHosts" @connect="openTerminal" @file-manage="openFileManager" />
        </template>
        <HostMonitor v-else />
      </div>

      <a-modal v-model:visible="terminalVisible" :title="terminalTitle" :width="1100" :footer="false" :esc-to-close="false" unmount-on-close @close="clearTerminal">
        <div class="terminal-modal-body"><SshTerminal v-if="terminalHostId" :initial-host-id="terminalHostId" disconnect-on-unmount /></div>
      </a-modal>

      <a-modal v-model:visible="fileManagerVisible" title="文件管理" :width="960" :footer="false" :body-style="{ padding: 0, height: '70vh', overflowY: 'auto' }" unmount-on-close @close="closeFileManager">
        <a-spin :loading="fileManagerLoading" class="file-manager-loading">
          <SshFileManager v-if="fileManagerSessionId" :session-id="fileManagerSessionId" />
        </a-spin>
      </a-modal>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue';
import { useRoute, useRouter } from 'vue-router';
import { Message } from '@arco-design/web-vue';
import PageTitleTabs from '@/components/page-title-tabs/index.vue';
import SshHosts from '@/views/container/ssh-hosts/ssh-hosts.vue';
import SshTerminal from '@/views/container/ssh-terminal/ssh-terminal.vue';
import SshFileManager from '@/views/container/ssh-file-manager/ssh-file-manager.vue';
import HostMonitor from './host-monitor.vue';
import { getCurrentMetrics, type HostMetrics } from '@/api/modules/host-monitor';
import { createSSHSession, disconnectSSHSession, listSSHHosts, type SSHHost } from '@/api/modules/ssh';
import { mergeHostWorkbenchRows, type HostWorkbenchRow } from './host-workbench-utils';

const route = useRoute();
const router = useRouter();
type HostTab = 'hosts' | 'monitor';
const tabs = [
  { key: 'hosts', label: '主机列表' },
  { key: 'monitor', label: '主机监控' },
] as const;
const normalizeTab = (value: unknown): HostTab => value === 'monitor' ? value : 'hosts';
const activeTab = ref<HostTab>(normalizeTab(route.query.tab));
if (route.query.tab !== undefined && route.query.tab !== activeTab.value) {
  void router.replace({ query: { ...route.query, tab: activeTab.value } });
}
const monitorLoading = ref(false);
const loadError = ref('');
const monitors = ref<HostMetrics[]>([]);
const sshHosts = ref<SSHHost[]>([]);
const terminalVisible = ref(false);
const terminalHostId = ref<number>();
const fileManagerVisible = ref(false);
const fileManagerLoading = ref(false);
const fileManagerSessionId = ref('');
const terminalTitle = computed(() => {
  const host = sshHosts.value.find((item) => item.id === terminalHostId.value);
  if (!host?.address) return 'SSH 终端';
  return host.name ? `SSH 终端 - ${host.address}（${host.name}）` : `SSH 终端 - ${host.address}`;
});
const rows = computed<HostWorkbenchRow[]>(() => mergeHostWorkbenchRows(monitors.value, sshHosts.value));
const monitorByHostId = computed(() => Object.fromEntries(rows.value.filter((row) => row.ssh?.id !== undefined && row.monitor).map((row) => [row.ssh!.id, row.monitor])));
const monitorOnlyHosts = computed(() => rows.value.filter((row) => row.monitor && !row.ssh).map((row) => row.monitor!));

async function loadMonitorData() {
  monitorLoading.value = true;
  loadError.value = '';
  try {
    const results = await Promise.allSettled([getCurrentMetrics(), listSSHHosts({ limit: 500 })]);
    const [monitorRsp, sshRsp] = results;
    if (monitorRsp.status === 'fulfilled') monitors.value = monitorRsp.value.metrics;
    else loadError.value = '资源监控暂不可用，仍可管理 SSH 主机。';
    if (sshRsp.status === 'fulfilled') sshHosts.value = sshRsp.value.hosts || [];
    else loadError.value = `${loadError.value ? `${loadError.value} ` : ''}SSH 主机配置加载失败。`;
  } finally {
    monitorLoading.value = false;
  }
}

function onTabChange(value: string | number) {
  activeTab.value = normalizeTab(value);
  void router.replace({ query: { ...route.query, tab: activeTab.value } });
}

function openTerminal(hostId: number) {
  terminalHostId.value = hostId;
  terminalVisible.value = true;
}

function clearTerminal() {
  terminalHostId.value = undefined;
}

async function openFileManager(hostId: number) {
  fileManagerLoading.value = true;
  try {
    const response = await createSSHSession({ host_id: hostId });
    if (!response.session_id) throw new Error('无法获取 session_id');
    fileManagerSessionId.value = response.session_id;
    fileManagerVisible.value = true;
  } catch (error: any) {
    Message.error(`打开文件管理失败：${error?.message || '未知错误'}`);
  } finally {
    fileManagerLoading.value = false;
  }
}

function closeFileManager() {
  const sessionId = fileManagerSessionId.value;
  fileManagerSessionId.value = '';
  if (sessionId) void disconnectSSHSession(sessionId).catch(() => undefined);
}

watch(() => route.query.tab, (value) => {
  activeTab.value = normalizeTab(value);
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
.workbench-content { min-width:0; margin-top:16px; }
.workbench-content :deep(.moox-page) { height:auto; padding:0; overflow:visible; background:transparent; }
.workbench-content :deep(.moox-page > .moox-inner) { min-height:0; padding:0; border:0; border-radius:0; box-shadow:none; }
.monitor-table-wrap { margin-top:4px; }
.ssh-management { margin-top:16px; border-top:1px solid var(--color-border-2); padding-top:12px; }
.ssh-management :deep(.moox-page) { padding:0; }
.terminal-modal-body { height: min(68vh, 720px); overflow:hidden; }
.terminal-modal-body :deep(.ssh-terminal-page) { height:100%; }
.file-manager-loading { display:block; height:100%; }
</style>
