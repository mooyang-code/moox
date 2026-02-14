<template>
  <div class="cloud-function-page">
    <!-- 搜索栏 -->
    <div class="search-section">
      <div class="search-bar">
        <a-space wrap>
          <a-select v-model="form.cloudAccountId" placeholder="请选择云账户" style="width: 200px" allow-clear>
            <a-option v-for="account in cloudAccountOptions" :key="account.account_id" :value="account.account_id">
              {{ account.account_name }} ({{ getProviderName(account.provider) }})
            </a-option>
          </a-select>
          <a-input v-model="form.nodeId" placeholder="请输入节点ID" allow-clear />
          <a-select placeholder="地区" v-model="form.region" style="width: 200px" allow-clear>
            <a-option v-for="region in regionOptions" :key="region.code" :value="region.code">
              {{ region.name }}
              <a-tag v-if="region.tag" size="small" :color="region.tag === '国内' ? 'blue' : 'orange'" style="margin-left: 4px;">
                {{ region.tag }}
              </a-tag>
            </a-option>
          </a-select>
          <a-select placeholder="节点类型" v-model="form.nodeType" style="width: 180px" allow-clear>
            <a-option value="scf-event">云函数（事件型）</a-option>
            <a-option value="scf-web">云函数（Web型）</a-option>
            <a-option value="server">服务器</a-option>
          </a-select>
          <a-select placeholder="节点状态" v-model="form.status" style="width: 120px" allow-clear>
            <a-option value="online">在线</a-option>
            <a-option value="offline">离线</a-option>
          </a-select>
          <a-button type="primary" @click="search">
            <template #icon><icon-search /></template>
            <span>查询</span>
          </a-button>
          <a-button @click="reset">
            <template #icon><icon-refresh /></template>
            <span>重置</span>
          </a-button>
        </a-space>

        <a-row>
          <a-space wrap>
            <a-button type="primary" status="success" @click="onBatchAdd" :disabled="taskPolling">
              <template #icon><icon-plus-circle /></template>
              <span>批量新增</span>
            </a-button>
            <a-button type="primary" status="warning" @click="batchDeploy" :disabled="taskPolling">
              <template #icon><icon-upload /></template>
              <span>批量部署</span>
            </a-button>
            <a-button type="primary" status="danger" @click="batchDelete" :disabled="taskPolling">
              <template #icon><icon-delete /></template>
              <span>批量删除</span>
            </a-button>
            <a-button type="outline" @click="onCloudAccountManage">
              <template #icon><icon-settings /></template>
              <span>云账户管理</span>
            </a-button>
            <a-button type="outline" @click="onFunctionPackageManage">
              <template #icon><icon-code-block /></template>
              <span>代码包版本管理</span>
            </a-button>
          </a-space>
        </a-row>
      </div>
    </div>

    <!-- 任务进度提示 -->
    <a-alert v-if="taskPolling && currentTaskStatus" type="info" closable @close="handleCloseTaskAlert" style="margin-bottom: 16px;">
      <template #icon><icon-loading /></template>
      <div>
        <div style="font-weight: 500; margin-bottom: 4px;">
          {{ getTaskTypeText(currentTaskStatus.task_type) }}进行中...
        </div>
        <a-progress
          :percent="currentTaskStatus.progress_percent"
          :status="currentTaskStatus.task_status === 3 ? 'danger' : 'normal'"
        />
        <div style="margin-top: 8px; font-size: 12px; color: #666;">
          {{ currentTaskStatus.current_step || '准备中...' }}
        </div>
      </div>
    </a-alert>

    <!-- 批量任务状态展示 -->
    <div v-if="batchJobStatuses.length > 0" style="margin-bottom: 16px;">
      <a-card title="批量任务执行状态" size="small">
        <a-space direction="vertical" fill>
          <div v-for="job in batchJobStatuses" :key="job.job_id" class="job-status-item">
            <a-row align="center">
              <a-col :span="4">
                <a-tag :color="getJobStatusColor(job.status)">
                  {{ getJobStatusText(job.status) }}
                </a-tag>
              </a-col>
              <a-col :span="16">
                <a-progress
                  :percent="job.progress_percent"
                  :status="job.status === 'FAILED' ? 'danger' : job.status === 'SUCCESS' ? 'success' : 'normal'"
                />
              </a-col>
              <a-col :span="4" style="text-align: right; font-size: 12px; color: #666;">
                {{ job.processed_count || 0 }} / {{ job.total_count || 0 }}
              </a-col>
            </a-row>
            <div v-if="job.current_step" style="margin-top: 4px; font-size: 12px; color: #999;">
              {{ job.current_step }}
            </div>
            <div v-if="job.error_message" style="margin-top: 4px; font-size: 12px; color: #f53f3f;">
              错误: {{ job.error_message }}
            </div>
          </div>
        </a-space>
      </a-card>
    </div>

    <!-- 表格 -->
    <a-table
      row-key="node_id"
      :loading="loading"
      :columns="columns"
      :data="functionList"
      :pagination="paginationProps"
      :row-selection="rowSelection"
      @page-change="onPageChange"
      @page-size-change="handlePageSizeChange"
      :scroll="{ x: 1800 }"
    >
      <template #columns>
        <a-table-column title="节点ID" data-index="node_id" :width="120">
          <template #cell="{ record }">
            <a-link @click="onViewNodeDetail(record)">{{ record.node_id }}</a-link>
          </template>
        </a-table-column>
        <a-table-column title="地区" data-index="region" :width="150">
          <template #cell="{ record }">
            {{ getRegionName(record.region) }}
          </template>
        </a-table-column>
        <a-table-column title="最后心跳时间" data-index="last_heartbeat" :width="170">
          <template #cell="{ record }">
            {{ formatDateTime(record.last_heartbeat) }}
          </template>
        </a-table-column>
        <a-table-column title="标签" data-index="tag" :width="80">
          <template #cell="{ record }">
            <a-tag v-if="record.tag" size="small" :color="record.tag === '国内' ? 'blue' : 'orange'">
              {{ record.tag }}
            </a-tag>
            <span v-else>-</span>
          </template>
        </a-table-column>
        <a-table-column title="代码包版本" data-index="package_version" :width="150">
          <template #cell="{ record }">
            <a-link
              v-if="record.package_version && record.package_version !== '-'"
              @click="onShowPackageDetail(record)"
              style="cursor: pointer;"
            >
              {{ record.package_version }}
            </a-link>
            <span v-else>-</span>
          </template>
        </a-table-column>
        <a-table-column title="状态" :width="80" align="center">
          <template #cell="{ record }">
            <a-tag bordered size="small"
              :color="getStatusColor(record.status)">
              {{ getStatusText(record.status) }}
            </a-tag>
          </template>
        </a-table-column>
        <a-table-column title="操作" :width="250" align="center" fixed="right">
          <template #cell="{ record }">
            <a-space>
              <a-button type="outline" size="mini" @click="onEdit(record)" :disabled="taskPolling">
                <template #icon><icon-edit /></template>
                <span>编辑</span>
              </a-button>
              <a-button v-if="['scf-event', 'scf-web'].includes(record.node_type)" type="primary" size="mini" @click="onDeploy(record)" :disabled="taskPolling">
                <template #icon><icon-upload /></template>
                <span>部署</span>
              </a-button>
              <a-popconfirm
                content="确定要删除该节点吗？删除后将无法恢复。"
                ok-text="确定"
                cancel-text="取消"
                @ok="() => onDelete(record)"
                position="tr"
              >
                <a-button type="outline" status="danger" size="mini" :disabled="taskPolling">
                  <template #icon><icon-delete /></template>
                  <span>删除</span>
                </a-button>
              </a-popconfirm>
            </a-space>
          </template>
        </a-table-column>
      </template>
    </a-table>

    <!-- 云账户管理弹窗 -->
    <CloudAccountManage
      v-model="cloudAccountManageVisible"
      @refresh="loadCloudAccounts"
    />

    <!-- 代码包版本管理弹窗 -->
    <FunctionPackageManage
      v-model="functionPackageManageVisible"
      :package-type="currentPackageType"
      @refresh="loadData"
    />

    <!-- 其他弹窗省略，与云函数页面完全一致 -->
  </div>
