<template>
  <div class="moox-page">
    <a-spin :loading="loading || taskPolling">
      <div class="moox-inner">
        <a-space wrap>
          <a-select v-model="form.cloudAccountId" placeholder="请选择云账户" style="width: 200px" allow-clear>
            <a-option v-for="account in cloudAccountOptions" :key="account.account_id" :value="account.account_id">
              {{ account.account_name }} ({{ getProviderName(account.provider) }})
            </a-option>
          </a-select>
          <a-input v-model="form.namespace" placeholder="请输入命名空间" allow-clear />
          <a-select placeholder="地区" v-model="form.region" style="width: 180px" allow-clear>
            <a-option value="ap-bangkok">亚太东南（曼谷）</a-option>
            <a-option value="ap-beijing">华北地区（北京）</a-option>
            <a-option value="ap-chengdu">西南地区（成都）</a-option>
            <a-option value="ap-chongqing">西南地区（重庆）</a-option>
            <a-option value="ap-guangzhou">华南地区（广州）</a-option>
            <a-option value="ap-hongkong">港澳台地区（中国香港）</a-option>
            <a-option value="ap-jakarta">亚太东南（雅加达）</a-option>
            <a-option value="ap-nanjing">华东地区（南京）</a-option>
            <a-option value="ap-seoul">亚太东北（首尔）</a-option>
            <a-option value="ap-shanghai">华东地区（上海）</a-option>
            <a-option value="ap-shanghai-fsi">华东地区（上海金融）</a-option>
            <a-option value="ap-shenzhen-fsi">华南地区（深圳金融）</a-option>
            <a-option value="ap-singapore">亚太东南（新加坡）</a-option>
            <a-option value="ap-tokyo">亚太东北（东京）</a-option>
            <a-option value="eu-frankfurt">欧洲地区（法兰克福）</a-option>
            <a-option value="na-ashburn">美国东部（弗吉尼亚）</a-option>
            <a-option value="na-siliconvalley">美国西部（硅谷）</a-option>
            <a-option value="sa-saopaulo">南美地区（圣保罗）</a-option>
          </a-select>
          <a-select placeholder="节点类型" v-model="form.nodeType" style="width: 120px" allow-clear>
            <a-option value="scf">云函数</a-option>
            <a-option value="server">服务器</a-option>
          </a-select>
          <a-select placeholder="节点状态" v-model="form.status" style="width: 120px" allow-clear>
            <a-option value="1">在线</a-option>
            <a-option value="0">离线</a-option>
            <a-option value="2">维护中</a-option>
            <a-option value="3">过载</a-option>
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
            <a-button type="primary" @click="onAdd" :disabled="taskPolling">
              <template #icon><icon-plus /></template>
              <span>新增</span>
            </a-button>
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
          </a-space>
        </a-row>

        <!-- 任务进度提示 -->
        <a-alert
          v-if="currentTaskStatus"
          type="info"
          style="margin: 16px 0;"
          closable
          @close="handleCloseTaskAlert"
        >
          <template #title>
            <a-space>
              <icon-loading spin />
              <span>任务执行中</span>
            </a-space>
          </template>
          <div>
            <div>任务类型：{{ getTaskTypeText(currentTaskStatus.task_type) }}</div>
            <div>处理进度：{{ currentTaskStatus.success_count + currentTaskStatus.failed_count }} / {{ currentTaskStatus.total_count }}</div>
            <div>成功：{{ currentTaskStatus.success_count }}，失败：{{ currentTaskStatus.failed_count }}</div>
            <a-progress 
              :percent="currentTaskStatus.progress" 
              :status="currentTaskStatus.failed_count > 0 ? 'warning' : 'normal'"
              style="margin-top: 8px"
            />
          </div>
        </a-alert>

        <!-- 选择状态提示 -->
        <a-alert
          v-if="selectedKeys.length > 0 && !taskPolling"
          type="info"
          style="margin: 16px 0;"
          :closable="true"
          @close="selectedKeys = []"
        >
          <template #title>
            已选择 {{ selectedKeys.length }} 个节点
          </template>
          <div style="font-size: 12px; color: #86909c;">
            提示：批量操作只会对当前选中的节点生效。切换页面时会保留其他页的选择状态。
          </div>
        </a-alert>

        <a-table
          row-key="node_id"
          :data="functionList"
          :bordered="{ cell: true }"
          :loading="loading"
          :scroll="{ x: 1320, y: '100%' }"
          :pagination="paginationConfig"
          :row-selection="taskPolling ? undefined : { type: 'checkbox', showCheckedAll: true }"
          :selected-keys="selectedKeys"
          @select="select"
          @select-all="selectAll"
          @page-change="onPageChange"
          @page-size-change="onPageSizeChange"
        >
          <template #columns>
            <a-table-column title="节点ID" data-index="node_id" :width="120">
              <template #cell="{ record }">
                <a-link @click="onViewNodeDetail(record)">{{ record.node_id }}</a-link>
              </template>
            </a-table-column>
            <a-table-column title="云账户ID" data-index="cloud_account_id" :width="120"></a-table-column>
            <a-table-column title="命名空间" data-index="namespace" :width="120"></a-table-column>
            <a-table-column title="节点类型" data-index="node_type" :width="100">
              <template #cell="{ record }">
                <a-tag bordered size="small" :color="record.node_type === 'scf' ? 'blue' : 'orange'">
                  {{ record.node_type === 'scf' ? '云函数' : '服务器' }}
                </a-tag>
              </template>
            </a-table-column>
            <a-table-column title="地区" data-index="region" :width="150">
              <template #cell="{ record }">
                {{ getRegionName(record.region) }}
              </template>
            </a-table-column>
            <a-table-column title="IP地址" data-index="ip_address" :width="120"></a-table-column>
            <a-table-column title="支持的采集器" data-index="supported_collectors" :width="160">
              <template #cell="{ record }">
                <div v-if="record.supported_collectors">
                  <a-tag v-for="item in parseJSON(record.supported_collectors)" :key="item" 
                    bordered size="small" style="margin: 2px">{{ item }}</a-tag>
                </div>
              </template>
            </a-table-column>
            <a-table-column title="状态" :width="100" align="center">
              <template #cell="{ record }">
                <a-tag bordered size="small" 
                  :color="getStatusColor(record.status)">
                  {{ getStatusText(record.status) }}
                </a-tag>
              </template>
            </a-table-column>
            <a-table-column title="操作" :width="240" align="center" fixed="right">
              <template #cell="{ record }">
                <a-space>
                  <a-button v-if="record.node_type === 'scf'" type="primary" size="mini" @click="onDeploy(record)" :disabled="taskPolling">
                    <template #icon><icon-upload /></template>
                    <span>部署</span>
                  </a-button>
                  <a-button type="primary" size="mini" status="success" @click="onEdit(record)" :disabled="taskPolling">
                    <template #icon><icon-edit /></template>
                    <span>编辑</span>
                  </a-button>
                  <a-popconfirm
                    content="确定要删除该节点吗？删除后将无法恢复。"
                    ok-text="确定"
                    cancel-text="取消"
                    @ok="onDelete(record)"
                    position="tr"
                  >
                    <a-button type="primary" size="mini" status="danger" :disabled="taskPolling">
                      <template #icon><icon-delete /></template>
                      <span>删除</span>
                    </a-button>
                  </a-popconfirm>
                </a-space>
              </template>
            </a-table-column>
          </template>
        </a-table>
      </div>
    </a-spin>

    <!-- 保留原有的其他弹窗组件 -->
    <!-- ... 其他弹窗代码保持不变 ... -->

  </div>
