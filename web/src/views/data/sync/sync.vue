<template>
  <div class="moox-page">
    <div class="moox-inner">
      <a-card title="数据同步配置" style="margin-bottom: 24px">
        <template #extra>
          <a-space>
            <a-button type="primary" @click="handleAddSync">
              <template #icon>
                <icon-plus />
              </template>
              新增同步配置
            </a-button>
            <a-button @click="handleRefresh">
              <template #icon>
                <icon-refresh />
              </template>
              刷新
            </a-button>
          </a-space>
        </template>

        <!-- 搜索区域 -->
        <a-row :gutter="16" style="margin-bottom: 16px">
          <a-col :span="6">
            <a-select
              v-model="searchForm.projectId"
              placeholder="选择项目"
              allow-clear
              @change="handleSearch"
            >
              <a-option value="">全部项目</a-option>
              <a-option v-for="project in projectList" :key="project.id" :value="project.id">
                {{ project.name_cn || project.name }}
              </a-option>
            </a-select>
          </a-col>
          <a-col :span="6">
            <a-select
              v-model="searchForm.status"
              placeholder="同步状态"
              allow-clear
              @change="handleSearch"
            >
              <a-option value="">全部状态</a-option>
              <a-option value="0">未配置</a-option>
              <a-option value="1">同步中</a-option>
              <a-option value="2">已暂停</a-option>
              <a-option value="3">同步失败</a-option>
            </a-select>
          </a-col>
          <a-col :span="6">
            <a-input
              v-model="searchForm.keyword"
              placeholder="搜索配置名称"
              allow-clear
              @change="handleSearch"
            >
              <template #suffix>
                <icon-search />
              </template>
            </a-input>
          </a-col>
        </a-row>

        <!-- 同步配置列表 -->
        <a-table
          row-key="id"
          size="small"
          :bordered="{ cell: true }"
          :loading="loading"
          :columns="columns"
          :data="filteredSyncList"
          :pagination="false"
        >
          <template #status="{ record }">
            <a-tag size="small" :color="getStatusColor(record.status)">
              {{ getStatusText(record.status) }}
            </a-tag>
          </template>
          
          <template #syncType="{ record }">
            <a-tag size="small" :color="record.syncType === 'full' ? 'blue' : 'green'">
              {{ record.syncType === 'full' ? '全量同步' : '增量同步' }}
            </a-tag>
          </template>
          
          <template #lastSync="{ record }">
            <div>
              <div>{{ record.lastSyncTime || '-' }}</div>
              <div style="font-size: 12px; color: var(--color-text-3);">
                {{ record.lastSyncStatus === 'success' ? '成功' : record.lastSyncStatus === 'failed' ? '失败' : '-' }}
              </div>
            </div>
          </template>

          <template #optional="{ record }">
            <a-space>
              <a-button 
                size="mini" 
                type="text" 
                @click="handleEdit(record)"
                :disabled="record.status === 1"
              >
                编辑
              </a-button>
              <a-button 
                size="mini" 
                type="text" 
                @click="handleToggleSync(record)"
                :loading="record.loading"
              >
                {{ record.status === 1 ? '暂停' : '启动' }}
              </a-button>
              <a-button 
                size="mini" 
                type="text" 
                @click="handleSyncNow(record)"
                :disabled="record.status !== 1"
              >
                立即同步
              </a-button>
              <a-button 
                size="mini" 
                type="text" 
                status="danger" 
                @click="handleDelete(record)"
              >
                删除
              </a-button>
            </a-space>
          </template>
        </a-table>
      </a-card>

      <!-- 新增/编辑同步配置弹窗 -->
      <a-modal
        v-model:visible="syncModalVisible"
        :title="isEditMode ? '编辑同步配置' : '新增同步配置'"
        width="600px"
        @ok="handleSaveSync"
        @cancel="handleCancelSync"
      >
        <a-form ref="syncFormRef" :model="syncForm" layout="vertical">
          <a-row :gutter="16">
            <a-col :span="12">
              <a-form-item
                label="配置名称"
                field="name"
                :rules="[{ required: true, message: '请输入配置名称' }]"
              >
                <a-input v-model="syncForm.name" placeholder="请输入配置名称" />
              </a-form-item>
            </a-col>
            <a-col :span="12">
              <a-form-item
                label="关联项目"
                field="projectId"
                :rules="[{ required: true, message: '请选择项目' }]"
              >
                <a-select v-model="syncForm.projectId" placeholder="请选择项目">
                  <a-option v-for="project in projectList" :key="project.id" :value="project.id">
                    {{ project.name_cn || project.name }}
                  </a-option>
                </a-select>
              </a-form-item>
            </a-col>
          </a-row>
          
          <a-row :gutter="16">
            <a-col :span="12">
              <a-form-item
                label="源机器地址"
                field="sourceHost"
                :rules="[{ required: true, message: '请输入源机器地址' }]"
              >
                <a-input v-model="syncForm.sourceHost" placeholder="192.168.1.100:8080" />
              </a-form-item>
            </a-col>
            <a-col :span="12">
              <a-form-item
                label="目标机器地址"
                field="targetHost"
                :rules="[{ required: true, message: '请输入目标机器地址' }]"
              >
                <a-input v-model="syncForm.targetHost" placeholder="192.168.1.101:8080" />
              </a-form-item>
            </a-col>
          </a-row>

          <a-row :gutter="16">
            <a-col :span="12">
              <a-form-item
                label="同步类型"
                field="syncType"
                :rules="[{ required: true, message: '请选择同步类型' }]"
              >
                <a-select v-model="syncForm.syncType" placeholder="请选择同步类型">
                  <a-option value="full">全量同步</a-option>
                  <a-option value="incremental">增量同步</a-option>
                </a-select>
              </a-form-item>
            </a-col>
            <a-col :span="12">
              <a-form-item
                label="同步频率"
                field="syncInterval"
                :rules="[{ required: true, message: '请输入同步频率' }]"
              >
                <a-input-number v-model="syncForm.syncInterval" placeholder="分钟" :min="1" />
              </a-form-item>
            </a-col>
          </a-row>

          <a-form-item label="认证信息">
            <a-row :gutter="16">
              <a-col :span="12">
                <a-input v-model="syncForm.username" placeholder="用户名" />
              </a-col>
              <a-col :span="12">
                <a-input-password v-model="syncForm.password" placeholder="密码" />
              </a-col>
            </a-row>
          </a-form-item>

          <a-form-item label="描述">
            <a-textarea
              v-model="syncForm.description"
              placeholder="请输入同步配置描述"
              :max-length="200"
              show-word-limit
            />
          </a-form-item>
        </a-form>
      </a-modal>
    </div>
  </div>
