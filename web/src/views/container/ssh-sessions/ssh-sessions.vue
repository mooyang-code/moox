<template>
  <div class="ssh-sessions-page">
    <div class="page-content">
      <a-card :bordered="false">
        <template #title>
          <span>在线会话列表</span>
          <a-tag
            :color="autoRefresh ? 'green' : 'gray'"
            size="small"
            style="margin-left: 12px; cursor: pointer;"
            @click="toggleAutoRefresh"
          >
            <template #icon>
              <icon-clock-circle />
            </template>
            {{ autoRefresh ? '自动刷新: 开启' : '自动刷新: 关闭' }}
          </a-tag>
        </template>
        <template #extra>
          <a-space>
            <a-button @click="loadSessions" :loading="loading">
              <template #icon>
                <icon-refresh />
              </template>
              刷新
            </a-button>
          </a-space>
        </template>

        <a-table
          row-key="session_id"
          :loading="loading"
          :data="sessions"
          :pagination="false"
          :bordered="false"
        >
          <template #columns>
            <a-table-column title="Session ID" data-index="session_id" :width="200">
              <template #cell="{ record }">
                <a-tooltip :content="record.session_id">
                  <span class="session-id-cell">{{ record.session_id }}</span>
                </a-tooltip>
              </template>
            </a-table-column>
            <a-table-column title="主机名称" data-index="host_name" :width="150" />
            <a-table-column title="主机地址" data-index="address" :width="180">
              <template #cell="{ record }">
                {{ record.address }}:{{ record.port }}
              </template>
            </a-table-column>
            <a-table-column title="用户" data-index="user" :width="120" />
            <a-table-column title="客户端IP" data-index="client_ip" :width="150" />
            <a-table-column title="连接时间" data-index="start_time" :width="180">
              <template #cell="{ record }">
                {{ formatDateTime(record.start_time) }}
              </template>
            </a-table-column>
            <a-table-column title="最后活跃时间" data-index="last_active_time" :width="180">
              <template #cell="{ record }">
                {{ formatDateTime(record.last_active_time) }}
              </template>
            </a-table-column>
            <a-table-column title="操作" :width="130" align="center" fixed="right">
              <template #cell="{ record }">
                <a-popconfirm
                  content="确定要强制断开该会话吗？该操作将立即中断用户的SSH连接。"
                  ok-text="确定断开"
                  cancel-text="取消"
                  type="warning"
                  @ok="() => handleForceDisconnect(record.session_id)"
                  position="tr"
                >
                  <a-button type="outline" status="danger" size="small">
                    <template #icon>
                      <icon-poweroff />
                    </template>
                    强制断开
                  </a-button>
                </a-popconfirm>
              </template>
            </a-table-column>
          </template>

          <template #empty>
            <a-empty description="暂无在线会话" />
          </template>
        </a-table>
      </a-card>
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, onMounted, onBeforeUnmount } from 'vue';
import { Message } from '@arco-design/web-vue';
import { getOnlineSessions, forceDisconnect, type SessionInfo } from '@/api/modules/ssh';

// 状态管理
const loading = ref(false);
const sessions = ref<SessionInfo[]>([]);
const autoRefresh = ref(true);
let refreshTimer: ReturnType<typeof setInterval> | null = null;

const AUTO_REFRESH_INTERVAL = 10000; // 10 seconds

// 加载会话列表
const loadSessions = async () => {
  loading.value = true;
  try {
    const response = await getOnlineSessions();
    const res = response.data;
    if (res.code === 200) {
      sessions.value = res.data || [];
    }
  } catch (error) {
    console.error('加载会话列表失败:', error);
    Message.error('加载会话列表失败');
  } finally {
    loading.value = false;
  }
};

// 强制断开会话
const handleForceDisconnect = async (sessionId: string) => {
  try {
    const response = await forceDisconnect(sessionId);
    const res = response.data;
    if (res.code === 200) {
      Message.success('会话已断开');
      await loadSessions();
    } else {
      Message.error(res.message || '断开会话失败');
    }
  } catch (error) {
    console.error('断开会话失败:', error);
    Message.error('断开会话失败');
  }
};

// 自动刷新
const startAutoRefresh = () => {
  stopAutoRefresh();
  refreshTimer = setInterval(() => {
    loadSessions();
  }, AUTO_REFRESH_INTERVAL);
};

const stopAutoRefresh = () => {
  if (refreshTimer) {
    clearInterval(refreshTimer);
    refreshTimer = null;
  }
};

const toggleAutoRefresh = () => {
  autoRefresh.value = !autoRefresh.value;
  if (autoRefresh.value) {
    startAutoRefresh();
    Message.success('已开启自动刷新');
  } else {
    stopAutoRefresh();
    Message.info('已关闭自动刷新');
  }
};

// 格式化时间
const formatDateTime = (dateTime: string | null) => {
  if (!dateTime) return '-';
  try {
    const date = new Date(dateTime);
    return date.toLocaleString('zh-CN', {
      year: 'numeric',
      month: '2-digit',
      day: '2-digit',
      hour: '2-digit',
      minute: '2-digit',
      second: '2-digit',
    });
  } catch {
    return dateTime;
  }
};

// 生命周期
onMounted(async () => {
  await loadSessions();
  if (autoRefresh.value) {
    startAutoRefresh();
  }
});

onBeforeUnmount(() => {
  stopAutoRefresh();
});
</script>

<style lang="scss" scoped>
.ssh-sessions-page {
  padding: 20px;

  .page-content {
    .session-id-cell {
      display: inline-block;
      max-width: 170px;
      overflow: hidden;
      text-overflow: ellipsis;
      white-space: nowrap;
      word-break: break-all;
      cursor: default;
    }
  }

  :deep(.arco-card) {
    border-radius: 4px;
  }
}
</style>
