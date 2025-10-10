<template>
  <div class="moox-page">
    <a-spin :loading="loading">
      <div class="moox-inner">
        <a-space wrap>
          <a-input v-model="form.taskId" placeholder="请输入任务ID" allow-clear />
          <a-input v-model="form.datasetId" placeholder="请输入数据集ID" allow-clear />
          <a-select placeholder="任务类型" v-model="form.taskType" style="width: 150px" allow-clear>
            <a-option value="object_list">对象列表采集</a-option>
            <a-option value="data_collect">数据采集</a-option>
          </a-select>
          <a-select placeholder="采集器类型" v-model="form.collectorType" style="width: 120px" allow-clear>
            <a-option value="kline">K线</a-option>
            <a-option value="ticker">Ticker</a-option>
            <a-option value="orderbook">订单簿</a-option>
            <a-option value="trade">交易</a-option>
            <a-option value="news">新闻</a-option>
          </a-select>
          <a-select placeholder="启用状态" v-model="form.enabled" style="width: 100px" allow-clear>
            <a-option value="true">启用</a-option>
            <a-option value="false">禁用</a-option>
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
            <a-button type="primary" @click="onAdd">
              <template #icon><icon-plus /></template>
              <span>新建任务</span>
            </a-button>
            <a-button type="primary" status="danger" @click="batchDelete">
              <template #icon><icon-delete /></template>
              <span>批量删除</span>
            </a-button>
            <a-button type="primary" status="warning" @click="batchUpdateEnabled">
              <template #icon><icon-settings /></template>
              <span>批量启用/禁用</span>
            </a-button>
          </a-space>
        </a-row>

        <a-table
          row-key="task_id"
          :data="taskList"
          :bordered="{ cell: true }"
          :loading="loading"
          :scroll="{ x: '100%', y: '100%', minWidth: 1500 }"
          :pagination="paginationConfig"
          :row-selection="{ type: 'checkbox', showCheckedAll: true }"
          :selected-keys="selectedKeys"
          @select="select"
          @select-all="selectAll"
          @page-change="onPageChange"
          @page-size-change="onPageSizeChange"
        >
          <template #columns>
            <a-table-column title="任务ID" data-index="task_id" :width="150">
              <template #cell="{ record }">
                <a-link @click="onViewDetails(record)">{{ record.task_id }}</a-link>
              </template>
            </a-table-column>
            <a-table-column title="数据集ID" data-index="dataset_id" :width="150"></a-table-column>
            <a-table-column title="任务类型" data-index="task_type" :width="130">
              <template #cell="{ record }">
                <a-tag bordered size="small" :color="record.task_type === 'object_list' ? 'blue' : 'green'">
                  {{ record.task_type === 'object_list' ? '对象列表' : '数据采集' }}
                </a-tag>
              </template>
            </a-table-column>
            <a-table-column title="采集器类型" data-index="collector_type" :width="100"></a-table-column>
            <a-table-column title="数据源" data-index="source_name" :width="100"></a-table-column>
            <a-table-column title="分配类型" data-index="assignment_type" :width="100">
              <template #cell="{ record }">
                <a-tag bordered size="small" :color="getAssignmentColor(record.assignment_type)">
                  {{ getAssignmentText(record.assignment_type) }}
                </a-tag>
              </template>
            </a-table-column>
            <a-table-column title="负载均衡策略" data-index="load_balance_strategy" :width="120"></a-table-column>
            <a-table-column title="优先级" data-index="priority" :width="80" align="center"></a-table-column>
            <a-table-column title="最后分发时间" data-index="last_dispatch_time" :width="180"></a-table-column>
            <a-table-column title="操作" :width="240" align="center" :fixed="'right'">
              <template #cell="{ record }">
                <a-space>
                  <a-button 
                    :type="record.enabled === 'true' ? 'primary' : 'outline'"
                    :status="record.enabled === 'true' ? 'success' : 'normal'"
                    size="mini" 
                    @click="handleEnableChange(record, record.enabled !== 'true')">
                    <template #icon>
                      <icon-check-circle v-if="record.enabled === 'true'" />
                      <icon-close-circle v-else />
                    </template>
                    <span>{{ record.enabled === 'true' ? '启用' : '禁用' }}</span>
                  </a-button>
                  <a-button type="primary" size="mini" @click="onUpdate(record)">
                    <template #icon><icon-edit /></template>
                    <span>修改</span>
                  </a-button>
                  <a-popconfirm type="warning" content="确定删除该任务配置吗?">
                    <a-button type="primary" status="danger" size="mini" @click="handleDelete(record)">
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

    <!-- 新增/修改模态框 -->
    <a-modal v-model:visible="open" @close="afterClose" @ok="handleOk" @cancel="afterClose" width="900px">
      <template #title> {{ title }} </template>
      <div>
        <a-form ref="formRef" auto-label-width :rules="rules" :model="addForm" :layout="'vertical'">
          <a-row :gutter="16">
            <a-col :span="12">
              <a-form-item field="task_id" label="任务ID" validate-trigger="blur">
                <a-input v-model="addForm.task_id" placeholder="留空自动生成" allow-clear :disabled="title === '修改任务'" />
              </a-form-item>
            </a-col>
            <a-col :span="12">
              <a-form-item field="task_type" label="任务类型" validate-trigger="blur">
                <a-select v-model="addForm.task_type" placeholder="请选择任务类型">
                  <a-option value="object_list">对象列表采集</a-option>
                  <a-option value="data_collect">数据采集</a-option>
                </a-select>
              </a-form-item>
            </a-col>
          </a-row>
          
          <a-form-item field="dataset_id" label="数据集ID" validate-trigger="blur">
            <a-input-number v-model="addForm.dataset_id" placeholder="请输入数据集ID" :min="1" hide-button style="width: 100%" />
          </a-form-item>
          
          <a-row :gutter="16">
            <a-col :span="12">
              <a-form-item field="collector_type" label="采集器类型" validate-trigger="blur">
                <a-select v-model="addForm.collector_type" placeholder="请选择采集器类型">
                  <a-option value="kline">K线数据</a-option>
                  <a-option value="ticker">Ticker数据</a-option>
                  <a-option value="orderbook">订单簿数据</a-option>
                  <a-option value="trade">交易数据</a-option>
                  <a-option value="news">新闻数据</a-option>
                </a-select>
              </a-form-item>
            </a-col>
            <a-col :span="12">
              <a-form-item field="source_name" label="数据源名称" validate-trigger="blur">
                <a-input v-model="addForm.source_name" placeholder="如：binance, okx" allow-clear />
              </a-form-item>
            </a-col>
          </a-row>

          <a-divider>任务分配配置</a-divider>
          
          <a-row :gutter="16">
            <a-col :span="12">
              <a-form-item field="assignment_type" label="分配类型">
                <a-select v-model="addForm.assignment_type" placeholder="请选择分配类型" @change="onAssignmentTypeChange">
                  <a-option value="auto">自动分配</a-option>
                  <a-option value="fixed">固定节点</a-option>
                  <a-option value="pattern">模式匹配</a-option>
                </a-select>
              </a-form-item>
            </a-col>
            <a-col :span="12">
              <a-form-item field="load_balance_strategy" label="负载均衡策略">
                <a-select v-model="addForm.load_balance_strategy" placeholder="请选择负载均衡策略">
                  <a-option value="round_robin">轮询</a-option>
                  <a-option value="least_load">最小负载</a-option>
                  <a-option value="random">随机</a-option>
                </a-select>
              </a-form-item>
            </a-col>
          </a-row>

          <a-form-item v-if="addForm.assignment_type === 'fixed'" label="指定节点列表">
            <a-select v-model="assignedNodesList" placeholder="请选择节点" multiple>
              <a-option v-for="node in nodeOptions" :key="node.node_id" :value="node.node_id">
                {{ node.node_name }} ({{ node.node_id }})
              </a-option>
            </a-select>
          </a-form-item>

          <a-form-item v-if="addForm.assignment_type === 'pattern'" field="node_pattern" label="节点匹配模式">
            <a-input v-model="addForm.node_pattern" placeholder="如：scf-collector-*" allow-clear />
          </a-form-item>

          <a-divider>采集目标配置</a-divider>
          
          <a-form-item field="object_pattern" label="对象匹配模式">
            <a-input v-model="addForm.object_pattern" placeholder="如：*USDT（支持通配符）" allow-clear />
          </a-form-item>
          
          <a-form-item label="目标对象列表（JSON数组）">
            <a-textarea v-model="addForm.target_objects" placeholder='["BTCUSDT", "ETHUSDT"]' :rows="3" />
          </a-form-item>
          
          <a-form-item label="强制指定对象（JSON对象）">
            <a-textarea v-model="addForm.force_objects" 
              placeholder='{"node-id-1": ["BTCUSDT"], "node-id-2": ["ETHUSDT"]}' 
              :rows="3" />
          </a-form-item>

          <a-divider>采集参数配置</a-divider>
          
          <a-row :gutter="16">
            <a-col :span="12">
              <a-form-item label="采集参数（JSON）">
                <a-textarea v-model="addForm.collect_params" 
                  placeholder='{"intervals": ["1m", "5m"], "depth": 20}' 
                  :rows="4" />
              </a-form-item>
            </a-col>
            <a-col :span="12">
              <a-form-item label="调度配置（JSON）">
                <a-textarea v-model="addForm.schedule_config" 
                  placeholder='{"cron": "*/5 * * * *", "retry": 3, "timeout": 300}' 
                  :rows="4" />
              </a-form-item>
            </a-col>
          </a-row>

          <a-row :gutter="16">
            <a-col :span="12">
              <a-form-item field="priority" label="优先级">
                <a-input-number v-model="addForm.priority" placeholder="数值越大优先级越高" :min="0" :max="999" />
              </a-form-item>
            </a-col>
            <a-col :span="12">
              <a-form-item field="enabled" label="启用状态">
                <a-select v-model="addForm.enabled">
                  <a-option value="true">启用</a-option>
                  <a-option value="false">禁用</a-option>
                </a-select>
              </a-form-item>
            </a-col>
          </a-row>
        </a-form>
      </div>
    </a-modal>

    <!-- 详情模态框 -->
    <a-modal v-model:visible="detailVisible" :footer="false" width="800px">
      <template #title>任务配置详情</template>
      <a-descriptions :column="2" bordered>
        <a-descriptions-item label="任务ID">{{ detailData.task_id }}</a-descriptions-item>
        <a-descriptions-item label="任务类型">{{ detailData.task_type }}</a-descriptions-item>
        <a-descriptions-item label="数据集ID">{{ detailData.dataset_id }}</a-descriptions-item>
        <a-descriptions-item label="采集器类型">{{ detailData.collector_type }}</a-descriptions-item>
        <a-descriptions-item label="数据源">{{ detailData.source_name }}</a-descriptions-item>
        <a-descriptions-item label="分配类型">{{ getAssignmentText(detailData.assignment_type || '') }}</a-descriptions-item>
        <a-descriptions-item label="负载均衡策略">{{ detailData.load_balance_strategy }}</a-descriptions-item>
        <a-descriptions-item label="优先级">{{ detailData.priority }}</a-descriptions-item>
        <a-descriptions-item label="启用状态">
          <a-tag :color="detailData.enabled === 'true' ? 'green' : 'red'">
            {{ detailData.enabled === 'true' ? '启用' : '禁用' }}
          </a-tag>
        </a-descriptions-item>
        <a-descriptions-item label="最后分发时间">{{ detailData.last_dispatch_time || '-' }}</a-descriptions-item>
        <a-descriptions-item label="创建时间">{{ detailData.create_time }}</a-descriptions-item>
      </a-descriptions>
      
      <a-divider />
      
      <a-descriptions :column="1" bordered>
        <a-descriptions-item label="节点配置">
          <div v-if="detailData.assignment_type === 'fixed'">
            指定节点：{{ detailData.assigned_nodes }}
          </div>
          <div v-else-if="detailData.assignment_type === 'pattern'">
            节点模式：{{ detailData.node_pattern }}
          </div>
          <div v-else>自动分配</div>
        </a-descriptions-item>
        <a-descriptions-item label="对象匹配模式">{{ detailData.object_pattern || '-' }}</a-descriptions-item>
        <a-descriptions-item label="目标对象">
          <pre>{{ formatJSON(detailData.target_objects || '') }}</pre>
        </a-descriptions-item>
        <a-descriptions-item label="强制指定对象">
          <pre>{{ formatJSON(detailData.force_objects || '') }}</pre>
        </a-descriptions-item>
        <a-descriptions-item label="采集参数">
          <pre>{{ formatJSON(detailData.collect_params || '') }}</pre>
        </a-descriptions-item>
        <a-descriptions-item label="调度配置">
          <pre>{{ formatJSON(detailData.schedule_config || '') }}</pre>
        </a-descriptions-item>
        <a-descriptions-item label="最后分发结果">
          {{ detailData.last_dispatch_result || '-' }}
        </a-descriptions-item>
      </a-descriptions>
    </a-modal>

    <!-- 批量启用/禁用模态框 -->
    <a-modal v-model:visible="batchEnableVisible" @ok="handleBatchUpdateEnabled">
      <template #title>批量更新启用状态</template>
      <a-form :model="batchEnableForm">
        <a-form-item label="选择状态">
          <a-radio-group v-model="batchEnableForm.enabled">
            <a-radio value="true">启用</a-radio>
            <a-radio value="false">禁用</a-radio>
          </a-radio-group>
        </a-form-item>
        <a-alert>将更新 {{ selectedKeys.length }} 个任务配置的启用状态</a-alert>
      </a-form>
    </a-modal>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, onMounted, watch } from 'vue';
