<template>
  <div class="resource-monitor-page">
    <div class="page-header">
      <h2>容器监控</h2>
      <p>实时监控容器资源使用情况和服务运行状态</p>
    </div>

    <div class="page-content">
      <a-row :gutter="20">
        <!-- 总览卡片 -->
        <a-col :span="6">
          <a-card :bordered="false" class="overview-card">
            <a-statistic
              title="运行中容器"
              :value="runningContainers"
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
              title="运行中服务"
              :value="runningServices"
              :value-style="{ color: '#0fbf60' }"
            >
              <template #suffix>
                <span>/ {{ totalServices }}</span>
              </template>
            </a-statistic>
          </a-card>
        </a-col>

        <!-- 主要内容区域 - Tab标签页 -->
        <a-col :span="24">
          <a-card :bordered="false">
            <a-tabs v-model:active-key="activeTab" type="rounded">
              <!-- 资源监控标签页 -->
              <a-tab-pane key="resource" title="资源监控">
                <div class="tab-content">
                  <div class="tab-header">
                    <a-space>
                      <a-button @click="refreshData" :loading="loading">
                        <template #icon>
                          <icon-refresh />
                        </template>
                        刷新
                      </a-button>
                      <a-switch v-model="autoRefresh" @change="toggleAutoRefresh">
                        <template #checked>自动刷新</template>
                        <template #unchecked>手动刷新</template>
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
                          <h4>{{ container.name }}</h4>
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
                                :percent="container.cpuUsage"
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
                                :percent="container.memoryUsage"
                                :color="getProgressColor(container.memoryUsage)"
                                :show-text="false"
                                size="small"
                              />
                              <span class="metric-text">{{ container.memoryUsage }}%</span>
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

                          <!-- 磁盘I/O -->
                          <div class="metric-item">
                            <div class="metric-label">磁盘I/O</div>
                            <div class="metric-value">
                              <div class="disk-io">
                                <span class="io-item">
                                  <icon-arrow-up style="color: #0fbf60;" />
                                  {{ container.diskRead }}
                                </span>
                                <span class="io-item">
                                  <icon-arrow-down style="color: #f53f3f;" />
                                  {{ container.diskWrite }}
                                </span>
                              </div>
                            </div>
                          </div>
                        </div>

                        <div class="card-footer">
                          <a-space>
                            <a-button size="small" @click="viewDetails(container)">
                              详细信息
                            </a-button>
                            <a-button size="small" @click="viewLogs(container)">
                              查看日志
                            </a-button>
                          </a-space>
                        </div>
                      </div>
                    </a-col>
                  </a-row>

                  <!-- 历史趋势图 -->
                  <a-row :gutter="20" style="margin-top: 20px;">
                    <a-col :span="24">
                      <div class="chart-section">
                        <h4>资源使用趋势</h4>
                        <div class="chart-container">
                          <div class="chart-placeholder">
                            <icon-bar-chart style="font-size: 48px; color: var(--color-text-3);" />
                            <p>资源使用趋势图</p>
                            <p style="color: var(--color-text-3); font-size: 14px;">
                              实际使用时这里会显示CPU、内存使用率的历史趋势图表
                            </p>
                          </div>
                        </div>
                      </div>
                    </a-col>
                  </a-row>
                </div>
              </a-tab-pane>

              <!-- 服务状态标签页 -->
              <a-tab-pane key="service" title="服务状态">
                <div class="tab-content">
                  <div class="tab-header">
                    <a-space>
                      <a-button @click="refreshServices" :loading="loading">
                        <template #icon>
                          <icon-refresh />
                        </template>
                        刷新
                      </a-button>
                      <a-select
                        v-model="filterStatus"
                        placeholder="筛选状态"
                        style="width: 120px;"
                        allow-clear
                      >
                        <a-option value="running">运行中</a-option>
                        <a-option value="stopped">已停止</a-option>
                        <a-option value="error">异常</a-option>
                      </a-select>
                    </a-space>
                  </div>

                  <!-- 服务状态总览 -->
                  <a-row :gutter="20" style="margin-bottom: 20px;">
                    <a-col :span="6">
                      <div class="stat-card">
                        <div class="stat-label">总服务数</div>
                        <div class="stat-value" style="color: #1d39c4;">{{ totalServices }}</div>
                      </div>
                    </a-col>
                    <a-col :span="6">
                      <div class="stat-card">
                        <div class="stat-label">运行中</div>
                        <div class="stat-value" style="color: #0fbf60;">{{ runningServices }}</div>
                      </div>
                    </a-col>
                    <a-col :span="6">
                      <div class="stat-card">
                        <div class="stat-label">已停止</div>
                        <div class="stat-value" style="color: #f53f3f;">{{ stoppedServices }}</div>
                      </div>
                    </a-col>
                    <a-col :span="6">
                      <div class="stat-card">
                        <div class="stat-label">异常</div>
                        <div class="stat-value" style="color: #ff7d00;">{{ errorServices }}</div>
                      </div>
                    </a-col>
                  </a-row>

                  <!-- 按容器分组的服务状态 -->
                  <a-collapse :default-active-key="['container-001', 'container-002']">
                    <a-collapse-item
                      v-for="container in containers"
                      :key="container.id"
                      :header="getContainerHeader(container)"
                    >
                      <template #extra>
                        <a-tag :color="getContainerStatusColor(container.status)">
                          {{ getContainerStatusText(container.status) }}
                        </a-tag>
                      </template>

                      <a-table
                        :columns="serviceColumns"
                        :data="getFilteredServices(container.services)"
                        :pagination="false"
                        size="small"
                      >
                        <template #status="{ record }">
                          <a-tag :color="getServiceStatusColor(record.status)">
                            {{ getServiceStatusText(record.status) }}
                          </a-tag>
                        </template>

                        <template #health="{ record }">
                          <div class="health-indicator">
                            <div
                              class="health-dot"
                              :class="getHealthClass(record.health)"
                            ></div>
                            <span>{{ record.health }}</span>
                          </div>
                        </template>

                        <template #actions="{ record }">
                          <a-space>
                            <a-button
                              type="text"
                              size="small"
                              @click="startService(container, record)"
                              v-if="record.status === 'stopped'"
                            >
                              启动
                            </a-button>
                            <a-button
                              type="text"
                              size="small"
                              @click="stopService(container, record)"
                              v-if="record.status === 'running'"
                            >
                              停止
                            </a-button>
                            <a-button
                              type="text"
                              size="small"
                              @click="restartService(container, record)"
                            >
                              重启
                            </a-button>
                            <a-button
                              type="text"
                              size="small"
                              @click="viewServiceLogs(container, record)"
                            >
                              日志
                            </a-button>
                          </a-space>
                        </template>
                      </a-table>
                    </a-collapse-item>
                  </a-collapse>

                  <!-- 服务监控图表 -->
                  <a-row :gutter="20" style="margin-top: 20px;">
                    <a-col :span="12">
                      <div class="chart-section">
                        <h4>服务状态分布</h4>
                        <div class="chart-container">
                          <div class="chart-placeholder">
                            <icon-pie-chart style="font-size: 48px; color: var(--color-text-3);" />
                            <p>服务状态饼图</p>
                          </div>
                        </div>
                      </div>
                    </a-col>
                    <a-col :span="12">
                      <div class="chart-section">
                        <h4>服务响应时间</h4>
                        <div class="chart-container">
                          <div class="chart-placeholder">
                            <icon-line-chart style="font-size: 48px; color: var(--color-text-3);" />
                            <p>响应时间趋势图</p>
                          </div>
                        </div>
                      </div>
                    </a-col>
                  </a-row>
                </div>
              </a-tab-pane>
            </a-tabs>
          </a-card>
        </a-col>
      </a-row>
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, onMounted, onUnmounted } from 'vue';
import { Message, Modal } from '@arco-design/web-vue';

