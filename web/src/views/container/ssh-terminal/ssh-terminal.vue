<template>
  <div class="ssh-terminal-page">
    <!-- Tab bar -->
    <div class="tab-bar">
      <div class="tab-list">
        <div
          v-for="tab in tabs"
          :key="tab.id"
          class="tab-item"
          :class="{ active: tab.id === activeTabId }"
          @click="switchTab(tab.id)"
        >
          <span class="tab-status" :class="{ connected: tab.connected }" />
          <span class="tab-name">{{ tab.hostName }}</span>
          <span class="tab-close" @click.stop="closeTab(tab.id)">
            <icon-close />
          </span>
        </div>
      </div>
    </div>

    <!-- Toolbar -->
    <div class="toolbar">
      <div class="toolbar-left">
        <a-select
          v-model="selectedHostId"
          placeholder="选择主机连接..."
          style="width: 240px"
          allow-search
          allow-clear
          :loading="hostsLoading"
          @change="onHostSelected"
        >
          <a-option
            v-for="host in hostList"
            :key="host.id"
            :value="host.id"
          >
            {{ host.name }} ({{ host.address }}:{{ host.port }})
          </a-option>
        </a-select>
      </div>
      <div class="toolbar-right">
        <a-space>
          <a-tooltip content="重连">
            <a-button
              size="small"
              type="text"
              :disabled="!activeTab"
              @click="reconnect"
            >
              <template #icon><icon-sync /></template>
              重连
            </a-button>
          </a-tooltip>
          <a-tooltip content="清屏">
            <a-button
              size="small"
              type="text"
              :disabled="!activeTab"
              @click="clearScreen"
            >
              <template #icon><icon-eraser /></template>
              清屏
            </a-button>
          </a-tooltip>
          <a-tooltip content="断开">
            <a-button
              size="small"
              type="text"
              status="danger"
              :disabled="!activeTab || !activeTab.connected"
              @click="disconnectCurrent"
            >
              <template #icon><icon-poweroff /></template>
              断开
            </a-button>
          </a-tooltip>
          <a-tooltip content="文件管理">
            <a-button
              size="small"
              type="text"
              :disabled="!activeTab || !activeTab.connected"
              @click="openFileManager"
            >
              <template #icon><icon-folder /></template>
              文件管理
            </a-button>
          </a-tooltip>
        </a-space>
      </div>
    </div>

    <!-- Terminal container -->
    <div class="terminal-area">
      <div
        v-for="tab in tabs"
        :key="tab.id"
        :ref="(el: any) => setTerminalRef(tab.id, el)"
        class="terminal-wrapper"
        :class="{ hidden: tab.id !== activeTabId }"
      />
      <div v-if="tabs.length === 0" class="terminal-placeholder">
        <div class="placeholder-content">
          <icon-desktop style="font-size: 48px; color: var(--color-text-4)" />
          <p>请从上方选择主机以建立 SSH 连接</p>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, onMounted, onUnmounted, nextTick } from 'vue';
import { useRoute, useRouter } from 'vue-router';
import { Message } from '@arco-design/web-vue';
import { Terminal } from '@xterm/xterm';
import { AttachAddon } from '@xterm/addon-attach';
import { FitAddon } from '@xterm/addon-fit';
import '@xterm/xterm/css/xterm.css';
import {
  listSSHHosts,
  createSSHSession,
  disconnectSSHSession,
  resizeSSHTerminal,
  getSSHWebSocketUrl,
  getSSHHostDetail,
  type SSHHost,
} from '@/api/modules/ssh';

const route = useRoute();
const router = useRouter();

// ---------- Host list ----------

const hostList = ref<SSHHost[]>([]);
const hostsLoading = ref(false);
const selectedHostId = ref<number | undefined>(undefined);

const fetchHosts = async () => {
  hostsLoading.value = true;
  try {
    const res = await listSSHHosts({ limit: 200 });
    hostList.value = res.data?.data ?? [];
  } catch {
    Message.error('获取主机列表失败');
  } finally {
    hostsLoading.value = false;
  }
};

// ---------- Tab management ----------

interface TerminalTab {
  id: string;
  hostId: number;
  hostName: string;
  connected: boolean;
  terminal?: Terminal;
  fitAddon?: FitAddon;
  ws?: WebSocket;
  config?: SSHHost;
}

const tabs = ref<TerminalTab[]>([]);
const activeTabId = ref<string>('');