import { Message, Modal } from '@arco-design/web-vue';
import service from '@/api/index';
import { useProjectStore } from '@/store/modules/project';
import { storeToRefs } from 'pinia';

interface TaskConfig {
  task_id: string;
  project_id: string;
  dataset_id: string;
  task_type: string;
  collector_type: string;
  source_name: string;
  assignment_type: string;
  assigned_nodes: string;
  node_pattern: string;
  load_balance_strategy: string;
  target_objects: string;
  object_pattern: string;
  force_objects: string;
  collect_params: string;
  schedule_config: string;
  enabled: string;
  priority: number;
  last_dispatch_time: string;
  last_dispatch_result: string;
  create_time: string;
}

const loading = ref(false);
const taskList = ref<TaskConfig[]>([]);
const selectedKeys = ref<string[]>([]);
const open = ref(false);
const title = ref('新建任务');
const formRef = ref();
const detailVisible = ref(false);
const detailData = ref<Partial<TaskConfig>>({});
const nodeOptions = ref<any[]>([]);
const assignedNodesList = ref<string[]>([]);
const batchEnableVisible = ref(false);
const batchEnableForm = ref({
  enabled: 'true'
});

// Get project store
const projectStore = useProjectStore();
const { selectedProjectId } = storeToRefs(projectStore);