// 状态管理
const loading = ref(false);
const autoRefresh = ref(false);
const activeTab = ref('resource');
const filterStatus = ref('');
let refreshTimer: NodeJS.Timeout | null = null;

// 容器数据 - 整合资源和服务信息
const containers = ref([
  {
    id: 'container-001',
    name: 'moox-backend-1',
    status: 'running',
    cpuUsage: 15,
    memoryUsage: 45,
    networkIn: '1.2 MB/s',
    networkOut: '0.8 MB/s',
    diskRead: '0.5 MB/s',
    diskWrite: '0.2 MB/s',
    services: [
      {
        id: 'service-001',
        name: 'moox-api',
        status: 'running',
        health: 'healthy',
        port: '8080',
        uptime: '2d 5h 30m',
        responseTime: '45ms'
      },
      {
        id: 'service-002',
        name: 'nginx',
        status: 'running',
        health: 'healthy',
        port: '80',
        uptime: '2d 5h 32m',
        responseTime: '12ms'
      }
    ]
  },
  {
    id: 'container-002',
    name: 'moox-database-1',
    status: 'running',
    cpuUsage: 8,
    memoryUsage: 62,
    networkIn: '0.3 MB/s',
    networkOut: '0.1 MB/s',
    diskRead: '2.1 MB/s',
    diskWrite: '1.8 MB/s',
    services: [
      {
        id: 'service-003',
        name: 'postgresql',
        status: 'running',
        health: 'healthy',
        port: '5432',
        uptime: '2d 5h 25m',
        responseTime: '8ms'
      }
    ]
  },
  {
    id: 'container-003',
    name: 'moox-redis-1',
    status: 'stopped',
    cpuUsage: 0,
    memoryUsage: 0,
    networkIn: '0 MB/s',
    networkOut: '0 MB/s',
    diskRead: '0 MB/s',
    diskWrite: '0 MB/s',
    services: [
      {
        id: 'service-004',
        name: 'redis-server',
        status: 'stopped',
        health: 'unhealthy',
        port: '6379',
        uptime: '0m',
        responseTime: '-'
      }
    ]
  }
]);