const activeTab = computed<TerminalTab | undefined>(() =>
  tabs.value.find((t) => t.id === activeTabId.value)
);

// Track terminal container DOM refs keyed by tab id
const terminalRefs: Record<string, HTMLElement | null> = {};

const setTerminalRef = (tabId: string, el: HTMLElement | null) => {
  if (el) {
    terminalRefs[tabId] = el;
  }
};

// ---------- ResizeObserver ----------

let resizeObserver: ResizeObserver | null = null;

const setupResizeObserver = (tabId: string) => {
  const el = terminalRefs[tabId];
  if (!el) return;

  // Clean up previous observer
  if (resizeObserver) {
    resizeObserver.disconnect();
  }

  resizeObserver = new ResizeObserver(() => {
    const tab = tabs.value.find((t) => t.id === tabId);
    if (tab?.fitAddon && tab.terminal) {
      try {
        tab.fitAddon.fit();
        if (tab.connected && tab.ws?.readyState === WebSocket.OPEN) {
          resizeSSHTerminal(tab.id, tab.terminal.cols, tab.terminal.rows);
        }
      } catch {
        // ignore fit errors during transition
      }
    }
  });

  resizeObserver.observe(el);
};

// ---------- Window resize handler ----------

const handleWindowResize = () => {
  const tab = activeTab.value;
  if (!tab?.fitAddon || !tab.terminal) return;
  try {
    tab.fitAddon.fit();
    if (tab.connected && tab.ws?.readyState === WebSocket.OPEN) {
      resizeSSHTerminal(tab.id, tab.terminal.cols, tab.terminal.rows);
    }
  } catch {
    // ignore
  }
};

// ---------- Connection flow ----------

const connectToHost = async (hostId: number) => {
  // Fetch host detail for terminal config
  let hostConfig: SSHHost | undefined;
  try {
    const detailRes = await getSSHHostDetail(hostId);
    hostConfig = detailRes.data?.data?.[0] as SSHHost;
  } catch {
    Message.error('获取主机信息失败');
    return;
  }

  if (!hostConfig) {
    Message.error('主机信息为空');
    return;
  }

  // Create session
  let sessionId: string;
  try {
    const sessionRes = await createSSHSession({ host_id: hostId });
    sessionId = sessionRes.data?.data?.[0]?.session_id;
    if (!sessionId) {
      Message.error('创建会话失败：无法获取 session_id');
      return;
    }
  } catch (err: any) {
    Message.error('创建 SSH 会话失败：' + (err?.message || '未知错误'));
    return;
  }

  // Create tab
  const tab: TerminalTab = {
    id: sessionId,
    hostId: hostId,
    hostName: hostConfig.name || `${hostConfig.address}:${hostConfig.port}`,
    connected: false,
    config: hostConfig,
  };

  tabs.value.push(tab);
  activeTabId.value = sessionId;

  // Wait for DOM to render
  await nextTick();

  // Initialize terminal
  initTerminal(tab);
};

const initTerminal = (tab: TerminalTab) => {
  const container = terminalRefs[tab.id];
  if (!container) {
    Message.error('终端容器未就绪');
    return;
  }

  const config = tab.config;

  const term = new Terminal({
    fontSize: config?.font_size || 14,
    fontFamily: config?.font_family || "'Consolas', 'Monaco', 'Courier New', monospace",
    cursorStyle: config?.cursor_style || 'block',
    cursorBlink: true,
    theme: {
      background: config?.background || '#1e1e1e',
      foreground: config?.foreground || '#d4d4d4',
      cursor: config?.cursor_color || '#ffffff',
    },
    allowProposedApi: true,
    scrollback: 5000,
  });

  const fitAddon = new FitAddon();
  term.loadAddon(fitAddon);
  term.open(container);

  try {
    fitAddon.fit();
  } catch {
    // ignore initial fit error
  }

  tab.terminal = term;
  tab.fitAddon = fitAddon;

  // Build WebSocket URL and connect
  const wsUrl = getSSHWebSocketUrl(tab.id, term.cols, term.rows);
  const ws = new WebSocket(wsUrl);

  ws.onopen = () => {
    tab.connected = true;
    const attachAddon = new AttachAddon(ws);
    term.loadAddon(attachAddon);
    term.focus();
  };

  ws.onclose = () => {
    tab.connected = false;
    term.writeln('\r\n\x1b[31m[连接已断开]\x1b[0m');
  };

  ws.onerror = () => {
    tab.connected = false;
    term.writeln('\r\n\x1b[31m[连接发生错误]\x1b[0m');
  };

  tab.ws = ws;

  // Setup resize observer for this tab
  setupResizeObserver(tab.id);
};