const form = ref({
  taskId: '',
  datasetId: '',
  taskType: null,
  collectorType: null,
  enabled: null
});

const pagination = ref({
  current: 1,
  pageSize: 10,
  total: 0,
  showTotal: true,
  showPageSize: true
});

const paginationConfig = computed(() => ({
  ...pagination.value,
  onChange: (current: number) => {
    pagination.value.current = current;
    getTaskList();
  },
  onPageSizeChange: (pageSize: number) => {
    pagination.value.pageSize = pageSize;
    pagination.value.current = 1;
    getTaskList();
  }
}));

const addForm = ref({
  task_id: '',
  project_id: '',
  dataset_id: '',
  task_type: 'data_collect',
  collector_type: '',
  source_name: '',
  assignment_type: 'auto',
  assigned_nodes: '[]',
  node_pattern: '',
  load_balance_strategy: 'round_robin',
  target_objects: '[]',
  object_pattern: '',
  force_objects: '{}',
  collect_params: '{}',
  schedule_config: '{}',
  enabled: 'true',
  priority: '0'
});

const rules = {
  dataset_id: [{ required: true, message: '请输入数据集ID' }],
  task_type: [{ required: true, message: '请选择任务类型' }],
  collector_type: [{ required: true, message: '请选择采集器类型' }],
  source_name: [{ required: true, message: '请输入数据源名称' }]
};