</template>

<script setup lang="ts">
import { ref, reactive, computed, onMounted, onBeforeUnmount } from 'vue';
import { Message } from '@arco-design/web-vue';
import { api } from '@/api/config';
import { asyncTaskManager } from '@/utils/async-task';
import CloudAccountManage from '@/views/collector/cloud-account/cloud-account-manage.vue';
import FunctionPackageManage from '@/views/collector/cloud-function/function-package-manage.vue';

// 状态管理
const loading = ref(false);
const taskPolling = ref(false);
const currentTaskStatus = ref<any>(null);

const form = reactive({
  cloudAccountId: '',
  nodeId: '',
  region: '',
  nodeType: '',
  status: ''
});

const functionList = ref<any[]>([]);
const selectedKeys = ref<string[]>([]);
const cloudAccountOptions = ref<any[]>([]);
const regionOptions = ref<any[]>([]);
const cloudAccountManageVisible = ref(false);
const functionPackageManageVisible = ref(false);
const batchJobStatuses = ref<any[]>([]);

const pagination = reactive({
  current: 1,
  pageSize: 20,
  total: 0,
  showTotal: true,
  showJumper: true,
  showPageSize: true,
  pageSizeOptions: [10, 20, 50, 100]
});

// 表格列配置
const columns = [
  { title: '节点ID', dataIndex: 'node_id' },
  { title: '地区', dataIndex: 'region' },
  { title: '最后心跳时间', dataIndex: 'last_heartbeat' },
  { title: '标签', dataIndex: 'tag' },
  { title: '代码包版本', dataIndex: 'package_version' },
  { title: '状态', dataIndex: 'status' },
  { title: '操作', dataIndex: 'actions' }
];

