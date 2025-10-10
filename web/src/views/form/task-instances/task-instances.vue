<template>
  <div class="moox-page">
    <a-spin :loading="loading">
      <div class="moox-inner">
        <a-space wrap>
          <a-input v-model="form.instanceId" placeholder="请输入实例ID" allow-clear />
          <a-input v-model="form.taskId" placeholder="请输入任务ID" allow-clear />
          <a-input v-model="form.nodeId" placeholder="请输入节点ID" allow-clear />
          <a-select placeholder="执行状态" v-model="form.status" style="width: 120px" allow-clear>
            <a-option :value="0">待执行</a-option>
            <a-option :value="1">执行中</a-option>
            <a-option :value="2">成功</a-option>
            <a-option :value="3">失败</a-option>
            <a-option :value="4">超时</a-option>
            <a-option :value="5">已取消</a-option>
          </a-select>
          <a-range-picker v-model="form.timeRange" :style="{width: '320px'}" />
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
            <a-button type="primary" @click="refreshList">
              <template #icon><icon-sync /></template>
              <span>刷新</span>
            </a-button>
            <a-button type="primary" status="warning" @click="batchRetry">
              <template #icon><icon-redo /></template>
              <span>批量重试</span>
            </a-button>
            <a-button type="primary" status="danger" @click="batchCancel">
              <template #icon><icon-stop /></template>
              <span>批量取消</span>
            </a-button>
          </a-space>
        </a-row>

        <a-table
          row-key="instance_id"
          :data="instanceList"
          :bordered="{ cell: true }"
          :loading="loading"
          :scroll="{ x: 1600, y: '100%' }"
          :pagination="paginationConfig"
          :row-selection="{ type: 'checkbox', showCheckedAll: true }"
          :selected-keys="selectedKeys"
          @select="select"
          @select-all="selectAll"
          @page-change="onPageChange"
          @page-size-change="onPageSizeChange"
        >
          <template #columns>
            <a-table-column title="实例ID" data-index="instance_id" :width="200"></a-table-column>
            <a-table-column title="任务ID" data-index="task_id" :width="180"></a-table-column>
            <a-table-column title="数据集ID" data-index="dataset_id" :width="150"></a-table-column>
            <a-table-column title="执行节点" data-index="node_id" :width="150"></a-table-column>
            <a-table-column title="目标对象" data-index="target_objects" :width="200">
              <template #cell="{ record }">
                <a-tooltip v-if="getObjectList(record.target_objects).length > 3" 
                  :content="getObjectList(record.target_objects).join(', ')">
                  <span>{{ getObjectList(record.target_objects).slice(0, 3).join(', ') }}...</span>
                </a-tooltip>
                <span v-else>{{ getObjectList(record.target_objects).join(', ') }}</span>
              </template>
            </a-table-column>
            <a-table-column title="开始时间" data-index="start_time" :width="180"></a-table-column>
            <a-table-column title="结束时间" data-index="end_time" :width="180"></a-table-column>
            <a-table-column title="执行状态" :width="100" align="center">
              <template #cell="{ record }">
                <a-tag bordered size="small" :color="getStatusColor(record.status)">
                  {{ getStatusText(record.status) }}
                </a-tag>
              </template>
            </a-table-column>
            <a-table-column title="操作" :width="200" align="center" fixed="right">
              <template #cell="{ record }">
                <a-space>
                  <a-button type="primary" size="mini" @click="onViewDetails(record)">
                    <template #icon><icon-eye /></template>
                    <span>详情</span>
                  </a-button>
                  <a-button v-if="canRetry(record)" type="primary" status="warning" size="mini" @click="handleRetry(record)">
                    <template #icon><icon-redo /></template>
                    <span>重试</span>
                  </a-button>
                  <a-button v-if="canCancel(record)" type="primary" status="danger" size="mini" @click="handleCancel(record)">
                    <template #icon><icon-stop /></template>
                    <span>取消</span>
                  </a-button>
                  <a-button type="text" size="mini" @click="onViewLogs(record)">
                    <template #icon><icon-file /></template>
                    <span>日志</span>
                  </a-button>
                </a-space>
              </template>
            </a-table-column>
          </template>
        </a-table>
      </div>
    </a-spin>

    <!-- 详情模态框 -->
    <a-modal v-model:visible="detailVisible" :footer="false" width="800px">
      <template #title>任务实例详情</template>
      <a-descriptions :column="2" bordered>
        <a-descriptions-item label="实例ID">{{ detailData.instance_id }}</a-descriptions-item>
        <a-descriptions-item label="任务ID">{{ detailData.task_id }}</a-descriptions-item>
        <a-descriptions-item label="数据集ID">{{ detailData.dataset_id }}</a-descriptions-item>
        <a-descriptions-item label="执行节点">{{ detailData.node_id }}</a-descriptions-item>
        <a-descriptions-item label="执行状态">
          <a-tag :color="getStatusColor(detailData.status || 0)">
            {{ getStatusText(detailData.status || 0) }}
          </a-tag>
        </a-descriptions-item>
        <a-descriptions-item label="开始时间">{{ detailData.start_time }}</a-descriptions-item>
        <a-descriptions-item label="结束时间">{{ detailData.end_time || '-' }}</a-descriptions-item>
        <a-descriptions-item label="创建时间">{{ detailData.create_time }}</a-descriptions-item>
        <a-descriptions-item label="更新时间">{{ detailData.modify_time }}</a-descriptions-item>
      </a-descriptions>
      
      <a-divider />
      
      <a-descriptions :column="1" bordered>
        <a-descriptions-item label="目标对象列表">
          <pre>{{ formatJSON(detailData.target_objects || '') }}</pre>
        </a-descriptions-item>
        <a-descriptions-item label="执行参数">
          <pre>{{ formatJSON(detailData.execution_params || '') }}</pre>
        </a-descriptions-item>
        <a-descriptions-item label="执行结果">
          <pre>{{ formatJSON(detailData.result || '') }}</pre>
        </a-descriptions-item>
      </a-descriptions>
    </a-modal>

    <!-- 日志模态框 -->
    <a-modal v-model:visible="logVisible" width="900px" :footer="false">
      <template #title>任务执行日志</template>
      <div class="log-container">
        <a-button type="primary" size="small" @click="refreshLogs" style="margin-bottom: 10px">
          <template #icon><icon-sync /></template>
          刷新日志
        </a-button>
        <pre class="log-content">{{ logContent || '暂无日志' }}</pre>
      </div>
    </a-modal>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, onMounted, watch } from 'vue';
