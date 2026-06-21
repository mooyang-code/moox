<template>
  <div class="moox-page resource-monitor-page">
    <div class="page-header">
      <h2>主机监控</h2>
      <p>实时监控主机资源使用情况</p>
    </div>
    <SpaceContextBar />

    <div class="page-content">
      <a-row :gutter="20">
        <!-- 总览卡片 -->
        <a-col :span="6">
          <a-card :bordered="false" class="overview-card">
            <a-statistic
              title="在线主机"
              :value="onlineHosts"
              :value-style="{ color: '#0fbf60' }"
            >
              <template #suffix>
                <span>/ {{ totalContainers }}</span>
              </template>
            </a-statistic>
          </a-card>
        </a-col>
        <a-col :span="6">
          <a-card :bordered="false" class="overview-card">
            <a-statistic
              title="平均CPU使用率"
              :value="averageCpuUsage"
              suffix="%"
              :value-style="{ color: averageCpuUsage > 80 ? '#f53f3f' : '#0fbf60' }"
            />
          </a-card>
        </a-col>
        <a-col :span="6">
          <a-card :bordered="false" class="overview-card">
            <a-statistic
              title="平均内存使用率"
              :value="averageMemoryUsage"
              suffix="%"
              :value-style="{ color: averageMemoryUsage > 80 ? '#f53f3f' : '#0fbf60' }"
            />
          </a-card>
        </a-col>
        <a-col :span="6">
          <a-card :bordered="false" class="overview-card">
            <a-statistic
              title="平均磁盘使用率"
              :value="averageDiskUsage"
              suffix="%"
              :value-style="{ color: averageDiskUsage > 80 ? '#f53f3f' : '#0fbf60' }"
            />
          </a-card>
        </a-col>

        <!-- 主要内容区域 -->
        <a-col :span="24">
          <a-card :bordered="false">
            <div class="tab-content">
              <div class="tab-header">
                <span class="auto-refresh-hint" v-if="autoRefresh">
                  <icon-sync :spin="true" /> 每 5 秒自动刷新
                </span>
                <a-space>
                  <a-button @click="manualRefresh" :loading="loading">
                    <template #icon>
                      <icon-refresh />
                    </template>
                    刷新
                  </a-button>
                  <a-switch v-model="autoRefresh" @change="toggleAutoRefresh">
                    <template #checked>自动</template>
                    <template #unchecked>手动</template>
                  </a-switch>
                </a-space>
              </div>

              <a-row :gutter="20">
                <a-col
                  v-for="container in containers"
                  :key="container.id"
                  :span="8"
                  class="container-card-col"
                >
                  <div class="container-resource-card">
                    <div class="card-header">
                      <div>
                        <h4>{{ container.name }}</h4>
                        <span class="host-address">{{ container.address }}</span>
                      </div>
                      <a-tag :color="getStatusColor(container.status)">
                        {{ getStatusText(container.status) }}
                      </a-tag>
                    </div>

                    <div class="resource-metrics">
                      <!-- CPU使用率 -->
                      <div class="metric-item">
                        <div class="metric-label">CPU使用率</div>
                        <div class="metric-value">
                          <a-progress
                            :percent="container.cpuUsage / 100"
                            :color="getProgressColor(container.cpuUsage)"
                            :show-text="false"
                            size="small"
                          />
                          <span class="metric-text">{{ container.cpuUsage }}%</span>
                        </div>
                      </div>

                      <!-- 内存使用率 -->
                      <div class="metric-item">
                        <div class="metric-label">内存使用率</div>
                        <div class="metric-value">
                          <a-progress
                            :percent="container.memoryUsage / 100"
                            :color="getProgressColor(container.memoryUsage)"
                            :show-text="false"
                            size="small"
                          />
                          <span class="metric-text">{{ container.memoryUsage }}%</span>
                        </div>
                      </div>

                      <!-- 磁盘使用率 -->
                      <div class="metric-item">
                        <div class="metric-label">磁盘使用率</div>
                        <div class="metric-value">
                          <a-progress
                            :percent="container.diskUsage / 100"
                            :color="getProgressColor(container.diskUsage)"
                            :show-text="false"
                            size="small"
                          />
                          <span class="metric-text">{{ container.diskUsage }}%</span>
                        </div>
                      </div>

                      <!-- 网络I/O -->
                      <div class="metric-item">
                        <div class="metric-label">网络I/O</div>
                        <div class="metric-value">
                          <div class="network-io">
                            <span class="io-item">
                              <icon-arrow-up style="color: #0fbf60;" />
                              {{ container.networkIn }}
                            </span>
                            <span class="io-item">
                              <icon-arrow-down style="color: #f53f3f;" />
                              {{ container.networkOut }}
                            </span>
                          </div>
                        </div>
                      </div>
                    </div>
                  </div>
                </a-col>
              </a-row>

              <!-- 历史趋势图 -->
              <a-row :gutter="20" style="margin-top: 20px;" v-if="containers.length > 0">
                <a-col :span="24">
                  <div class="chart-section">
                    <div class="chart-section-header">
                      <h4>资源使用趋势</h4>
                      <a-space>
                        <a-select
                          v-model="selectedHostAddress"
                          placeholder="选择主机"
                          style="width: 200px;"
                          @change="loadHistory"
                        >
                          <a-option
                            v-for="c in containers"
                            :key="c.address"
                            :value="c.address"
                          >
                            {{ c.name }} ({{ c.address }})
                          </a-option>
                        </a-select>
                        <a-radio-group v-model="historyDuration" type="button" size="small" @change="loadHistory">
                          <a-radio value="1h">1小时</a-radio>
                          <a-radio value="24h">24小时</a-radio>
                          <a-radio value="7d">7天</a-radio>
                        </a-radio-group>
                      </a-space>
                    </div>
                    <div class="chart-container" ref="trendChartRef">
                      <div v-if="historyLoading" class="chart-placeholder">
                        <a-spin />
                      </div>
                      <div v-else-if="historyData.length === 0" class="chart-placeholder">
                        <icon-bar-chart style="font-size: 48px; color: var(--color-text-3);" />
                        <p>暂无历史数据</p>
                      </div>
                    </div>
                  </div>
                </a-col>
              </a-row>
            </div>
          </a-card>
        </a-col>
      </a-row>
    </div>
  </div>