const getAssignmentColor = (type: string) => {
  const colors: { [key: string]: string } = {
    auto: 'blue',
    fixed: 'green',
    pattern: 'orange'
  };
  return colors[type] || 'gray';
};

const getAssignmentText = (type: string) => {
  const texts: { [key: string]: string } = {
    auto: '自动分配',
    fixed: '固定节点',
    pattern: '模式匹配'
  };
  return texts[type] || type;
};

const formatJSON = (str: string) => {
  try {
    return JSON.stringify(JSON.parse(str || '{}'), null, 2);
  } catch {
    return str || '-';
  }
};

const select = (list: string[]) => {
  selectedKeys.value = list;
};

const selectAll = (state: boolean) => {
  selectedKeys.value = state ? taskList.value.map(el => el.task_id) : [];
};

const onPageChange = (current: number) => {
  pagination.value.current = current;
  getTaskList();
};

const onPageSizeChange = (pageSize: number) => {
  pagination.value.pageSize = pageSize;
  pagination.value.current = 1;
  getTaskList();
};

const search = () => {
  pagination.value.current = 1;
  getTaskList();
};

const reset = () => {
  form.value = {
    taskId: '',
    datasetId: '',
    taskType: null,
    collectorType: null,
    enabled: null
  };
  getTaskList();
};