import { Message, Modal } from '@arco-design/web-vue';
import service from '@/api/index';
import { useProjectStore } from '@/store/modules/project';
import { storeToRefs } from 'pinia';

interface TaskInstance {
  instance_id: string;
  task_id: string;
  project_id: string;
  dataset_id: string;
  node_id: string;
  target_objects: string;
  execution_params: string;
  status: number;
  start_time: string;
  end_time: string;
  result: string;
  create_time: string;
  modify_time: string;
}

const loading = ref(false);
const instanceList = ref<TaskInstance[]>([]);
const selectedKeys = ref<string[]>([]);
const detailVisible = ref(false);
const detailData = ref<Partial<TaskInstance>>({});
const logVisible = ref(false);
const logContent = ref('');
const currentInstanceId = ref('');

const form = ref({
  instanceId: '',
  taskId: '',
  nodeId: '',
  status: null,
  timeRange: []
});

const pagination = ref({
  current: 1,
  pageSize: 10,
  total: 0,
  showTotal: true,
  showPageSize: true
});

// Get project store
const projectStore = useProjectStore();
const { selectedProjectId } = storeToRefs(projectStore);

const paginationConfig = computed(() => ({
  ...pagination.value,
  onChange: (current: number) => {
    pagination.value.current = current;
    getInstanceList();
  },
  onPageSizeChange: (pageSize: number) => {
    pagination.value.pageSize = pageSize;
    pagination.value.current = 1;
    getInstanceList();
  }
}));

