<template>
  <div class="moox-page host-workbench-page">
    <div class="moox-inner">
      <PageTitleTabs :model-value="activeTab" :items="tabs" aria-label="主机工作台" @change="onTabChange" />

      <a-alert v-if="loadError" type="warning" show-icon class="load-alert">{{ loadError }}</a-alert>

      <div class="workbench-content">
        <template v-if="activeTab === 'hosts'">
          <SshHosts :rows="rows" :loading="monitorLoading" @connect="openTerminal" @file-manage="openFileManager" />
        </template>
        <HostMonitor v-else />
      </div>

      <a-modal
        v-model:visible="terminalVisible"
        :title="terminalTitle"
        :width="1100"
        :footer="false"
        :esc-to-close="false"
        unmount-on-close
        @close="clearTerminal"
      >
        <div class="terminal-modal-body">
          <SshTerminal v-if="terminalHostId" :initial-host-id="terminalHostId" disconnect-on-unmount />
        </div>
      </a-modal>

      <a-modal
        v-model:visible="fileManagerVisible"
        title="文件管理"
        :width="960"
        :footer="false"
        :body-style="{ padding: 0, height: '70vh', overflowY: 'auto' }"
        unmount-on-close
        @close="closeFileManager"
      >
        <a-spin :loading="fileManagerLoading" class="file-manager-loading">
          <SshFileManager v-if="fileManagerSessionId" :session-id="fileManagerSessionId" />
        </a-spin>
      </a-modal>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref, watch, onBeforeUnmount } from "vue";
import { useRoute, useRouter } from "vue-router";
import { Message } from "@arco-design/web-vue";
import PageTitleTabs from "@/components/page-title-tabs/index.vue";
import SshHosts from "@/views/container/ssh-hosts/ssh-hosts.vue";
import SshTerminal from "@/views/container/ssh-terminal/ssh-terminal.vue";
import SshFileManager from "@/views/container/ssh-file-manager/ssh-file-manager.vue";
import HostMonitor from "./host-monitor.vue";
import { listAllDeploymentHosts } from "@/api/admin/sysdeploy";
import type { DeploymentHost } from "@/api/admin/types";
import { getCurrentMetrics, type HostMetrics } from "@/api/modules/host-monitor";
import { createSSHSession, disconnectSSHSession } from "@/api/modules/ssh";
import { buildHostMonitorRows } from "./host-monitor-mapping";
import { sshAvailability } from "./host-workbench-utils";

const route = useRoute();
const router = useRouter();
type HostTab = "hosts" | "monitor";
const tabs = [
  { key: "hosts", label: "主机列表" },
  { key: "monitor", label: "主机监控" }
] as const;
const normalizeTab = (value: unknown): HostTab => (value === "monitor" ? value : "hosts");
const activeTab = ref<HostTab>(normalizeTab(route.query.tab));
if (route.query.tab !== undefined && route.query.tab !== activeTab.value) {
  void router.replace({ query: { ...route.query, tab: activeTab.value } });
}
const monitorLoading = ref(false);
const loadError = ref("");
const monitors = ref<HostMetrics[]>([]);
const hosts = ref<DeploymentHost[]>([]);
const terminalVisible = ref(false);
const terminalHostId = ref("");
const fileManagerVisible = ref(false);
const fileManagerLoading = ref(false);
const fileManagerSessionId = ref("");
const terminalTitle = computed(() => {
  const host = hosts.value.find(item => item.host_id === terminalHostId.value);
  if (!host) return "SSH 终端";
  return `SSH 终端 - ${host.label || host.host_id}（${host.address}）`;
});
const rows = computed(() => buildHostMonitorRows(hosts.value, monitors.value));