</template>

<script setup lang="ts">
import { ref, reactive, computed, onMounted, onBeforeUnmount } from 'vue';
import { useRouter } from 'vue-router';
import { Message, Modal } from '@arco-design/web-vue';
import { api } from '@/api/config';
import { AsyncTaskManager, TaskStatus, asyncTaskManager } from '@/utils/async-task';
import type { TaskStatusResponse } from '@/utils/async-task';

// 接口定义
interface CloudFunction {
  node_id: string;
  cloud_account_id: string;
  namespace: string;
  node_type: string;
  region: string;
  ip_address: string;
  version: string;
  supported_collectors: string;
  capacity: string;
  current_load: string;
  metadata: string;
  status: number;
  enabled: number;
  created_at: string;
  updated_at: string;
}

interface CloudAccount {
  account_id: string;
  account_name: string;
  provider: string;
  secret_id: string;
  secret_key: string;
  extra_config: string;
  status: number;
  created_at: string;
  updated_at: string;
}

// 状态管理
const loading = ref(false);
const taskPolling = ref(false);
const currentTaskStatus = ref<TaskStatusResponse | null>(null);
const form = reactive({
  cloudAccountId: '',
  namespace: '',
  region: '',
  nodeType: '',
  status: ''
});

// 数据列表
const functionList = ref<CloudFunction[]>([]);
const allFunctionList = ref<CloudFunction[]>([]);
const selectedKeys = ref<string[]>([]);
const cloudAccountOptions = ref<CloudAccount[]>([]);

