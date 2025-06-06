<template>
  <div class="moox-page">
    <div class="moox-inner">
      <!-- 数据统计卡片 -->
      <a-row :gutter="16" style="margin-bottom: 24px">
        <a-col :xs="24" :sm="12" :md="6" :lg="6" :xl="6" :xxl="6">
          <a-card class="stats-card">
            <a-statistic title="项目总数" :value="projectStats.totalProjects" />
            <template #extra>
              <icon-folder size="24" style="color: #165dff" />
            </template>
          </a-card>
        </a-col>
        <a-col :xs="24" :sm="12" :md="6" :lg="6" :xl="6" :xxl="6">
          <a-card class="stats-card">
            <a-statistic title="对象总数" :value="projectStats.totalObjects" />
            <template #extra>
              <icon-storage size="24" style="color: #00b42a" />
            </template>
          </a-card>
        </a-col>
        <a-col :xs="24" :sm="12" :md="6" :lg="6" :xl="6" :xxl="6">
          <a-card class="stats-card">
            <a-statistic title="数据总量" :value="projectStats.totalData" suffix="条" />
            <template #extra>
              <icon-file size="24" style="color: #ff7d00" />
            </template>
          </a-card>
        </a-col>
        <a-col :xs="24" :sm="12" :md="6" :lg="6" :xl="6" :xxl="6">
          <a-card class="stats-card">
            <a-statistic title="活跃项目" :value="projectStats.activeProjects" />
            <template #extra>
              <icon-check-circle size="24" style="color: #14c9c9" />
            </template>
          </a-card>
        </a-col>
      </a-row>

      <!-- 项目列表 -->
      <a-card title="项目列表" style="margin-bottom: 24px">
        <template #extra>
          <a-button type="primary" @click="handleCreateProject">
            <template #icon>
              <icon-plus />
            </template>
            新建项目
          </a-button>
        </template>
        
        <a-table
          row-key="id"
          size="small"
          :bordered="{ cell: true }"
          :loading="loading"
          :columns="columns"
          :data="projectList"
          :pagination="false"
        >
          <template #status="{ record }">
            <a-tag size="small" :color="getStatusColor(record.status)">
              {{ getStatusText(record.status) }}
            </a-tag>
          </template>
          
          <template #optional="{ record }">
            <a-space>
              <a-button size="mini" type="text" @click="handleViewProject(record)">
                查看数据
              </a-button>
              <a-button size="mini" type="text" @click="handleManageProject(record)">
                管理项目
              </a-button>
            </a-space>
          </template>
        </a-table>
      </a-card>

      <!-- 最近活动 -->
      <a-card title="最近活动">
        <a-timeline>
          <a-timeline-item v-for="activity in recentActivities" :key="activity.id">
            <template #dot>
              <icon-check-circle style="color: #00b42a" />
            </template>
            <div class="activity-item">
              <div class="activity-content">{{ activity.content }}</div>
              <div class="activity-time">{{ activity.time }}</div>
            </div>
          </a-timeline-item>
        </a-timeline>
      </a-card>
    </div>
  </div>
</template>

<script setup lang="ts">
import { useRouter, useRoute } from 'vue-router';
import { ref, reactive, onMounted, computed, watch } from 'vue';

interface ProjectInfo {
  id: string;
  name: string;
  description: string;
  status: number;
  objectCount: number;
  dataCount: number;
  createTime: string;
  updateTime: string;
}

interface ProjectStats {
  totalProjects: number;
  totalObjects: number;
  totalData: number;
  activeProjects: number;
}

interface Activity {
  id: string;
  content: string;
  time: string;
}

const router = useRouter();
const route = useRoute();

// 获取当前项目ID
const currentProjectId = computed(() => {
  return route.params.projectId as string;
});

// 数据统计
const projectStats = reactive<ProjectStats>({
  totalProjects: 8,
  totalObjects: 45,
  totalData: 125430,
  activeProjects: 6
});