</template>

<script setup lang="ts">
import SpaceContextBar from '@/components/SpaceContextBar/index.vue';
import { ref, computed, onMounted, onUnmounted, watch, nextTick } from 'vue';
import { Message } from '@arco-design/web-vue';
import { default as VChart } from '@visactor/vchart';
import {
  getCurrentMetrics,
  getHistoryMetrics,
  type HostMetrics,
  type HistoryPoint,
  formatBytesPerSecond
} from '@/api/modules/host-monitor';

// 状态管理
const loading = ref(false);
const autoRefresh = ref(true);
let refreshTimer: NodeJS.Timeout | null = null;

// 主机监控数据
const hostMetrics = ref<HostMetrics[]>([]);

// 历史趋势图
const selectedHostAddress = ref('');
const historyDuration = ref('1h');
const historyData = ref<HistoryPoint[]>([]);
const historyLoading = ref(false);
const trendChartRef = ref<HTMLElement>();
let trendChart: VChart | null = null;

// 将主机监控数据转换为显示格式
const containers = computed(() => {
  return hostMetrics.value.map(host => {
    const networkIn = host.networks && host.networks.length > 0
      ? formatBytesPerSecond(host.networks[0].rx_speed)
      : '0 B/s';
    const networkOut = host.networks && host.networks.length > 0
      ? formatBytesPerSecond(host.networks[0].tx_speed)
      : '0 B/s';

    // 取第一个磁盘分区的使用率
    const diskUsage = host.disks && host.disks.length > 0
      ? Math.round(host.disks[0].percent)
      : 0;

    return {
      id: `host-${host.host_id}`,
      name: host.host_name,
      address: host.address,
      status: host.status === 'online' ? 'running' : host.status === 'offline' ? 'stopped' : 'error',
      cpuUsage: Math.round(host.cpu?.usage || 0),
      memoryUsage: Math.round(host.memory?.percent || 0),
      diskUsage,
      networkIn,
      networkOut,
    };
  });
});