</template>

<script setup lang="ts">
import { useRoute } from 'vue-router';
import { ref, reactive, computed, onMounted, watch } from 'vue';
import { listProjects, type Project } from '@/api/project';
import { Message } from '@arco-design/web-vue';

const route = useRoute();

// 获取当前项目ID
const currentProjectId = computed(() => {
  return route.params.projectId as string;
});

interface SyncConfig {
  id: string;
  name: string;
  projectId: string;
  projectName: string;
  sourceHost: string;
  targetHost: string;
  syncType: 'full' | 'incremental';
  syncInterval: number;
  status: number; // 0:未配置 1:同步中 2:已暂停 3:同步失败
  username?: string;
  password?: string;
  description?: string;
  createTime: string;
  lastSyncTime?: string;
  lastSyncStatus?: 'success' | 'failed';
  loading?: boolean;
}

// 项目列表
const projectList = ref<Project[]>([]);

// 同步配置列表
const syncConfigList = reactive<SyncConfig[]>([
  {
    id: "sync-001",
    name: "用户数据同步",
    projectId: "101",
    projectName: "用户管理系统",
    sourceHost: "192.168.1.100:8080",
    targetHost: "192.168.1.101:8080",
    syncType: "incremental",
    syncInterval: 30,
    status: 1,
    description: "用户管理系统的增量数据同步",
    createTime: "2024-01-15 10:00:00",
    lastSyncTime: "2024-01-20 14:30:00",
    lastSyncStatus: "success"
  },
  {
    id: "sync-002",
    name: "订单数据同步",
    projectId: "102",
    projectName: "订单处理系统",
    sourceHost: "192.168.1.100:8080",
    targetHost: "192.168.1.102:8080",
    syncType: "full",
    syncInterval: 120,
    status: 2,
    description: "订单处理系统的全量数据同步",
    createTime: "2024-01-16 11:00:00",
    lastSyncTime: "2024-01-19 09:15:00",
    lastSyncStatus: "success"
  },
  {
    id: "sync-003",
    name: "分析数据同步",
    projectId: "101",
    projectName: "数据分析平台",
    sourceHost: "192.168.1.100:8080",
    targetHost: "192.168.1.103:8080",
    syncType: "incremental",
    syncInterval: 60,
    status: 3,
    description: "数据分析平台的增量数据同步",
    createTime: "2024-01-17 12:00:00",
    lastSyncTime: "2024-01-20 08:45:00",
    lastSyncStatus: "failed"
  }
]);