// 计算总览数据 - 容器相关
const totalContainers = computed(() => containers.value.length);
const runningContainers = computed(() =>
  containers.value.filter(c => c.status === 'running').length
);
const averageCpuUsage = computed(() => {
  const runningContainersList = containers.value.filter(c => c.status === 'running');
  if (runningContainersList.length === 0) return 0;
  const total = runningContainersList.reduce((sum, c) => sum + c.cpuUsage, 0);
  return Math.round(total / runningContainersList.length);
});
const averageMemoryUsage = computed(() => {
  const runningContainersList = containers.value.filter(c => c.status === 'running');
  if (runningContainersList.length === 0) return 0;
  const total = runningContainersList.reduce((sum, c) => sum + c.memoryUsage, 0);
  return Math.round(total / runningContainersList.length);
});

// 计算总览数据 - 服务相关
const totalServices = computed(() => {
  return containers.value.reduce((total, container) => total + container.services.length, 0);
});

const runningServices = computed(() => {
  return containers.value.reduce((total, container) =>
    total + container.services.filter(s => s.status === 'running').length, 0
  );
});

const stoppedServices = computed(() => {
  return containers.value.reduce((total, container) =>
    total + container.services.filter(s => s.status === 'stopped').length, 0
  );
});

const errorServices = computed(() => {
  return containers.value.reduce((total, container) =>
    total + container.services.filter(s => s.status === 'error').length, 0
  );
});

// 服务表格列配置
const serviceColumns = [
  {
    title: '服务名称',
    dataIndex: 'name',
    key: 'name'
  },
  {
    title: '状态',
    dataIndex: 'status',
    key: 'status',
    slotName: 'status'
  },
  {
    title: '健康状态',
    dataIndex: 'health',
    key: 'health',
    slotName: 'health'
  },
  {
    title: '端口',
    dataIndex: 'port',
    key: 'port'
  },
  {
    title: '运行时间',
    dataIndex: 'uptime',
    key: 'uptime'
  },
  {
    title: '响应时间',
    dataIndex: 'responseTime',
    key: 'responseTime'
  },
  {
    title: '操作',
    key: 'actions',
    slotName: 'actions'
  }
];

// 获取容器标题
const getContainerHeader = (container: any) => {
  const serviceCount = container.services.length;
  const runningCount = container.services.filter((s: any) => s.status === 'running').length;
  return `${container.name} (${runningCount}/${serviceCount} 运行中)`;
};

// 获取容器状态颜色
const getContainerStatusColor = (status: string) => {
  switch (status) {
    case 'running':
      return 'green';
    case 'stopped':
      return 'red';
    default:
      return 'gray';
  }
};

// 获取容器状态文本
const getContainerStatusText = (status: string) => {
  switch (status) {
    case 'running':
      return '运行中';
    case 'stopped':
      return '已停止';
    default:
      return '未知';
  }
};

// 获取状态颜色
const getStatusColor = (status: string) => {
  switch (status) {
    case 'running':
      return 'green';
    case 'stopped':
      return 'red';
    case 'paused':
      return 'orange';
    default:
      return 'gray';
  }
};

// 获取状态文本
const getStatusText = (status: string) => {
  switch (status) {
    case 'running':
      return '运行中';
    case 'stopped':
      return '已停止';
    case 'paused':
      return '已暂停';
    default:
      return '未知';
  }
};

// 获取进度条颜色
const getProgressColor = (value: number) => {
  if (value > 80) return '#f53f3f';
  if (value > 60) return '#ff7d00';
  return '#0fbf60';
};

// 获取服务状态颜色
const getServiceStatusColor = (status: string) => {
  switch (status) {
    case 'running':
      return 'green';
    case 'stopped':
      return 'red';
    case 'error':
      return 'orange';
    default:
      return 'gray';
  }
};

// 获取服务状态文本
const getServiceStatusText = (status: string) => {
  switch (status) {
    case 'running':
      return '运行中';
    case 'stopped':
      return '已停止';
    case 'error':
      return '异常';
    default:
      return '未知';
  }
};

// 获取健康状态样式类
const getHealthClass = (health: string) => {
  switch (health) {
    case 'healthy':
      return 'health-healthy';
    case 'unhealthy':
      return 'health-unhealthy';
    case 'warning':
      return 'health-warning';
    default:
      return 'health-unknown';
  }
};