async function loadMonitorData() {
  monitorLoading.value = true;
  loadError.value = "";
  try {
    const results = await Promise.allSettled([getCurrentMetrics(), listAllDeploymentHosts()]);
    const [monitorRsp, hostRsp] = results;
    if (monitorRsp.status === "fulfilled") monitors.value = monitorRsp.value.metrics;
    else loadError.value = "资源监控暂不可用，仍可连接已登记 SSH 的主机。";
    if (hostRsp.status === "fulfilled") hosts.value = hostRsp.value;
    else loadError.value = `${loadError.value ? `${loadError.value} ` : ""}登记主机清单加载失败。`;
  } finally {
    monitorLoading.value = false;
  }
}

function onTabChange(value: string | number) {
  activeTab.value = normalizeTab(value);
  void router.replace({ query: { ...route.query, tab: activeTab.value } });
}

function openTerminal(hostId: string) {
  terminalHostId.value = hostId;
  terminalVisible.value = true;
}

function clearTerminal() {
  terminalHostId.value = "";
}

// 建连序号：连续点击不同主机时建连响应可能乱序；只有最新一次请求能更新弹窗与会话，页面卸载也使进行中的建连失效
let fileManagerRequest = 0;

async function openFileManager(hostId: string) {
  const request = ++fileManagerRequest;
  fileManagerLoading.value = true;
  try {
    const response = await createSSHSession({ host_id: hostId });
    if (!response.session_id) throw new Error("无法获取 session_id");
    if (request !== fileManagerRequest) {
      // 迟到的响应：它创建的会话没有人使用，立即断开，避免遗留 SSH 会话，也不能覆盖当前操作的主机
      void disconnectSSHSession(response.session_id).catch(() => undefined);
      return;
    }
    fileManagerSessionId.value = response.session_id;
    fileManagerVisible.value = true;
  } catch (error: any) {
    if (request !== fileManagerRequest) return;
    Message.error(`打开文件管理失败：${error?.message || "未知错误"}`);
  } finally {
    if (request === fileManagerRequest) fileManagerLoading.value = false;
  }
}

function closeFileManager() {
  fileManagerRequest += 1; // 关闭使进行中的建连失效（其会话由迟到分支断开）
  const sessionId = fileManagerSessionId.value;
  fileManagerSessionId.value = "";
  if (sessionId) void disconnectSSHSession(sessionId).catch(() => undefined);
}

watch(
  () => route.query.tab,
  value => {
    activeTab.value = normalizeTab(value);
  }
);

onMounted(async () => {
  await loadMonitorData();
  // 只有主机列表页签的 host_id 链接才直接打开终端；监控页签的 host_id 只用来定位主机，不能建连。
  const hostId = typeof route.query.host_id === "string" ? route.query.host_id : "";
  const target = hosts.value.find(item => item.host_id === hostId);
  if (activeTab.value === "hosts" && target && sshAvailability(target).ok) openTerminal(hostId);
});

onBeforeUnmount(() => {
  fileManagerRequest += 1;
});
</script>

<style scoped lang="scss">
.host-workbench-page {
  height: 100%;
  min-height: 0;
}
.host-workbench-page > .moox-inner {
  min-height: 100%;
}
.workbench-content {
  min-width: 0;
  margin-top: var(--moox-space-3);
}
.workbench-content :deep(.moox-page) {
  height: auto;
  padding: 0;
  overflow: visible;
  background: transparent;
}
.workbench-content :deep(.moox-page > .moox-inner) {
  min-height: 0;
  padding: 0;
  border: 0;
  border-radius: 0;
  box-shadow: none;
}
.monitor-table-wrap {
  margin-top: var(--moox-space-1);
}
.ssh-management {
  margin-top: var(--moox-space-4);
  border-top: 1px solid var(--color-border-2);
  padding-top: var(--moox-space-3);
}
.ssh-management :deep(.moox-page) {
  padding: 0;
}
.terminal-modal-body {
  height: min(68vh, 720px);
  overflow: hidden;
}
.terminal-modal-body :deep(.ssh-terminal-page) {
  height: 100%;
}
.file-manager-loading {
  display: block;
  height: 100%;
}
</style>