// 搜索表单
const searchForm = reactive({
  projectId: '',
  status: '',
  keyword: ''
});

// 弹窗状态
const syncModalVisible = ref(false);
const isEditMode = ref(false);
const currentEditSync = ref<SyncConfig | null>(null);

// 同步配置表单
const syncForm = reactive({
  name: '',
  projectId: '',
  sourceHost: '',
  targetHost: '',
  syncType: 'incremental',
  syncInterval: 30,
  username: '',
  password: '',
  description: ''
});

const loading = ref(false);

// 根据当前项目ID过滤同步配置，再应用搜索条件
const filteredSyncList = computed(() => {
  // 首先按当前项目ID过滤
  let result = syncConfigList.filter(item => item.projectId === currentProjectId.value);
  
  // 再应用搜索条件
  if (searchForm.status !== '') {
    result = result.filter(item => item.status === Number(searchForm.status));
  }
  
  if (searchForm.keyword) {
    result = result.filter(item => 
      item.name.toLowerCase().includes(searchForm.keyword.toLowerCase()) ||
      item.description?.toLowerCase().includes(searchForm.keyword.toLowerCase())
    );
  }
  
  console.log('数据同步 - 当前项目ID:', currentProjectId.value);
  console.log('数据同步 - 过滤后的配置列表:', result);
  
  return result;
});

const columns = [
  {
    title: "配置名称",
    dataIndex: "name",
    width: 150
  },
  {
    title: "关联项目",
    dataIndex: "projectName",
    width: 120
  },
  {
    title: "源机器",
    dataIndex: "sourceHost",
    width: 140
  },
  {
    title: "目标机器",
    dataIndex: "targetHost",
    width: 140
  },
  {
    title: "同步类型",
    dataIndex: "syncType",
    slotName: "syncType",
    align: "center",
    width: 100
  },
  {
    title: "频率(分钟)",
    dataIndex: "syncInterval",
    align: "center",
    width: 90
  },
  {
    title: "状态",
    dataIndex: "status",
    slotName: "status",
    align: "center",
    width: 80
  },
  {
    title: "最后同步",
    dataIndex: "lastSyncTime",
    slotName: "lastSync",
    width: 140
  },
  {
    title: "创建时间",
    dataIndex: "createTime",
    width: 140
  },
  {
    title: "操作",
    slotName: "optional",
    align: "center",
    fixed: "right",
    width: 200
  }
];

const getStatusColor = (status: number) => {
  const colors = ['gray', 'green', 'orange', 'red'];
  return colors[status] || 'gray';
};

const getStatusText = (status: number) => {
  const texts = ['未配置', '同步中', '已暂停', '同步失败'];
  return texts[status] || '未知';
};