// 计算总览数据
const totalContainers = computed(() => containers.value.length);
const onlineHosts = computed(() =>
  hostMetrics.value.filter(h => h.status === 'online').length
);
const averageCpuUsage = computed(() => {
  const list = hostMetrics.value.filter(h => h.status === 'online');
  if (list.length === 0) return 0;
  return Math.round(list.reduce((sum, h) => sum + (h.cpu?.usage || 0), 0) / list.length);
});
const averageMemoryUsage = computed(() => {
  const list = hostMetrics.value.filter(h => h.status === 'online');
  if (list.length === 0) return 0;
  return Math.round(list.reduce((sum, h) => sum + (h.memory?.percent || 0), 0) / list.length);
});
const averageDiskUsage = computed(() => {
  const list = hostMetrics.value.filter(h => h.status === 'online' && h.disks && h.disks.length > 0);
  if (list.length === 0) return 0;
  return Math.round(list.reduce((sum, h) => sum + (h.disks[0]?.percent || 0), 0) / list.length);
});

// 获取状态颜色
const getStatusColor = (status: string) => {
  switch (status) {
    case 'running': return 'green';
    case 'stopped': return 'red';
    default: return 'gray';
  }
};

// 获取状态文本
const getStatusText = (status: string) => {
  switch (status) {
    case 'running': return '运行中';
    case 'stopped': return '已停止';
    default: return '未知';
  }
};

// 获取进度条颜色
const getProgressColor = (value: number) => {
  if (value > 80) return '#f53f3f';
  if (value > 60) return '#ff7d00';
  return '#0fbf60';
};

// 静默刷新（自动刷新时不弹 Message）
const refreshData = async (silent = false) => {
  loading.value = true;
  try {
    const response = await getCurrentMetrics();
    const res = response.data;
    if (res.code === 0 || res.code === 200) {
      hostMetrics.value = res.data || [];
      if (!silent) {
        Message.success('资源数据已刷新');
      }
    } else if (!silent) {
      Message.error(res.msg || '获取监控数据失败');
    }
  } catch (error) {
    console.error('获取监控数据失败:', error);
    if (!silent) {
      Message.error('刷新失败，请检查网络连接');
    }
  } finally {
    loading.value = false;
  }
};

// 手动刷新（带提示）
const manualRefresh = () => {
  refreshData(false);
  loadHistory();
};

// 自动刷新控制
const startAutoRefresh = () => {
  if (refreshTimer) clearInterval(refreshTimer);
  refreshTimer = setInterval(() => {
    refreshData(true);
  }, 5000);
};

const stopAutoRefresh = () => {
  if (refreshTimer) {
    clearInterval(refreshTimer);
    refreshTimer = null;
  }
};

const toggleAutoRefresh = (enabled: boolean) => {
  if (enabled) {
    startAutoRefresh();
  } else {
    stopAutoRefresh();
  }
};

// ========== 历史趋势图 ==========

const loadHistory = async () => {
  if (!selectedHostAddress.value) return;

  historyLoading.value = true;
  try {
    const response = await getHistoryMetrics(selectedHostAddress.value, historyDuration.value);
    const res = response.data;
    if (res.code === 0 || res.code === 200) {
      historyData.value = res.data || [];
      await nextTick();
      renderTrendChart();
    }
  } catch (error) {
    console.error('获取历史数据失败:', error);
  } finally {
    historyLoading.value = false;
  }
};

