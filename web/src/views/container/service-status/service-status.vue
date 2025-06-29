<template>
  <div class="service-status-page">
    <div class="page-header">
      <h2>运行状态</h2>
      <p>监控各个容器上运行的服务状态</p>
    </div>
    
    <div class="page-content">
      <a-row :gutter="20">
        <!-- 服务状态总览 -->
        <a-col :span="24">
          <a-card title="服务状态总览" :bordered="false" class="overview-card">
            <a-row :gutter="20">
              <a-col :span="6">
                <a-statistic
                  title="总服务数"
                  :value="totalServices"
                  :value-style="{ color: '#1d39c4' }"
                />
              </a-col>
              <a-col :span="6">
                <a-statistic
                  title="运行中"
                  :value="runningServices"
                  :value-style="{ color: '#0fbf60' }"
                />
              </a-col>
              <a-col :span="6">
                <a-statistic
                  title="已停止"
                  :value="stoppedServices"
                  :value-style="{ color: '#f53f3f' }"
                />
              </a-col>
              <a-col :span="6">
                <a-statistic
                  title="异常"
                  :value="errorServices"
                  :value-style="{ color: '#ff7d00' }"
                />
              </a-col>
            </a-row>
          </a-card>
        </a-col>
        
        <!-- 按容器分组的服务状态 -->
        <a-col :span="24">
          <a-card title="容器服务状态" :bordered="false">
            <template #extra>
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
            </template>
            
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
          </a-card>
        </a-col>
        
        <!-- 服务监控图表 -->
        <a-col :span="24">
          <a-card title="服务监控" :bordered="false">
            <a-row :gutter="20">
              <a-col :span="12">
                <div class="chart-container">
                  <h4>服务状态分布</h4>
                  <div class="chart-placeholder">
                    <icon-pie-chart style="font-size: 48px; color: var(--color-text-3);" />
                    <p>服务状态饼图</p>
                  </div>
                </div>
              </a-col>
              <a-col :span="12">
                <div class="chart-container">
                  <h4>服务响应时间</h4>
                  <div class="chart-placeholder">
                    <icon-line-chart style="font-size: 48px; color: var(--color-text-3);" />
                    <p>响应时间趋势图</p>
                  </div>
                </div>
              </a-col>
            </a-row>
          </a-card>
        </a-col>
      </a-row>
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, onMounted } from 'vue';
import { Message, Modal } from '@arco-design/web-vue';

// 状态管理
const loading = ref(false);
const filterStatus = ref('');

// 容器和服务数据
const containers = ref([
  {
    id: 'container-001',
    name: 'moox-backend-1',
    status: 'running',
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

// 计算总览数据
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

// 启动服务
const startService = (container: any, service: any) => {
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
const stopService = (container: any, service: any) => {
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
const restartService = (container: any, service: any) => {
  Modal.confirm({
    title: '确认重启',
    content: `确定要重启服务 ${service.name} 吗？`,
    onOk: () => {
      Message.success(`服务 ${service.name} 重启成功`);
    }
  });
};

// 查看服务日志
const viewServiceLogs = (container: any, service: any) => {
  Message.info(`查看服务 ${service.name} 的日志`);
};

onMounted(() => {
  refreshServices();
});
</script>

<style lang="scss" scoped>
.service-status-page {
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
  
  .chart-container {
    h4 {
      margin: 0 0 16px 0;
      font-size: 16px;
      font-weight: 600;
    }
    
    .chart-placeholder {
      height: 200px;
      display: flex;
      flex-direction: column;
      justify-content: center;
      align-items: center;
      color: var(--color-text-2);
      border: 1px dashed var(--color-border-2);
      border-radius: 4px;
      
      p {
        margin: 8px 0 0 0;
      }
    }
  }
  
  :deep(.arco-collapse-item-header) {
    font-weight: 500;
  }
}
</style>