const getStatusColor = (status: number) => {
  const colors: { [key: number]: string } = {
    0: 'gray',    // 待执行
    1: 'blue',    // 执行中
    2: 'green',   // 成功
    3: 'red',     // 失败
    4: 'orange',  // 超时
    5: 'orange'   // 已取消
  };
  return colors[status] || 'gray';
};

const getStatusText = (status: number) => {
  const texts: { [key: number]: string } = {
    0: '待执行',
    1: '执行中',
    2: '成功',
    3: '失败',
    4: '超时',
    5: '已取消'
  };
  return texts[status] || '未知';
};

const formatJSON = (str: string) => {
  try {
    return JSON.stringify(JSON.parse(str || '{}'), null, 2);
  } catch {
    return str || '-';
  }
};

const getObjectList = (str: string) => {
  try {
    const objects = JSON.parse(str || '[]');
    return Array.isArray(objects) ? objects : [];
  } catch {
    return [];
  }
};

const canRetry = (record: TaskInstance) => {
  return record.status === 3 || record.status === 4; // 失败或超时
};

const canCancel = (record: TaskInstance) => {
  return record.status === 0 || record.status === 1; // 待执行或执行中
};

const select = (list: string[]) => {
  selectedKeys.value = list;
};

const selectAll = (state: boolean) => {
  selectedKeys.value = state ? instanceList.value.map(el => el.instance_id) : [];
};

const onPageChange = (current: number) => {
  pagination.value.current = current;
  getInstanceList();
};

const onPageSizeChange = (pageSize: number) => {
  pagination.value.pageSize = pageSize;
  pagination.value.current = 1;
  getInstanceList();
};

const search = () => {
  pagination.value.current = 1;
  getInstanceList();
};

const reset = () => {
  form.value = {
    instanceId: '',
    taskId: '',
    nodeId: '',
    status: null,
    timeRange: []
  };
  getInstanceList();
};

const refreshList = () => {
  getInstanceList();
};

const getInstanceList = async () => {
  loading.value = true;
  try {
    const params: any = {
      page: pagination.value.current,
      size: pagination.value.pageSize
    };

    // Always include the selected project ID from the global dropdown
    if (selectedProjectId.value) {
      params.project_id = selectedProjectId.value;
    }

    if (form.value.instanceId) params.instance_id = form.value.instanceId;
    if (form.value.taskId) params.task_id = form.value.taskId;
    if (form.value.nodeId) params.node_id = form.value.nodeId;
    if (form.value.status) params.status = form.value.status;
    if (form.value.timeRange && form.value.timeRange.length === 2) {
      params.start_time = form.value.timeRange[0];
      params.end_time = form.value.timeRange[1];
    }

    const response = await service.post('/gateway/collector/ListTaskInstances', params, {
      headers: {
        'app_id': 'moox_frontend',
        'app_key': '2521e0d21b6be0347b72bca93904a0dd'
      }
    });

    const data = response as any;
    if (data.code === 200) {
      instanceList.value = data.data || [];
      pagination.value.total = data.data ? data.data.length : 0;
    } else {
      Message.error(data.message || '获取任务实例列表失败');
    }
  } catch (error) {
    console.error('获取任务实例列表失败:', error);
    Message.error('获取任务实例列表失败');
  } finally {
    loading.value = false;
  }
};

const onViewDetails = (record: TaskInstance) => {
  detailData.value = record;
  detailVisible.value = true;
};

const onViewLogs = async (record: TaskInstance) => {
  currentInstanceId.value = record.instance_id;
  logVisible.value = true;
  await fetchLogs();
};

const fetchLogs = async () => {
  try {
    const response = await service.post('/gateway/collector/GetTaskInstanceLogs', {
      instance_id: currentInstanceId.value
    }, {
      headers: {
        'app_id': 'moox_frontend',
        'app_key': '2521e0d21b6be0347b72bca93904a0dd'
      }
    });

    const data = response as any;
    if (data.code === 200) {
      logContent.value = data.data || '暂无日志';
    } else {
      Message.error(data.message || '获取日志失败');
    }
  } catch (error) {
    console.error('获取日志失败:', error);
    Message.error('获取日志失败');
  }
};