const renderTrendChart = () => {
  if (!trendChartRef.value || historyData.value.length === 0) return;

  // 销毁旧图表
  if (trendChart) {
    trendChart.release();
    trendChart = null;
  }

  // 转换数据：每个指标一条线
  const chartData: { time: string; value: number; type: string }[] = [];
  for (const point of historyData.value) {
    const time = new Date(point.timestamp).toLocaleTimeString('zh-CN', { hour: '2-digit', minute: '2-digit', second: '2-digit' });
    chartData.push({ time, value: Math.round(point.cpu_usage * 100) / 100, type: 'CPU' });
    chartData.push({ time, value: Math.round(point.memory_percent * 100) / 100, type: '内存' });
    chartData.push({ time, value: Math.round(point.disk_percent * 100) / 100, type: '磁盘' });
  }

  const spec = {
    type: 'line' as const,
    data: [{ id: 'trend', values: chartData }],
    xField: 'time',
    yField: 'value',
    seriesField: 'type',
    line: { style: { lineWidth: 2, curveType: 'monotone' } },
    point: { visible: false },
    legends: { visible: true, orient: 'top' as const },
    axes: [
      {
        orient: 'left' as const,
        title: { visible: true, text: '使用率 (%)' },
        min: 0,
        max: 100,
      },
      {
        orient: 'bottom' as const,
        sampling: true,
        label: { style: { fontSize: 10 } },
      },
    ],
    tooltip: {
      mark: {
        content: [
          {
            key: (datum: any) => datum.type,
            value: (datum: any) => datum.value + '%',
          },
        ],
      },
    },
    color: ['#3491FA', '#6BC76D', '#FF7D00'],
    crosshair: { xField: { visible: true } },
  };

  trendChart = new VChart(spec as any, { dom: trendChartRef.value });
  trendChart.renderSync();
};

// 首个主机加载后自动选中并加载历史
watch(containers, (val) => {
  if (val.length > 0 && !selectedHostAddress.value) {
    selectedHostAddress.value = val[0].address;
    loadHistory();
  }
});

onMounted(() => {
  refreshData(true);
  startAutoRefresh();
});

onUnmounted(() => {
  stopAutoRefresh();
  if (trendChart) {
    trendChart.release();
    trendChart = null;
  }
});
</script>

<style lang="scss" scoped>
.resource-monitor-page {
  padding: 20px;

  .page-header {
    margin-bottom: 20px;

    h2 {
      margin: 0 0 8px 0;
      font-size: 24px;
      font-weight: 600;
    }

    p {
      margin: 0;
      color: var(--color-text-2);
    }
  }

  .overview-card {
    margin-bottom: 20px;
    box-shadow: 0 2px 8px rgba(0, 0, 0, 0.1);
  }

  .tab-content {
    .tab-header {
      margin-bottom: 20px;
      display: flex;
      justify-content: flex-end;
      align-items: center;
      gap: 16px;

      .auto-refresh-hint {
        font-size: 12px;
        color: var(--color-text-3);
        display: flex;
        align-items: center;
        gap: 4px;
      }
    }
  }

  .container-card-col {
    margin-bottom: 20px;
  }

  .container-resource-card {
    background: var(--color-bg-2);
    border: 1px solid var(--color-border-2);
    border-radius: 8px;
    padding: 16px;
    height: 100%;

    .card-header {
      display: flex;
      justify-content: space-between;
      align-items: flex-start;
      margin-bottom: 16px;

      h4 {
        margin: 0;
        font-size: 16px;
        font-weight: 600;
      }

      .host-address {
        font-size: 12px;
        color: var(--color-text-3);
      }
    }

    .resource-metrics {
      .metric-item {
        margin-bottom: 12px;

        .metric-label {
          font-size: 12px;
          color: var(--color-text-2);
          margin-bottom: 4px;
        }

        .metric-value {
          display: flex;
          align-items: center;
          gap: 8px;

          :deep(.arco-progress) {
            flex: 1;
          }

          .metric-text {
            font-size: 12px;
            font-weight: 500;
            min-width: 35px;
            text-align: right;
          }
        }

        .network-io {
          display: flex;
          gap: 12px;

          .io-item {
            display: flex;
            align-items: center;
            gap: 4px;
            font-size: 12px;
          }
        }
      }
    }
  }

  .chart-section {
    .chart-section-header {
      display: flex;
      justify-content: space-between;
      align-items: center;
      margin-bottom: 16px;

      h4 {
        margin: 0;
        font-size: 16px;
        font-weight: 600;
      }
    }
  }

  .chart-container {
    height: 320px;

    .chart-placeholder {
      height: 100%;
      display: flex;
      flex-direction: column;
      justify-content: center;
      align-items: center;
      color: var(--color-text-2);
      border: 1px dashed var(--color-border-2);
      border-radius: 4px;

      p {
        margin: 8px 0;
      }
    }
  }

  :deep(.arco-tabs-content) {
    padding-top: 20px;
  }
}
</style>
