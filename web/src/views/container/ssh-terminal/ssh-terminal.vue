<template>
  <div class="ssh-terminal-page">
    <!-- Toolbar -->
    <div class="toolbar">
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
          <p>请从主机列表点击连接以建立 SSH 连接</p>
        </div>
      </div>
    </div>

    <!-- 文件管理弹窗 -->
    <a-modal
      v-model:visible="fileManagerVisible"
      title="文件管理"
      :width="960"
      :footer="false"
      :body-style="{ padding: 0, height: '70vh', overflowY: 'auto' }"
      unmount-on-close
    >
      <SshFileManager :session-id="fileManagerSessionId" />
    </a-modal>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, onMounted, onUnmounted, nextTick } from 'vue';
import { useRoute } from 'vue-router';
import { Message } from '@arco-design/web-vue';
import { Terminal } from '@xterm/xterm';
import { AttachAddon } from '@xterm/addon-attach';
import { FitAddon } from '@xterm/addon-fit';
import '@xterm/xterm/css/xterm.css';
import {
  createSSHSession,
  disconnectSSHSession,
  resizeSSHTerminal,
  getSSHWebSocketUrl,
  getSSHHostDetail,
  type SSHHost,
} from '@/api/modules/ssh';
import SshFileManager from '@/views/container/ssh-file-manager/ssh-file-manager.vue';

const route = useRoute();
const props = defineProps<{ initialHostId?: number; disconnectOnUnmount?: boolean }>();

// ---------- Tab management ----------

interface TerminalTab {
  id: string;
  hostId: number;
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
    hostConfig = detailRes.host;
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
    sessionId = sessionRes.session_id || '';
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

const initTerminal = async (tab: TerminalTab) => {
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
  const wsUrl = await getSSHWebSocketUrl(tab.id, term.cols, term.rows);
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

// ---------- Toolbar actions ----------

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

// ---------- 文件管理弹窗 ----------

const fileManagerVisible = ref(false);
const fileManagerSessionId = ref('');

const openFileManager = () => {
  const tab = activeTab.value;
  if (!tab) return;
  fileManagerSessionId.value = tab.id;
  fileManagerVisible.value = true;
};

// ---------- Lifecycle ----------

onMounted(async () => {
  window.addEventListener('resize', handleWindowResize);

  // Priority 1: Auto-connect if hostId is provided in query
  const hostIdQuery = props.initialHostId || route.query.hostId;
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
    if (props.disconnectOnUnmount) {
      void disconnectSSHSession(tab.id).catch(() => undefined);
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

/* ---------- Toolbar ---------- */

.toolbar {
  display: flex;
  align-items: center;
  justify-content: flex-end;
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