// 行选择配置
const rowSelection = reactive({
  type: 'checkbox',
  showCheckedAll: true,
  selectedRowKeys: selectedKeys,
  onlyCurrent: false
});

// 分页配置
const paginationProps = computed(() => ({
  current: pagination.current,
  pageSize: pagination.pageSize,
  total: pagination.total,
  showTotal: pagination.showTotal,
  showJumper: pagination.showJumper,
  showPageSize: pagination.showPageSize,
  pageSizeOptions: pagination.pageSizeOptions
}));

// 根据路由路径判断当前的 package_type
const currentPackageType = computed(() => {
  return 'data_collector'; // 容器列表固定为数据采集类型
});

// 根据路由路径判断默认的节点类型
const defaultNodeType = computed(() => {
  return 'server'; // 容器列表默认为服务器类型
});

// 根据路由路径判断当前的业务类型
const currentBizType = computed(() => {
  return 'container'; // 容器列表固定为容器类型
});

// 生命周期钩子
onMounted(async () => {
  // 根据路由设置默认的节点类型筛选条件
  form.nodeType = defaultNodeType.value;

  await loadData();
  await loadCloudAccounts();
  await loadRegions();

  // 检查并恢复任务状态
  await asyncTaskManager.checkAndRestoreTask(handleTaskRestore);
});

onBeforeUnmount(() => {
  // 清理轮询
  asyncTaskManager.stopPolling();
});

// 数据加载
const loadData = async (showEmptyTip = false) => {
  loading.value = true;
  try {
    const response = await api.post('/cloudnode/GetNodeList', {
      node_id: form.nodeId,
      cloud_account_id: form.cloudAccountId,
      region: form.region,
      node_type: form.nodeType,
      biz_type: currentBizType.value,
      status: form.status,
      page: pagination.current,
      page_size: pagination.pageSize
    });

    if (response.data?.code === 200) {
      let data = response.data.data;
      if (Array.isArray(data)) {
        functionList.value = data;
      } else {
        functionList.value = [data].filter(Boolean);
      }
      pagination.total = response.data.total || functionList.value.length;
      if (showEmptyTip && functionList.value.length === 0) {
        Message.info('查询结果为空');
      }
    }
  } catch (error) {
    console.error('加载数据失败:', error);
    Message.error('加载数据失败');
  } finally {
    loading.value = false;
  }
};

const loadCloudAccounts = async () => {
  try {
    const response = await api.post('/cloudaccount/GetAccountList', {
      page: 1,
      page_size: 100
    });
    if (response.data?.code === 200) {
      cloudAccountOptions.value = response.data.data || [];
    }
  } catch (error) {
    console.error('加载云账户失败:', error);
  }
};