// 加载项目数据
const loadProjectData = async () => {
  console.log('数据同步 - 加载项目数据，当前项目ID:', currentProjectId.value);
  
  try {
    projectList.value = await listProjects();
    
    // 设置搜索表单的默认项目ID为当前项目
    searchForm.projectId = currentProjectId.value;
    
    // TODO: 根据项目ID加载该项目的同步配置
    // 这里可以调用API获取特定项目的同步配置
    
  } catch (error) {
    console.error('加载项目数据失败:', error);
  }
};

// 监听项目ID变化
watch(currentProjectId, (newProjectId) => {
  if (newProjectId) {
    loadProjectData();
  }
}, { immediate: true });

const handleSearch = () => {
  // 搜索逻辑已在计算属性中实现
};

const handleRefresh = () => {
  loadProjectData();
  Message.success('刷新成功');
};

const handleAddSync = () => {
  isEditMode.value = false;
  currentEditSync.value = null;
  resetSyncForm();
  syncModalVisible.value = true;
};

const handleEdit = (record: SyncConfig) => {
  isEditMode.value = true;
  currentEditSync.value = record;
  
  // 预填充表单数据
  Object.assign(syncForm, {
    name: record.name,
    projectId: record.projectId,
    sourceHost: record.sourceHost,
    targetHost: record.targetHost,
    syncType: record.syncType,
    syncInterval: record.syncInterval,
    username: record.username || '',
    password: record.password || '',
    description: record.description || ''
  });
  
  syncModalVisible.value = true;
};

const handleToggleSync = async (record: SyncConfig) => {
  record.loading = true;
  try {
    // 模拟API调用
    await new Promise(resolve => setTimeout(resolve, 1000));
    
    if (record.status === 1) {
      record.status = 2; // 暂停
      Message.success('同步已暂停');
    } else {
      record.status = 1; // 启动
      Message.success('同步已启动');
    }
  } catch (error) {
    Message.error('操作失败');
  } finally {
    record.loading = false;
  }
};

const handleSyncNow = async (record: SyncConfig) => {
  try {
    // 模拟立即同步
    await new Promise(resolve => setTimeout(resolve, 2000));
    record.lastSyncTime = new Date().toLocaleString();
    record.lastSyncStatus = 'success';
    Message.success('立即同步完成');
  } catch (error) {
    Message.error('同步失败');
  }
};

const handleDelete = (record: SyncConfig) => {
  const index = syncConfigList.findIndex(item => item.id === record.id);
  if (index > -1) {
    syncConfigList.splice(index, 1);
    Message.success('删除成功');
  }
};

const handleSaveSync = () => {
  // 表单验证和保存逻辑
  if (isEditMode.value && currentEditSync.value) {
    // 更新现有配置
    Object.assign(currentEditSync.value, syncForm);
    Message.success('更新成功');
  } else {
    // 新增配置
    const newSync: SyncConfig = {
      id: `sync-${Date.now()}`,
      name: syncForm.name,
      projectId: syncForm.projectId,
      sourceHost: syncForm.sourceHost,
      targetHost: syncForm.targetHost,
      syncType: syncForm.syncType as 'full' | 'incremental',
      syncInterval: syncForm.syncInterval,
      username: syncForm.username,
      password: syncForm.password,
      description: syncForm.description,
      projectName: projectList.value.find(p => p.id.toString() === syncForm.projectId)?.name_cn || '',
      status: 0,
      createTime: new Date().toLocaleString()
    };
    syncConfigList.push(newSync);
    Message.success('新增成功');
  }
  
  syncModalVisible.value = false;
  resetSyncForm();
};

const handleCancelSync = () => {
  syncModalVisible.value = false;
  resetSyncForm();
};

const resetSyncForm = () => {
  Object.assign(syncForm, {
    name: '',
    projectId: '',
    sourceHost: '',
    targetHost: '',
    syncType: 'incremental',
    syncInterval: 30,
    username: '',
    password: '',
    description: ''
  });
};

onMounted(() => {
  loadProjectData();
});
</script>

<style lang="scss" scoped>
:deep(.arco-table-td) {
  padding: 8px 12px;
}
</style> 