const refreshLogs = () => {
  fetchLogs();
};

const handleRetry = async (record: TaskInstance) => {
  try {
    const response = await service.post('/gateway/collector/RetryTaskInstance', {
      instance_id: record.instance_id
    }, {
      headers: {
        'app_id': 'moox_frontend',
        'app_key': '2521e0d21b6be0347b72bca93904a0dd'
      }
    });

    const data = response as any;
    if (data.code === 200) {
      Message.success('重试成功');
      getInstanceList();
    } else {
      Message.error(data.message || '重试失败');
    }
  } catch (error) {
    console.error('重试失败:', error);
    Message.error('重试失败');
  }
};

const handleCancel = async (record: TaskInstance) => {
  try {
    const response = await service.post('/gateway/collector/CancelTaskInstance', {
      instance_id: record.instance_id
    }, {
      headers: {
        'app_id': 'moox_frontend',
        'app_key': '2521e0d21b6be0347b72bca93904a0dd'
      }
    });

    const data = response as any;
    if (data.code === 200) {
      Message.success('取消成功');
      getInstanceList();
    } else {
      Message.error(data.message || '取消失败');
    }
  } catch (error) {
    console.error('取消失败:', error);
    Message.error('取消失败');
  }
};

const batchRetry = () => {
  if (selectedKeys.value.length === 0) {
    Message.warning('请选择要重试的任务实例');
    return;
  }
  
  const retryableInstances = instanceList.value.filter(
    instance => selectedKeys.value.includes(instance.instance_id) && canRetry(instance)
  );
  
  if (retryableInstances.length === 0) {
    Message.warning('所选任务实例都不能重试');
    return;
  }
  
  Modal.confirm({
    title: '批量重试确认',
    content: `确定要重试选中的 ${retryableInstances.length} 个失败任务吗？`,
    onOk: () => {
      retryableInstances.forEach(instance => {
        handleRetry(instance);
      });
    }
  });
};

const batchCancel = () => {
  if (selectedKeys.value.length === 0) {
    Message.warning('请选择要取消的任务实例');
    return;
  }
  
  const cancelableInstances = instanceList.value.filter(
    instance => selectedKeys.value.includes(instance.instance_id) && canCancel(instance)
  );
  
  if (cancelableInstances.length === 0) {
    Message.warning('所选任务实例都不能取消');
    return;
  }
  
  Modal.confirm({
    title: '批量取消确认',
    content: `确定要取消选中的 ${cancelableInstances.length} 个任务吗？`,
    onOk: () => {
      cancelableInstances.forEach(instance => {
        handleCancel(instance);
      });
    }
  });
};

// Watch for project changes
watch(selectedProjectId, () => {
  getInstanceList();
});

onMounted(() => {
  getInstanceList();
});
</script>

<style scoped>
.moox-page {
  flex: 1;
  display: flex;
  flex-direction: column;
  background-color: #fff;
  border-radius: 4px;
  padding: 16px;
  margin: 16px;
  overflow: auto;
}

.moox-inner {
  flex: 1;
  overflow: auto;
}

.moox-inner > *:not(:last-child) {
  margin-bottom: 16px;
}

pre {
  margin: 0;
  font-family: monospace;
  font-size: 12px;
  background: #f5f5f5;
  padding: 8px;
  border-radius: 4px;
  max-height: 200px;
  overflow: auto;
}

.log-container {
  max-height: 600px;
  overflow: auto;
}

.log-content {
  background: #1e1e1e;
  color: #d4d4d4;
  padding: 16px;
  border-radius: 4px;
  font-size: 13px;
  line-height: 1.5;
  max-height: 500px;
  overflow: auto;
  white-space: pre-wrap;
  word-wrap: break-word;
}
</style>