// ---------- Tab switching ----------

const switchTab = async (tabId: string) => {
  if (activeTabId.value === tabId) return;
  activeTabId.value = tabId;

  await nextTick();

  const tab = tabs.value.find((t) => t.id === tabId);
  if (tab?.fitAddon && tab.terminal) {
    try {
      tab.fitAddon.fit();
      tab.terminal.focus();
    } catch {
      // ignore
    }
    setupResizeObserver(tabId);
  }
};

// ---------- Tab close ----------

const closeTab = async (tabId: string) => {
  const tabIndex = tabs.value.findIndex((t) => t.id === tabId);
  if (tabIndex === -1) return;

  const tab = tabs.value[tabIndex];

  // Disconnect
  if (tab.ws && tab.ws.readyState === WebSocket.OPEN) {
    tab.ws.close();
  }
  try {
    await disconnectSSHSession(tab.id);
  } catch {
    // ignore
  }

  // Dispose terminal
  if (tab.terminal) {
    tab.terminal.dispose();
  }

  // Clean up ref
  delete terminalRefs[tabId];

  // Remove tab
  tabs.value.splice(tabIndex, 1);

  // Switch to adjacent tab
  if (activeTabId.value === tabId) {
    if (tabs.value.length > 0) {
      const newIndex = Math.min(tabIndex, tabs.value.length - 1);
      activeTabId.value = tabs.value[newIndex].id;
      await nextTick();
      const newTab = tabs.value[newIndex];
      if (newTab?.fitAddon) {
        try {
          newTab.fitAddon.fit();
          newTab.terminal?.focus();
        } catch {
          // ignore
        }
        setupResizeObserver(newTab.id);
      }
    } else {
      activeTabId.value = '';
    }
  }
};

// ---------- Toolbar actions ----------

const onHostSelected = (hostId: any) => {
  if (!hostId) return;
  connectToHost(Number(hostId));
  // Reset selection so user can open the same host again
  selectedHostId.value = undefined;
};

const reconnect = async () => {
  const tab = activeTab.value;
  if (!tab) return;

  const hostId = tab.hostId;
  const tabId = tab.id;

  // Disconnect current
  if (tab.ws && tab.ws.readyState === WebSocket.OPEN) {
    tab.ws.close();
  }
  try {
    await disconnectSSHSession(tab.id);
  } catch {
    // ignore
  }
  if (tab.terminal) {
    tab.terminal.dispose();
  }
  delete terminalRefs[tabId];

  // Remove old tab
  const idx = tabs.value.findIndex((t) => t.id === tabId);
  if (idx !== -1) {
    tabs.value.splice(idx, 1);
  }

  // Connect fresh
  await connectToHost(hostId);
};

const clearScreen = () => {
  const tab = activeTab.value;
  if (tab?.terminal) {
    tab.terminal.clear();
  }
};

const disconnectCurrent = async () => {
  const tab = activeTab.value;
  if (!tab) return;

  if (tab.ws && tab.ws.readyState === WebSocket.OPEN) {
    tab.ws.close();
  }
  try {
    await disconnectSSHSession(tab.id);
  } catch {
    // ignore
  }
  tab.connected = false;
  tab.terminal?.writeln('\r\n\x1b[33m[已手动断开连接]\x1b[0m');
};

const openFileManager = () => {
  const tab = activeTab.value;
  if (!tab) return;
  router.push({
    path: '/container-management/ssh-file-manager',
    query: { sessionId: tab.id },
  });
};

// ---------- Lifecycle ----------

onMounted(async () => {
  await fetchHosts();

  window.addEventListener('resize', handleWindowResize);

  // Auto-connect if hostId is provided in query
  const hostIdQuery = route.query.hostId;
  if (hostIdQuery) {
    const hostId = Number(hostIdQuery);
    if (!isNaN(hostId) && hostId > 0) {
      await connectToHost(hostId);
    }
  }
});