const loadRegions = async () => {
  try {
    const response = await api.post('/cloudaccount/GetRegionList', {});
    if (response.data?.code === 200) {
      regionOptions.value = response.data.data || [];
    }
  } catch (error) {
    console.error('加载地区列表失败:', error);
  }
};

// 工具函数
const getProviderName = (provider: string) => {
  const providerMap: Record<string, string> = {
    'tencent': '腾讯云',
    'aliyun': '阿里云',
    'aws': 'AWS'
  };
  return providerMap[provider] || provider;
};

const getRegionName = (region: string) => {
  const regionInfo = regionOptions.value.find(r => r.code === region);
  return regionInfo ? regionInfo.name : region;
};

const getStatusColor = (status: string) => {
  return status === 'online' ? 'green' : 'red';
};

const getStatusText = (status: string) => {
  return status === 'online' ? '在线' : '离线';
};

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
      second: '2-digit'
    });
  } catch {
    return dateTime;
  }
};

const getTaskTypeText = (taskType: string) => {
  const typeMap: Record<string, string> = {
    'CREATE_NODE': '批量创建节点',
    'BATCH_UPDATE_NODE': '批量更新节点',
    'DELETE_NODE': '批量删除节点',
    'DEPLOY_NODE': '批量部署节点'
  };
  return typeMap[taskType] || taskType;
};

const getJobStatusColor = (status: string) => {
  const colorMap: Record<string, string> = {
    'PENDING': 'gray',
    'PROCESSING': 'blue',
    'SUCCESS': 'green',
    'FAILED': 'red'
  };
  return colorMap[status] || 'gray';
};

const getJobStatusText = (status: string) => {
  const textMap: Record<string, string> = {
    'PENDING': '待处理',
    'PROCESSING': '处理中',
    'SUCCESS': '成功',
    'FAILED': '失败'
  };
  return textMap[status] || status;
};

// 查询和重置
const search = () => {
  pagination.current = 1;
  loadData(true);
};

const reset = () => {
  form.cloudAccountId = '';
  form.nodeId = '';
  form.region = '';
  form.nodeType = defaultNodeType.value;
  form.status = '';
  search();
};

// 分页相关
const onPageChange = (page: number) => {
  pagination.current = page;
  loadData();
};

const handlePageSizeChange = (pageSize: number) => {
  pagination.pageSize = pageSize;
  pagination.current = 1;
  loadData();
};

// 按钮处理函数（占位）
const onBatchAdd = () => {
  Message.info('批量新增功能开发中');
};

const batchDeploy = () => {
  Message.info('批量部署功能开发中');
};

const batchDelete = () => {
  Message.info('批量删除功能开发中');
};

const onCloudAccountManage = () => {
  cloudAccountManageVisible.value = true;
};

const onFunctionPackageManage = () => {
  functionPackageManageVisible.value = true;
};

const onViewNodeDetail = (record: any) => {
  Message.info(`查看节点详情: ${record.node_id}`);
};

const onEdit = (record: any) => {
  Message.info(`编辑节点: ${record.node_id}`);
};

const onDeploy = (record: any) => {
  Message.info(`部署节点: ${record.node_id}`);
};

const onDelete = (record: any) => {
  Message.info(`删除节点: ${record.node_id}`);
};

const onShowPackageDetail = (record: any) => {
  Message.info(`查看代码包详情: ${record.package_version}`);
};

const handleCloseTaskAlert = () => {
  taskPolling.value = false;
  currentTaskStatus.value = null;
  batchJobStatuses.value = [];
};

const handleTaskRestore = () => {
  // 任务恢复逻辑
  return false;
};
</script>

<style scoped lang="less">
.cloud-function-page {
  padding: 16px;
  background: #f5f5f5;
  min-height: calc(100vh - 60px);
}

.search-section {
  background: white;
  padding: 16px;
  border-radius: 4px;
  margin-bottom: 16px;
}

.search-bar {
  display: flex;
  flex-direction: column;
  gap: 16px;
}

.job-status-item {
  padding: 12px;
  background: #f7f8fa;
  border-radius: 4px;
}
</style>