// 过滤服务
const getFilteredServices = (services: any[]) => {
  if (!filterStatus.value) return services;
  return services.filter(service => service.status === filterStatus.value);
};

// 刷新资源数据
const refreshData = async () => {
  loading.value = true;
  try {
    // 模拟API调用
    await new Promise(resolve => setTimeout(resolve, 1000));

    // 模拟数据更新
    containers.value.forEach(container => {
      if (container.status === 'running') {
        container.cpuUsage = Math.floor(Math.random() * 30) + 5;
        container.memoryUsage = Math.floor(Math.random() * 40) + 30;
      }
    });

    Message.success('资源数据已刷新');
  } catch (error) {
    Message.error('刷新失败');
  } finally {
    loading.value = false;
  }
};

// 刷新服务状态
const refreshServices = async () => {
  loading.value = true;
  try {
    // 模拟API调用
    await new Promise(resolve => setTimeout(resolve, 1000));
    Message.success('服务状态已刷新');
  } catch (error) {
    Message.error('刷新失败');
  } finally {
    loading.value = false;
  }
};

// 切换自动刷新
const toggleAutoRefresh = (enabled: boolean) => {
  if (enabled) {
    refreshTimer = setInterval(() => {
      if (activeTab.value === 'resource') {
        refreshData();
      } else {
        refreshServices();
      }
    }, 5000); // 每5秒刷新一次
    Message.info('已开启自动刷新（每5秒）');
  } else {
    if (refreshTimer) {
      clearInterval(refreshTimer);
      refreshTimer = null;
    }
    Message.info('已关闭自动刷新');
  }
};

// 查看详细信息
const viewDetails = (container: any) => {
  Message.info(`查看容器 ${container.name} 的详细信息`);
};

// 查看日志
const viewLogs = (container: any) => {
  Message.info(`查看容器 ${container.name} 的日志`);
};

// 启动服务
const startService = (_container: any, service: any) => {
  Modal.confirm({
    title: '确认启动',
    content: `确定要启动服务 ${service.name} 吗？`,
    onOk: () => {
      service.status = 'running';
      service.health = 'healthy';
      Message.success(`服务 ${service.name} 启动成功`);
    }
  });
};

// 停止服务
const stopService = (_container: any, service: any) => {
  Modal.confirm({
    title: '确认停止',
    content: `确定要停止服务 ${service.name} 吗？`,
    onOk: () => {
      service.status = 'stopped';
      service.health = 'unhealthy';
      Message.success(`服务 ${service.name} 停止成功`);
    }
  });
};

// 重启服务
const restartService = (_container: any, service: any) => {
  Modal.confirm({
    title: '确认重启',
    content: `确定要重启服务 ${service.name} 吗？`,
    onOk: () => {
      Message.success(`服务 ${service.name} 重启成功`);
    }
  });
};

// 查看服务日志
const viewServiceLogs = (_container: any, service: any) => {
  Message.info(`查看服务 ${service.name} 的日志`);
};

onMounted(() => {
  refreshData();
});

onUnmounted(() => {
  if (refreshTimer) {
    clearInterval(refreshTimer);
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
      align-items: center;
      margin-bottom: 16px;

      h4 {
        margin: 0;
        font-size: 16px;
        font-weight: 600;
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

          .metric-text {
            font-size: 12px;
            font-weight: 500;
            min-width: 35px;
          }
        }

        .network-io,
        .disk-io {
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

    .card-footer {
      margin-top: 16px;
      padding-top: 12px;
      border-top: 1px solid var(--color-border-2);
    }
  }

  .stat-card {
    background: var(--color-bg-2);
    border: 1px solid var(--color-border-2);
    border-radius: 8px;
    padding: 16px;
    text-align: center;

    .stat-label {
      font-size: 14px;
      color: var(--color-text-2);
      margin-bottom: 8px;
    }

    .stat-value {
      font-size: 28px;
      font-weight: 600;
    }
  }

  .health-indicator {
    display: flex;
    align-items: center;
    gap: 8px;

    .health-dot {
      width: 8px;
      height: 8px;
      border-radius: 50%;

      &.health-healthy {
        background-color: #0fbf60;
      }

      &.health-unhealthy {
        background-color: #f53f3f;
      }

      &.health-warning {
        background-color: #ff7d00;
      }

      &.health-unknown {
        background-color: #86909c;
      }
    }
  }

  .chart-section {
    h4 {
      margin: 0 0 16px 0;
      font-size: 16px;
      font-weight: 600;
    }
  }

  .chart-container {
    height: 200px;

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

  :deep(.arco-collapse-item-header) {
    font-weight: 500;
  }

  :deep(.arco-tabs-content) {
    padding-top: 20px;
  }
}
</style>