onUnmounted(() => {
  window.removeEventListener('resize', handleWindowResize);

  if (resizeObserver) {
    resizeObserver.disconnect();
    resizeObserver = null;
  }

  // Clean up all tabs (only close WebSocket and dispose terminal UI,
  // don't disconnect server sessions — they may be used by file manager)
  for (const tab of tabs.value) {
    if (tab.ws && tab.ws.readyState === WebSocket.OPEN) {
      tab.ws.close();
    }
    if (tab.terminal) {
      tab.terminal.dispose();
    }
  }
  tabs.value = [];
});
</script>

<style lang="scss" scoped>
.ssh-terminal-page {
  display: flex;
  flex-direction: column;
  height: 100vh;
  background: #1a1a2e;
  overflow: hidden;
}

/* ---------- Tab bar ---------- */

.tab-bar {
  display: flex;
  align-items: center;
  height: 38px;
  min-height: 38px;
  background: #16213e;
  border-bottom: 1px solid #0f3460;
  padding: 0 8px;
  overflow-x: auto;

  &::-webkit-scrollbar {
    height: 2px;
  }

  &::-webkit-scrollbar-thumb {
    background: #0f3460;
  }
}

.tab-list {
  display: flex;
  align-items: center;
  gap: 2px;
  height: 100%;
}

.tab-item {
  display: flex;
  align-items: center;
  gap: 6px;
  height: 30px;
  padding: 0 12px;
  border-radius: 6px 6px 0 0;
  background: #1a1a2e;
  color: #8e8ea0;
  font-size: 12px;
  cursor: pointer;
  white-space: nowrap;
  user-select: none;
  transition: background 0.15s, color 0.15s;

  &:hover {
    background: #252545;
    color: #c4c4d8;
  }

  &.active {
    background: #1e1e1e;
    color: #e4e4e8;
  }
}

.tab-status {
  width: 7px;
  height: 7px;
  border-radius: 50%;
  background: #555;
  flex-shrink: 0;

  &.connected {
    background: #52c41a;
    box-shadow: 0 0 4px rgba(82, 196, 26, 0.5);
  }
}

.tab-name {
  max-width: 140px;
  overflow: hidden;
  text-overflow: ellipsis;
}

.tab-close {
  display: flex;
  align-items: center;
  justify-content: center;
  width: 16px;
  height: 16px;
  border-radius: 3px;
  font-size: 10px;
  flex-shrink: 0;
  opacity: 0.5;
  transition: opacity 0.15s, background 0.15s;

  &:hover {
    opacity: 1;
    background: rgba(255, 255, 255, 0.1);
  }
}

/* ---------- Toolbar ---------- */

.toolbar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  height: 44px;
  min-height: 44px;
  padding: 0 12px;
  background: #1e1e2e;
  border-bottom: 1px solid #2a2a3e;

  :deep(.arco-select-view-single) {
    background: #2a2a3e;
    border-color: #3a3a4e;
    color: #d4d4d8;

    .arco-select-view-suffix,
    .arco-select-view-value {
      color: #d4d4d8;
    }

    &:hover {
      border-color: #4a4a5e;
    }
  }

  :deep(.arco-btn-text) {
    color: #a0a0b8;

    &:hover {
      color: #d4d4d8;
      background: rgba(255, 255, 255, 0.06);
    }

    &:disabled {
      color: #555;
    }
  }
}

.toolbar-left,
.toolbar-right {
  display: flex;
  align-items: center;
}

/* ---------- Terminal area ---------- */

.terminal-area {
  flex: 1;
  position: relative;
  overflow: hidden;
  background: #1e1e1e;
}

.terminal-wrapper {
  position: absolute;
  top: 0;
  left: 0;
  right: 0;
  bottom: 0;
  padding: 4px;

  &.hidden {
    visibility: hidden;
    pointer-events: none;
    z-index: -1;
  }

  :deep(.xterm) {
    height: 100%;
    padding: 4px;
  }

  :deep(.xterm-viewport) {
    &::-webkit-scrollbar {
      width: 8px;
    }

    &::-webkit-scrollbar-thumb {
      background: #3a3a4e;
      border-radius: 4px;
    }

    &::-webkit-scrollbar-track {
      background: transparent;
    }
  }
}

.terminal-placeholder {
  position: absolute;
  top: 0;
  left: 0;
  right: 0;
  bottom: 0;
  display: flex;
  align-items: center;
  justify-content: center;
}

.placeholder-content {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 16px;
  color: #555;
  font-size: 14px;
}
</style>