// 分页配置
const pagination = ref({
  current: 1,
  pageSize: 10,
  total: 0,
  showTotal: true,
  showJumper: true,
  showPageSize: true,
  pageSizeOptions: [10, 20, 30, 50, 100]
});

const paginationConfig = computed(() => ({
  current: pagination.value.current,
  pageSize: pagination.value.pageSize,
  total: pagination.value.total,
  showTotal: pagination.value.showTotal,
  showJumper: pagination.value.showJumper,
  showPageSize: pagination.value.showPageSize,
  pageSizeOptions: pagination.value.pageSizeOptions
}));

// 生命周期钩子
onMounted(async () => {
  await loadData();
  await loadCloudAccounts();
  
  // 检查并恢复任务状态
  await asyncTaskManager.checkAndRestoreTask(handleTaskRestore);
});

onBeforeUnmount(() => {
  // 清理轮询
  asyncTaskManager.stopPolling();
});

// 检查任务恢复
const handleTaskRestore = (taskId: string, status: TaskStatusResponse) => {
  taskPolling.value = true;
  currentTaskStatus.value = status;
  
  // 继续轮询任务状态
  asyncTaskManager.startPolling(taskId, {
    onProgress: (data) => {
      currentTaskStatus.value = data;
    },
    onSuccess: (data) => {
      handleTaskComplete(data);
    },
    onFailed: (data) => {
      handleTaskComplete(data);
    },
    onPartialSuccess: (data) => {
      handleTaskComplete(data);
    },
    showLoading: false
  });
};

// 任务完成处理
const handleTaskComplete = (data: TaskStatusResponse) => {
  taskPolling.value = false;
  
  // 刷新数据
  loadData();
  
  // 清空选中项
  selectedKeys.value = [];
  
  // 3秒后自动关闭进度提示
  setTimeout(() => {
    currentTaskStatus.value = null;
    AsyncTaskManager.removeTaskIdFromUrl();
  }, 3000);
};

// 关闭任务提示
const handleCloseTaskAlert = () => {
  currentTaskStatus.value = null;
  AsyncTaskManager.removeTaskIdFromUrl();
};

// 批量新增
const onBatchAdd = () => {
  if (cloudAccountOptions.value.length === 0) {
    Message.warning('请先创建云账户');
    return;
  }
  
  // 显示批量新增弹窗
  Modal.open({
    title: '批量新增云函数',
    content: '确定要批量新增云函数节点吗？',
    onOk: async () => {
      await executeBatchAdd();
    }
  });
};