// 项目列表
const projectList = reactive<ProjectInfo[]>([
  {
    id: "proj-001",
    name: "用户管理系统",
    description: "用户信息管理和权限控制系统",
    status: 1,
    objectCount: 12,
    dataCount: 28500,
    createTime: "2024-01-15 10:30:00",
    updateTime: "2024-01-20 14:20:00"
  },
  {
    id: "proj-002", 
    name: "订单处理系统",
    description: "电商订单处理和库存管理",
    status: 1,
    objectCount: 18,
    dataCount: 45200,
    createTime: "2024-01-16 14:20:00",
    updateTime: "2024-01-21 09:15:00"
  },
  {
    id: "proj-003",
    name: "数据分析平台",
    description: "业务数据分析和报表生成",
    status: 2,
    objectCount: 8,
    dataCount: 12300,
    createTime: "2024-01-17 09:15:00",
    updateTime: "2024-01-22 16:30:00"
  }
]);

// 最近活动
const recentActivities = reactive<Activity[]>([
  {
    id: "1",
    content: "项目 '用户管理系统' 新增了用户行为数据表",
    time: "2小时前"
  },
  {
    id: "2", 
    content: "项目 '订单处理系统' 同步了最新的订单数据",
    time: "4小时前"
  },
  {
    id: "3",
    content: "数据分析平台完成了月度数据清理",
    time: "1天前"
  },
  {
    id: "4",
    content: "新创建项目 '客户关系管理'",
    time: "2天前"
  }
]);

const loading = ref<boolean>(false);

// 根据项目ID加载数据
const loadProjectData = async (projectId: string) => {
  if (!projectId) return;
  
  loading.value = true;
  console.log('数据概览 - 加载项目数据:', projectId);
  
  try {
    // TODO: 根据项目ID调用API获取项目相关数据
    // 这里先模拟根据项目ID显示不同的数据
    if (projectId === '101') {
      projectStats.totalObjects = 15;
      projectStats.totalData = 35000;
      // 更新活动记录
      recentActivities.splice(0, recentActivities.length, 
        {
          id: "1",
          content: `项目 ${projectId} 新增了数据表`,
          time: "1小时前"
        },
        {
          id: "2",
          content: `项目 ${projectId} 完成数据同步`,
          time: "3小时前"
        }
      );
    } else {
      projectStats.totalObjects = 25;
      projectStats.totalData = 58000;
      // 更新活动记录
      recentActivities.splice(0, recentActivities.length,
        {
          id: "1",
          content: `项目 ${projectId} 更新了配置`,
          time: "30分钟前"
        },
        {
          id: "2",
          content: `项目 ${projectId} 导入了新数据`,
          time: "2小时前"
        }
      );
    }
  } catch (error) {
    console.error('加载项目数据失败:', error);
  } finally {
    loading.value = false;
  }
};

// 监听项目ID变化
watch(currentProjectId, (newProjectId) => {
  if (newProjectId) {
    loadProjectData(newProjectId);
  }
}, { immediate: true });

const columns = [
  {
    title: "项目名称",
    dataIndex: "name",
    width: 150
  },
  {
    title: "描述",
    dataIndex: "description"
  },
  {
    title: "状态",
    dataIndex: "status",
    slotName: "status",
    align: "center",
    width: 80
  },
  {
    title: "对象数",
    dataIndex: "objectCount",
    align: "center",
    width: 80
  },
  {
    title: "数据量",
    dataIndex: "dataCount",
    align: "center",
    width: 100
  },
  {
    title: "更新时间",
    dataIndex: "updateTime",
    width: 160
  },
  {
    title: "操作",
    slotName: "optional",
    align: "center",
    fixed: "right",
    width: 150
  }
];

const getStatusColor = (status: number) => {
  return status === 1 ? 'green' : 'orange';
};

const getStatusText = (status: number) => {
  return status === 1 ? '运行中' : '维护中';
};

const handleCreateProject = () => {
  router.push('/project/create-project');
};

const handleViewProject = (project: ProjectInfo) => {
  router.push(`/data-management/${project.id}/object-list`);
};

const handleManageProject = (project: ProjectInfo) => {
  router.push(`/project/${project.id}/dataset`);
};

onMounted(() => {
  console.log('数据概览页面挂载，当前项目ID:', currentProjectId.value);
});
</script>

<style lang="scss" scoped>
.stats-card {
  text-align: center;
  
  :deep(.arco-card-header) {
    padding: 16px 20px 8px;
  }
  
  :deep(.arco-card-body) {
    padding: 8px 20px 16px;
  }
}

.activity-item {
  .activity-content {
    font-size: 14px;
    color: var(--color-text-1);
    margin-bottom: 4px;
  }
  
  .activity-time {
    font-size: 12px;
    color: var(--color-text-3);
  }
}
</style> 