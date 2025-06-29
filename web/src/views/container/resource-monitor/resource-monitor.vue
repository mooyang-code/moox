<template>
  <div class="resource-monitor-page">
    <div class="page-header">
      <h2>资源监控</h2>
      <p>实时监控各个容器的CPU、内存、磁盘使用情况</p>
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
              title="磁盘使用率"
              :value="diskUsage"
              suffix="%"
              :value-style="{ color: diskUsage > 80 ? '#f53f3f' : '#0fbf60' }"
            />
          </a-card>
        </a-col>
        
        <!-- 容器资源使用情况 -->
        <a-col :span="24">
          <a-card title="容器资源使用情况" :bordered="false">
            <template #extra>
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
            </template>
            
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
          </a-card>
        </a-col>
        
        <!-- 历史趋势图 -->
        <a-col :span="24">
          <a-card title="资源使用趋势" :bordered="false">
            <div class="chart-container">
              <div class="chart-placeholder">
                <icon-bar-chart style="font-size: 48px; color: var(--color-text-3);" />
                <p>资源使用趋势图</p>
                <p style="color: var(--color-text-3); font-size: 14px;">
                  实际使用时这里会显示CPU、内存使用率的历史趋势图表
                </p>
              </div>
            </div>
          </a-card>
        </a-col>
      </a-row>
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, onMounted, onUnmounted } from 'vue';
import { Message } from '@arco-design/web-vue';

// 状态管理
const loading = ref(false);
const autoRefresh = ref(false);
let refreshTimer: NodeJS.Timeout | null = null;

// 容器数据
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
    diskWrite: '0.2 MB/s'
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
    diskWrite: '1.8 MB/s'
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
    diskWrite: '0 MB/s'
  }
]);

// 计算总览数据
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
const diskUsage = computed(() => 75); // 模拟磁盘使用率

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

// 刷新数据
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
    
    Message.success('数据已刷新');
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
      refreshData();
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
  
  .chart-container {
    height: 300px;
    
    .chart-placeholder {
      height: 100%;
      display: flex;
      flex-direction: column;
      justify-content: center;
      align-items: center;
      color: var(--color-text-2);
      
      p {
        margin: 8px 0;
      }
    }
  }
}
</style>