// 执行批量新增
const executeBatchAdd = async () => {
  // 准备批量新增的数据
  const nodes = Array(5).fill(null).map((_, index) => ({
    cloud_account_id: cloudAccountOptions.value[0].account_id,
    node_type: 'scf',
    region: 'ap-guangzhou',
    ip_address: `10.0.0.${index + 1}`,
    version: '1.0.0',
    supported_collectors: JSON.stringify(['metrics', 'logs']),
    capacity: '100',
    metadata: JSON.stringify({ env: 'prod', index })
  }));

  try {
    // 创建异步任务
    const taskId = await asyncTaskManager.createAndExecuteTask('BATCH_CREATE_NODE', {
      nodes
    });

    taskPolling.value = true;
    
    // 开始轮询任务状态
    asyncTaskManager.startPolling(taskId, {
      onProgress: (data) => {
        currentTaskStatus.value = data;
      },
      onSuccess: (data) => {
        handleTaskComplete(data);
      },
      onFailed: (data) => {
        handleTaskComplete(data);
      },
      onPartialSuccess: (data) => {
        handleTaskComplete(data);
      }
    });
    
  } catch (error) {
    console.error('创建批量新增任务失败:', error);
  }
};

// 批量部署
const batchDeploy = () => {
  if (selectedKeys.value.length === 0) {
    Message.warning('请选择要部署的节点');
    return;
  }
  
  Message.info('批量部署功能开发中...');
};

// 批量删除
const batchDelete = () => {
  if (selectedKeys.value.length === 0) {
    Message.warning('请选择要删除的节点');
    return;
  }
  
  Modal.warning({
    title: '批量删除确认',
    content: `确定要删除选中的 ${selectedKeys.value.length} 个节点吗？删除后将无法恢复。`,
    hideCancel: false,
    onOk: async () => {
      await executeBatchDelete();
    }
  });
};

// 执行批量删除
const executeBatchDelete = async () => {
  const nodes = selectedKeys.value.map(nodeId => ({ node_id: nodeId }));

  try {
    // 创建异步任务
    const taskId = await asyncTaskManager.createAndExecuteTask('BATCH_DELETE_NODE', {
      nodes
    });

    taskPolling.value = true;
    
    // 开始轮询任务状态
    asyncTaskManager.startPolling(taskId, {
      onProgress: (data) => {
        currentTaskStatus.value = data;
      },
      onSuccess: (data) => {
        handleTaskComplete(data);
      },
      onFailed: (data) => {
        handleTaskComplete(data);
      },
      onPartialSuccess: (data) => {
        handleTaskComplete(data);
      }
    });
    
  } catch (error) {
    console.error('创建批量删除任务失败:', error);
  }
};

// 加载数据
const loadData = async () => {
  loading.value = true;
  try {
    const response = await api.post('/gateway/collector/GetNodeList', {
      cloud_account_id: form.cloudAccountId,
      namespace: form.namespace,
      region: form.region,
      node_type: form.nodeType,
      status: form.status
    });
    
    if (response.data?.ret_info?.code === 0) {
      allFunctionList.value = response.data.ret_info.data || [];
      pagination.value.total = allFunctionList.value.length;
      updateCurrentPageData();
    }
  } catch (error) {
    console.error('加载数据失败:', error);
    Message.error('加载数据失败');
  } finally {
    loading.value = false;
  }
};

// 加载云账户列表
const loadCloudAccounts = async () => {
  try {
    const response = await api.post('/gateway/collector/ListCloudAccounts', {});
    if (response.data?.ret_info?.code === 0) {
      cloudAccountOptions.value = response.data.ret_info.data || [];
    }
  } catch (error) {
    console.error('加载云账户失败:', error);
  }
};

// 工具函数
const getTaskTypeText = (taskType: string) => {
  const typeMap: Record<string, string> = {
    'BATCH_CREATE_NODE': '批量创建节点',
    'BATCH_UPDATE_NODE': '批量更新节点',
    'BATCH_DELETE_NODE': '批量删除节点'
  };
  return typeMap[taskType] || taskType;
};

const getProviderName = (provider: string) => {
  const providerMap: Record<string, string> = {
    'tencent': '腾讯云',
    'aliyun': '阿里云',
    'aws': 'AWS'
  };
  return providerMap[provider] || provider;
};