const getTaskList = async () => {
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

    if (form.value.taskId) params.task_id = form.value.taskId;
    // Note: form.value.projectId is removed since we use the global selected project
    if (form.value.datasetId) params.dataset_id = form.value.datasetId;
    if (form.value.taskType) params.task_type = form.value.taskType;
    if (form.value.collectorType) params.collector_type = form.value.collectorType;
    if (form.value.enabled !== null) params.enabled = form.value.enabled;

    const response = await service.post('/gateway/collector/ListTaskConfigs', params, {
      headers: {
        'app_id': 'moox_frontend',
        'app_key': '2521e0d21b6be0347b72bca93904a0dd'
      }
    });

    const data = response as any;
    if (data.code === 200) {
      taskList.value = data.data || [];
      pagination.value.total = data.data ? data.data.length : 0;
    } else {
      Message.error(data.message || '获取任务列表失败');
    }
  } catch (error) {
    console.error('获取任务列表失败:', error);
    Message.error('获取任务列表失败');
  } finally {
    loading.value = false;
  }
};

const getNodeList = async () => {
  try {
    const response = await service.post('/gateway/collector/ListNodes', {}, {
      headers: {
        'app_id': 'moox_frontend',
        'app_key': '2521e0d21b6be0347b72bca93904a0dd'
      }
    });
    const data = response as any;
    if (data.code === 200) {
      nodeOptions.value = data.data || [];
    }
  } catch (error) {
    console.error('获取节点列表失败:', error);
  }
};

const onAdd = () => {
  title.value = '新建任务';
  addForm.value = {
    task_id: '',
    project_id: selectedProjectId.value || '',
    dataset_id: '',
    task_type: 'data_collect',
    collector_type: '',
    source_name: '',
    assignment_type: 'auto',
    assigned_nodes: '[]',
    node_pattern: '',
    load_balance_strategy: 'round_robin',
    target_objects: '[]',
    object_pattern: '',
    force_objects: '{}',
    collect_params: '{}',
    schedule_config: '{}',
    enabled: 'true',
    priority: '0'
  };
  assignedNodesList.value = [];
  open.value = true;
};

const onUpdate = (record: TaskConfig) => {
  title.value = '修改任务';
  addForm.value = { ...record, priority: String(record.priority) };
  
  // 解析 assigned_nodes
  try {
    assignedNodesList.value = JSON.parse(record.assigned_nodes || '[]');
  } catch {
    assignedNodesList.value = [];
  }
  
  open.value = true;
};

const onAssignmentTypeChange = (value: string) => {
  // 清空相关字段
  if (value !== 'fixed') {
    assignedNodesList.value = [];
    addForm.value.assigned_nodes = '[]';
  }
  if (value !== 'pattern') {
    addForm.value.node_pattern = '';
  }
};

const afterClose = () => {
  formRef.value?.clearValidate();
  open.value = false;
};