const getRegionName = (region: string) => {
  const regionMap: Record<string, string> = {
    'ap-bangkok': '亚太东南（曼谷）',
    'ap-beijing': '华北地区（北京）',
    'ap-chengdu': '西南地区（成都）',
    'ap-chongqing': '西南地区（重庆）',
    'ap-guangzhou': '华南地区（广州）',
    'ap-hongkong': '港澳台地区（中国香港）',
    // ... 其他地区映射
  };
  return regionMap[region] || region;
};

const getStatusColor = (status: number) => {
  const colorMap: Record<number, string> = {
    0: 'red',
    1: 'green',
    2: 'orange',
    3: 'red'
  };
  return colorMap[status] || 'gray';
};

const getStatusText = (status: number) => {
  const textMap: Record<number, string> = {
    0: '离线',
    1: '在线',
    2: '维护中',
    3: '过载'
  };
  return textMap[status] || '未知';
};

const parseJSON = (str: string) => {
  try {
    return JSON.parse(str);
  } catch {
    return [];
  }
};

// 分页相关
const updateCurrentPageData = () => {
  const startIndex = (pagination.value.current - 1) * pagination.value.pageSize;
  const endIndex = startIndex + pagination.value.pageSize;
  functionList.value = allFunctionList.value.slice(startIndex, endIndex);
};

const onPageChange = (page: number) => {
  pagination.value.current = page;
  updateCurrentPageData();
};

const onPageSizeChange = (pageSize: number) => {
  pagination.value.pageSize = pageSize;
  pagination.value.current = 1;
  updateCurrentPageData();
};

// 查询和重置
const search = () => {
  pagination.value.current = 1;
  loadData();
};

const reset = () => {
  form.cloudAccountId = '';
  form.namespace = '';
  form.region = '';
  form.nodeType = '';
  form.status = '';
  search();
};

// 选择处理
const select = (rowKeys: string[], rowKey: string, record: CloudFunction) => {
  const index = selectedKeys.value.indexOf(rowKey);
  if (index > -1) {
    selectedKeys.value.splice(index, 1);
  } else {
    selectedKeys.value.push(rowKey);
  }
};

const selectAll = (checked: boolean) => {
  if (checked) {
    const currentPageKeys = functionList.value.map(item => item.node_id);
    currentPageKeys.forEach(key => {
      if (!selectedKeys.value.includes(key)) {
        selectedKeys.value.push(key);
      }
    });
  } else {
    const currentPageKeys = functionList.value.map(item => item.node_id);
    selectedKeys.value = selectedKeys.value.filter(key => !currentPageKeys.includes(key));
  }
};

// 单个操作（保留原有实现）
const onAdd = () => {
  Message.info('新增功能开发中...');
};

const onEdit = (record: CloudFunction) => {
  Message.info('编辑功能开发中...');
};

const onDelete = async (record: CloudFunction) => {
  try {
    const response = await api.post('/gateway/collector/DeleteNode', {
      node_id: record.node_id
    });
    
    if (response.data?.ret_info?.code === 200) {
      Message.success('删除成功');
      loadData();
    } else {
      Message.error(response.data?.ret_info?.msg || '删除失败');
    }
  } catch (error) {
    Message.error('删除失败');
  }
};

const onDeploy = (record: CloudFunction) => {
  Message.info('部署功能开发中...');
};

const onViewNodeDetail = (record: CloudFunction) => {
  Message.info('查看详情功能开发中...');
};

const onCloudAccountManage = () => {
  Message.info('云账户管理功能开发中...');
};
</script>

<style lang="less" scoped>
.moox-page {
  padding: 16px;
  height: 100%;
}

.moox-inner {
  height: 100%;
  background: #fff;
  padding: 16px;
  border-radius: 4px;

  .a-row {
    margin-top: 16px;
  }

  .a-table {
    margin-top: 16px;
  }
}
</style>