const handleOk = async () => {
  const valid = await formRef.value?.validate();
  if (!valid) {
    try {
      // 将选中的节点列表转换为JSON字符串
      if (addForm.value.assignment_type === 'fixed') {
        addForm.value.assigned_nodes = JSON.stringify(assignedNodesList.value);
      }
      
      // 验证JSON格式
      JSON.parse(addForm.value.target_objects || '[]');
      JSON.parse(addForm.value.force_objects || '{}');
      JSON.parse(addForm.value.collect_params || '{}');
      JSON.parse(addForm.value.schedule_config || '{}');
      
      // 确保 priority 是字符串，dataset_id 是数字
      const requestData = {
        ...addForm.value,
        dataset_id: addForm.value.dataset_id ? Number(addForm.value.dataset_id) : null,
        priority: String(addForm.value.priority)
      };
      
      const endpoint = title.value === '新建任务' ? '/gateway/collector/CreateTaskConfig' : '/gateway/collector/UpdateTaskConfig';
      const response = await service.post(endpoint, requestData, {
        headers: {
          'app_id': 'moox_frontend',
          'app_key': '2521e0d21b6be0347b72bca93904a0dd'
        }
      });

      const data = response as any;
      if (data.code === 200) {
        Message.success(title.value + '成功');
        open.value = false;
        getTaskList();
      } else {
        Message.error(data.message || title.value + '失败');
      }
    } catch (error) {
      if (error instanceof SyntaxError) {
        Message.error('JSON格式错误，请检查输入');
      } else {
        console.error(title.value + '失败:', error);
        Message.error(title.value + '失败');
      }
    }
  }
};

const handleDelete = async (record: TaskConfig) => {
  try {
    const response = await service.post('/gateway/collector/DeleteTaskConfig', { task_id: record.task_id }, {
      headers: {
        'app_id': 'moox_frontend',
        'app_key': '2521e0d21b6be0347b72bca93904a0dd'
      }
    });

    const data = response as any;
    if (data.code === 200) {
      Message.success('删除成功');
      getTaskList();
    } else {
      Message.error(data.message || '删除失败');
    }
  } catch (error) {
    console.error('删除失败:', error);
    Message.error('删除失败');
  }
};

const batchDelete = () => {
  if (selectedKeys.value.length === 0) {
    Message.warning('请选择要删除的任务');
    return;
  }
  
  Modal.warning({
    title: '批量删除确认',
    content: `确定要删除选中的 ${selectedKeys.value.length} 个任务配置吗？`,
    onOk: () => {
      selectedKeys.value.forEach(task_id => {
        handleDelete({ task_id } as TaskConfig);
      });
    }
  });
};

const handleEnableChange = async (record: TaskConfig, value: boolean) => {
  try {
    const response = await service.post('/gateway/collector/UpdateTaskConfig', { 
      ...record,
      enabled: value ? 'true' : 'false',
      priority: String(record.priority)
    }, {
      headers: {
        'app_id': 'moox_frontend',
        'app_key': '2521e0d21b6be0347b72bca93904a0dd'
      }
    });

    const data = response as any;
    if (data.code === 200) {
      Message.success('状态更新成功');
      getTaskList();
    } else {
      Message.error(data.message || '状态更新失败');
    }
  } catch (error) {
    console.error('状态更新失败:', error);
    Message.error('状态更新失败');
  }
};

const batchUpdateEnabled = () => {
  if (selectedKeys.value.length === 0) {
    Message.warning('请选择要操作的任务');
    return;
  }
  batchEnableVisible.value = true;
};

const handleBatchUpdateEnabled = async () => {
  try {
    const response = await service.post('/gateway/collector/BatchUpdateTaskConfigEnabled', { 
      task_ids: selectedKeys.value.join(','),
      enabled: batchEnableForm.value.enabled
    }, {
      headers: {
        'app_id': 'moox_frontend',
        'app_key': '2521e0d21b6be0347b72bca93904a0dd'
      }
    });

    const data = response as any;
    if (data.code === 200) {
      Message.success('批量更新成功');
      batchEnableVisible.value = false;
      getTaskList();
    } else {
      Message.error(data.message || '批量更新失败');
    }
  } catch (error) {
    console.error('批量更新失败:', error);
    Message.error('批量更新失败');
  }
};

const onViewDetails = (record: TaskConfig) => {
  detailData.value = record;
  detailVisible.value = true;
};

// Watch for project changes
watch(selectedProjectId, () => {
  getTaskList();
});

onMounted(() => {
  getTaskList();
  getNodeList();
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
</style>