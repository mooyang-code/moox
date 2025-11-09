<template>
  <div class="moox-page">
    <a-spin :loading="loading">
      <div class="moox-inner">
        <a-space wrap>
          <a-input v-model="form.ruleId" placeholder="请输入规则ID" allow-clear />
          <a-select v-model="form.dataType" placeholder="请选择数据类型" style="width: 150px" allow-clear>
            <a-option 
              v-for="config in dataTypeConfigs" 
              :key="config.data_type" 
              :value="config.data_type">
              {{ config.type_name }}
            </a-option>
          </a-select>
          <a-select v-model="form.dataSource" placeholder="请选择数据源" style="width: 150px" allow-clear>
            <a-option 
              v-for="source in getSearchDataSourceOptions()" 
              :key="source.value" 
              :value="source.value">
              {{ source.label }}
            </a-option>
          </a-select>
          <a-button type="primary" @click="search">
            <template #icon><icon-search /></template>
            <span>查询</span>
          </a-button>
          <a-button @click="reset">
            <template #icon><icon-refresh /></template>
            <span>重置</span>
          </a-button>
          <a-switch v-model="form.enabled" :checked-text="'启用'" :unchecked-text="'禁用'" @change="onEnabledChange" />
        </a-space>

        <a-row>
          <a-col>
            <a-button type="primary" status="success" @click="onAdd">
              <template #icon><icon-plus /></template>
              <span>新建任务</span>
            </a-button>
          </a-col>
        </a-row>

        <a-table
          row-key="rule_id"
          :data="taskList"
          :bordered="{ cell: true }"
          :loading="loading"
          :scroll="{ x: '100%', y: '100%', minWidth: 1000 }"
          :pagination="paginationConfig"
          :row-selection="{ type: 'checkbox', showCheckedAll: true }"
          :selected-keys="selectedKeys"
          @select="select"
          @select-all="selectAll"
          @page-change="onPageChange"
          @page-size-change="onPageSizeChange"
        >
          <template #columns>
            <a-table-column title="规则ID" data-index="rule_id" :width="150">
              <template #cell="{ record }">
                <a-link @click="onViewDetails(record)">{{ record.rule_id }}</a-link>
              </template>
            </a-table-column>
            <a-table-column title="数据类型" data-index="data_type" :width="120"></a-table-column>
            <a-table-column title="数据源" data-index="data_source" :width="120"></a-table-column>
            <a-table-column title="分配类型" data-index="assignment_type" :width="100">
              <template #cell="{ record }">
                <a-tag bordered size="small" :color="getAssignmentColor(record.assignment_type)">
                  {{ getAssignmentText(record.assignment_type) }}
                </a-tag>
              </template>
            </a-table-column>
            <a-table-column title="创建时间" :width="160">
              <template #cell="{ record }">
                {{ formatDateTime(record.create_time) }}
              </template>
            </a-table-column>
            <a-table-column title="修改时间" :width="160">
              <template #cell="{ record }">
                {{ formatDateTime(record.modify_time) }}
              </template>
            </a-table-column>
            <a-table-column title="操作" :width="180" align="center" :fixed="'right'">
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
        <a-form ref="formRef" auto-label-width :rules="Object.assign({}, rules, getDynamicRules())" :model="addForm" :layout="'vertical'">
          <a-row :gutter="16">
            <a-col v-if="title === '修改采集规则'" :span="12">
              <a-form-item field="rule_id" label="规则ID" validate-trigger="blur">
                <a-input v-model="addForm.rule_id" placeholder="留空自动生成" allow-clear :disabled="true" />
              </a-form-item>
            </a-col>
            <a-col :span="title === '修改采集规则' ? 12 : 24">
              <a-form-item field="data_type" label="数据类型" validate-trigger="blur">
                <a-select 
                  v-model="addForm.data_type" 
                  placeholder="请选择数据类型"
                  @change="onDataTypeChange">
                  <a-option 
                    v-for="config in dataTypeConfigs" 
                    :key="config.data_type" 
                    :value="config.data_type">
                    {{ config.type_name }}
                  </a-option>
                </a-select>
              </a-form-item>
            </a-col>
          </a-row>
          
          <a-form-item field="data_source" label="数据源" validate-trigger="blur">
            <a-select 
              v-model="addForm.data_source" 
              placeholder="请选择数据源"
              :loading="loadingDataSources"
              allow-clear>
              <a-option 
                v-for="source in dataSourceOptions" 
                :key="source.value" 
                :value="source.value">
                {{ source.label }}
              </a-option>
            </a-select>
          </a-form-item>

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

          <a-divider>采集参数配置</a-divider>
          
          <template v-if="currentFieldConfigs.length > 0">
            <a-form-item 
              v-for="field in currentFieldConfigs" 
              :key="field.id"
              :field="field.field_key"
              :label="field.field_name"
              :validate-trigger="['blur', 'change']"
              :rules="field.is_required ? [{ required: true, message: `请输入${field.field_name}` }] : []">
              
              <!-- 文本输入框 -->
              <a-input 
                v-if="field.field_type === 'text'"
                v-model="dynamicFormData[field.field_key]"
                :placeholder="`请输入${field.field_name}`"
                allow-clear />
              
              <!-- 数字输入框 -->
              <a-input-number 
                v-else-if="field.field_type === 'number'"
                v-model="dynamicFormData[field.field_key]"
                :placeholder="`请输入${field.field_name}`"
                :min="JSON.parse(field.field_options || '{}').min || -Infinity"
                :max="JSON.parse(field.field_options || '{}').max || Infinity"
                allow-clear />
              
              <!-- 单选下拉框 -->
              <a-select 
                v-else-if="field.field_type === 'select'"
                v-model="dynamicFormData[field.field_key]"
                :placeholder="`请选择${field.field_name}`"
                allow-clear>
                <a-option 
                  v-for="option in JSON.parse(field.field_options || '{}').options || []"
                  :key="option.value || option"
                  :value="option.value || option">
                  {{ option.label || option }}
                </a-option>
              </a-select>
              
              <!-- 多选下拉框 -->
              <a-select 
                v-else-if="field.field_type === 'multi-select'"
                v-model="dynamicFormData[field.field_key]"
                :placeholder="`请选择${field.field_name}`"
                multiple
                allow-clear>
                <a-option 
                  v-for="option in JSON.parse(field.field_options || '{}').options || []"
                  :key="option.value || option"
                  :value="option.value || option">
                  {{ getOptionLabel(option) }}
                </a-option>
              </a-select>
              
              <!-- 日期时间选择器 -->
              <a-date-picker 
                v-else-if="field.field_type === 'datetime'"
                v-model="dynamicFormData[field.field_key]"
                :placeholder="`请选择${field.field_name}`"
                show-time
                style="width: 100%" />
              
              <!-- 默认文本输入 -->
              <a-input 
                v-else
                v-model="dynamicFormData[field.field_key]"
                :placeholder="`请输入${field.field_name}`"
                allow-clear />
            </a-form-item>
          </template>
          
          <template v-else>
            <a-alert type="info" message="请先选择数据类型以配置采集参数" />
          </template>

          <a-form-item label="创建人">
            <a-input v-model="addForm.creator" readonly />
          </a-form-item>

          <a-form-item field="enabled" label="启用状态">
            <a-select v-model="addForm.enabled">
              <a-option value="true">启用</a-option>
              <a-option value="false">禁用</a-option>
            </a-select>
          </a-form-item>
        </a-form>
      </div>
    </a-modal>

    <!-- 详情模态框 -->
    <a-modal v-model:visible="detailVisible" :footer="false" width="800px">
      <template #title>任务配置详情</template>
      <a-descriptions :column="2" bordered>
        <a-descriptions-item label="规则ID">{{ detailData.rule_id }}</a-descriptions-item>
        <a-descriptions-item label="数据类型">{{ detailData.data_type }}</a-descriptions-item>
        <a-descriptions-item label="数据源">{{ detailData.data_source }}</a-descriptions-item>
        <a-descriptions-item label="分配类型">{{ getAssignmentText(detailData.assignment_type || '') }}</a-descriptions-item>
        <a-descriptions-item label="启用状态">
          <a-tag :color="detailData.enabled === 'true' ? 'green' : 'red'">
            {{ detailData.enabled === 'true' ? '启用' : '禁用' }}
          </a-tag>
        </a-descriptions-item>
        <a-descriptions-item label="创建人">{{ detailData.creator || '-' }}</a-descriptions-item>
        <a-descriptions-item label="创建时间">{{ formatDateTime(detailData.create_time || '') }}</a-descriptions-item>
        <a-descriptions-item label="修改时间">{{ formatDateTime(detailData.modify_time || '') }}</a-descriptions-item>
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
        <a-descriptions-item label="采集参数">
          <pre>{{ formatJSON(detailData.collect_params || '') }}</pre>
        </a-descriptions-item>
      </a-descriptions>
    </a-modal>

  </div>
</template>

<script setup lang="ts">
import { ref, computed, onMounted, watch } from 'vue';
import { Message } from '@arco-design/web-vue';
import service from '@/api/index';
import { useProjectStore } from '@/store/modules/project';
import { useUserInfoStore } from '@/store/modules/user-info';
import { storeToRefs } from 'pinia';

interface TaskConfig {
  rule_id: string;
  project_id: string;
  data_type: string;
  data_source: string;
  assignment_type: string;
  assigned_nodes: string;
  node_pattern: string;
  collect_params: string;
  enabled: string;
  creator: string;
  create_time: string;
  modify_time: string;
}

interface DataTypeConfig {
  id: number;
  data_type: string;
  type_name: string;
  type_desc: string;
  data_source_options: string;
  sort_order: number;
  version: number;
  create_time: string;
  modify_time: string;
}

interface FieldConfig {
  id: number;
  data_type: string;
  field_key: string;
  field_name: string;
  field_type: string;
  is_required: boolean;
  default_value: string;
  field_options: string;
  data_source_options: string;
  sort_order: number;
  create_time: string;
  modify_time: string;
}

// interface DataTypeConfigDetail {
//   config: DataTypeConfig;
//   fields: FieldConfig;
// }

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

// 数据类型配置相关数据
const dataTypeConfigs = ref<DataTypeConfig[]>([]);
const currentFieldConfigs = ref<FieldConfig[]>([]);
const dynamicFormData = ref<{ [key: string]: any }>({});

// 数据源相关数据
const dataSourceOptions = ref<{ label: string; value: string }[]>([]);
const loadingDataSources = ref(false);

// Get project store
const projectStore = useProjectStore();
const { selectedProjectId } = storeToRefs(projectStore);

// Get user info store
const userInfoStore = useUserInfoStore();
const { account } = storeToRefs(userInfoStore);

const form = ref({
  ruleId: '',
  dataType: '',
  dataSource: '',
  enabled: true
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
  rule_id: '',
  project_id: '',
  data_type: '',
  data_source: '',
  assignment_type: 'auto',
  assigned_nodes: '[]',
  node_pattern: '',
  collect_params: '{}',
  enabled: 'true',
  creator: ''
});

const rules = {
  data_type: [{ required: true, message: '请选择数据类型' }],
  data_source: [{ required: true, message: '请选择数据源' }]
};

// 获取动态表单验证规则
const getDynamicRules = () => {
  const dynamicRules: { [key: string]: any[] } = {};
  
  currentFieldConfigs.value.forEach(field => {
    if (field.is_required) {
      dynamicRules[field.field_key] = [
        { required: true, message: `请输入${field.field_name}` }
      ];
    }
  });
  
  return dynamicRules;
};

// 获取选项标签
const getOptionLabel = (option: any) => {
  if (typeof option === 'object' && option !== null) {
    return option.label || option.value || '';
  }
  return option;
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

// 获取数据源标签
const getDataSourceLabel = (value: string) => {
  const labels: { [key: string]: string } = {
    binance: '币安 (Binance)',
    okx: 'OKX',
    huobi: '火币 (Huobi)',
    bybit: 'Bybit',
    bitget: 'Bitget',
    kucoin: 'KuCoin',
    gate: 'Gate.io',
    mexc: 'MEXC',
    bitfinex: 'Bitfinex',
    coinbase: 'Coinbase',
    cryptonews: 'CryptoNews',
    coindesk: 'CoinDesk',
    cointelegraph: 'Cointelegraph',
    decrypt: 'Decrypt',
    theblock: 'The Block',
    messari: 'Messari',
    glassnode: 'Glassnode',
    intoblock: 'IntoTheBlock'
  };
  return labels[value] || value;
};

// 获取搜索用的数据源选项
const getSearchDataSourceOptions = () => {
  if (!form.value.dataType) {
    return [];
  }
  
  // 查找选中的数据类型配置
  const selectedConfig = dataTypeConfigs.value.find(config => config.data_type === form.value.dataType);
  if (!selectedConfig || !selectedConfig.data_source_options) {
    return [];
  }
  
  try {
    const config = JSON.parse(selectedConfig.data_source_options);
    
    // 处理对象格式 {"options": [{"value": "binance", "label": "币安"}]}
    if (config.options && Array.isArray(config.options)) {
      return config.options.map((option: any) => ({
        label: option.label || getDataSourceLabel(option.value),
        value: option.value
      }));
    } 
    // 处理数组格式 ["binance", "okx"]
    else if (Array.isArray(config)) {
      return config.map((source: string) => ({
        label: getDataSourceLabel(source),
        value: source
      }));
    }
  } catch (error) {
    console.error('解析数据源配置失败:', error);
  }
  
  return [];
};

const formatJSON = (str: string) => {
  try {
    return JSON.stringify(JSON.parse(str || '{}'), null, 2);
  } catch {
    return str || '-';
  }
};

// 格式化时间为本地时间格式
const formatDateTime = (dateTime: string) => {
  if (!dateTime) return '-';
  
  try {
    const date = new Date(dateTime);
    // 检查日期是否有效
    if (isNaN(date.getTime())) {
      return dateTime; // 如果转换失败，返回原始值
    }
    
    // 格式化日期为 YYYY-MM-DD HH:mm:ss
    const year = date.getFullYear();
    const month = String(date.getMonth() + 1).padStart(2, '0');
    const day = String(date.getDate()).padStart(2, '0');
    const hours = String(date.getHours()).padStart(2, '0');
    const minutes = String(date.getMinutes()).padStart(2, '0');
    const seconds = String(date.getSeconds()).padStart(2, '0');
    
    return `${year}-${month}-${day} ${hours}:${minutes}:${seconds}`;
  } catch (error) {
    console.error('格式化时间失败:', error);
    return dateTime;
  }
};

const select = (list: string[]) => {
  selectedKeys.value = list;
};

const selectAll = (state: boolean) => {
  selectedKeys.value = state ? taskList.value.map(el => el.rule_id) : [];
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

const onEnabledChange = (value: boolean) => {
  // 当启用状态开关变化时，重新查询列表
  console.log('启用状态变化为:', value ? '启用' : '禁用');
  search();
};

const reset = () => {
  form.value = {
    ruleId: '',
    dataType: '',
    dataSource: '',
    enabled: true
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

    if (form.value.ruleId) params.rule_id = form.value.ruleId;
    if (form.value.dataType) params.data_type = form.value.dataType;
    if (form.value.dataSource) params.data_source = form.value.dataSource;
    if (form.value.enabled !== null) params.enabled = form.value.enabled ? 'true' : 'false';

    const response = await service.post('/gateway/collector/ListTaskRules', params, {
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
    const response = await service.post('/gateway/cloudnode/ListNodes', {}, {
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

// 获取数据类型配置
const getDataTypeConfigs = async () => {
  try {
    const response = await service.post('/gateway/collector/ListDataTypeConfigs', {}, {
      headers: {
        'app_id': 'moox_frontend',
        'app_key': '2521e0d21b6be0347b72bca93904a0dd'
      }
    });
    const data = response as any;
    if (data.code === 200) {
      dataTypeConfigs.value = data.data || [];
    } else {
      Message.error(data.message || '获取数据类型配置失败');
    }
  } catch (error) {
    console.error('获取数据类型配置失败:', error);
    Message.error('获取数据类型配置失败');
  }
};

// 获取特定数据类型的字段配置
const getFieldConfigs = async (dataType: string) => {
  if (!dataType) {
    currentFieldConfigs.value = [];
    return;
  }

  try {
    const response = await service.post('/gateway/collector/GetDataTypeConfigWithFields', { data_type: dataType }, {
      headers: {
        'app_id': 'moox_frontend',
        'app_key': '2521e0d21b6be0347b72bca93904a0dd'
      }
    });
    const data = response as any;
    if (data.code === 200) {
      const detail = data.data && data.data.length > 0 ? data.data[0] : null;
      if (detail) {
        currentFieldConfigs.value = detail.fields || [];
        // 初始化动态表单数据
        initializeDynamicFormData(detail.fields || []);
        // 加载数据源选项，优先使用数据类型配置中的数据源选项
        if (detail.config?.data_source_options) {
          loadDataSourceOptions(detail.config.data_source_options);
        } else if (detail.fields && detail.fields.length > 0 && detail.fields[0].data_source_options) {
          // 如果数据类型配置中没有，尝试使用字段配置中的数据源选项
          loadDataSourceOptions(detail.fields[0].data_source_options);
        } else {
          // 如果都没有，使用默认选项
          loadDataSourceOptions();
        }
      } else {
        currentFieldConfigs.value = [];
        dataSourceOptions.value = [];
      }
    } else {
      Message.error(data.message || '获取字段配置失败');
      currentFieldConfigs.value = [];
    }
  } catch (error) {
    console.error('获取字段配置失败:', error);
    Message.error('获取字段配置失败');
    currentFieldConfigs.value = [];
  }
};

// 初始化动态表单数据
const initializeDynamicFormData = (fields: FieldConfig[]) => {
  dynamicFormData.value = {};
  fields.forEach(field => {
    let defaultValue: any = field.default_value;
    
    // 尝试解析JSON格式的默认值
    if (defaultValue) {
      try {
        defaultValue = JSON.parse(defaultValue);
        // 如果是多选类型，确保默认值是数组
        if (field.field_type === 'multi-select' && !Array.isArray(defaultValue)) {
          defaultValue = [];
        }
      } catch {
        // 如果解析失败，保持原值（对于非多选类型）
        if (field.field_type === 'multi-select') {
          defaultValue = [];
        }
      }
    } else {
      // 如果没有默认值，根据字段类型设置合适的默认值
      if (field.field_type === 'multi-select') {
        defaultValue = [];
      } else if (field.field_type === 'number') {
        defaultValue = undefined;
      } else {
        defaultValue = '';
      }
    }
    
    dynamicFormData.value[field.field_key] = defaultValue;
  });
};

const onAdd = () => {
  title.value = '新建采集规则';
  addForm.value = {
    rule_id: '',
    project_id: selectedProjectId.value || '',
    data_type: '',
    data_source: '',
    assignment_type: 'auto',
    assigned_nodes: '[]',
    node_pattern: '',
    collect_params: '{}',
    enabled: 'true',
    creator: account.value.user.userName || ''
  };
  assignedNodesList.value = [];
  // 重置动态表单数据
  currentFieldConfigs.value = [];
  dynamicFormData.value = {};
  dataSourceOptions.value = [];
  open.value = true;
};

const onUpdate = (record: TaskConfig) => {
  title.value = '修改采集规则';
  addForm.value = { ...record };
  
  // 解析 assigned_nodes
  try {
    assignedNodesList.value = JSON.parse(record.assigned_nodes || '[]');
  } catch {
    assignedNodesList.value = [];
  }

  // 如果有数据类型，加载对应的字段配置
  if (record.data_type) {
    getFieldConfigs(record.data_type).then(() => {
      // 加载现有的采集参数到动态表单
      try {
        const existingParams = JSON.parse(record.collect_params || '{}');
        Object.assign(dynamicFormData.value, existingParams);
      } catch (error) {
        console.error('解析现有采集参数失败:', error);
      }
      // 设置数据源值
      if (record.data_source) {
        addForm.value.data_source = record.data_source;
      }
    });
  }
  
  open.value = true;
};

// 数据类型变化处理
const onDataTypeChange = (value: string) => {
  addForm.value.data_type = value;
  // 重置数据源选择
  addForm.value.data_source = '';
  
  if (value) {
    getFieldConfigs(value);
  } else {
    currentFieldConfigs.value = [];
    dynamicFormData.value = {};
    dataSourceOptions.value = [];
  }
};

// 加载数据源选项
const loadDataSourceOptions = (dataSources?: string) => {
  loadingDataSources.value = true;
  dataSourceOptions.value = [];
  
  if (dataSources) {
    try {
      // 解析数据源配置
      const config = JSON.parse(dataSources);
      
      // 处理对象格式 {"options": [{"value": "binance", "label": "币安"}]}
      if (config.options && Array.isArray(config.options)) {
        dataSourceOptions.value = config.options.map((option: any) => ({
          label: option.label || getDataSourceLabel(option.value),
          value: option.value
        }));
      } 
      // 处理数组格式 ["binance", "okx"]
      else if (Array.isArray(config)) {
        dataSourceOptions.value = config.map((source: string) => ({
          label: getDataSourceLabel(source),
          value: source
        }));
      }
      else {
        console.warn('未知的数据源配置格式:', config);
        // 格式不匹配时提供默认选项
        dataSourceOptions.value = [
          { label: '币安 (Binance)', value: 'binance' },
          { label: 'OKX', value: 'okx' },
          { label: '火币 (Huobi)', value: 'huobi' },
          { label: 'Bybit', value: 'bybit' }
        ];
      }
    } catch (error) {
      console.error('解析数据源配置失败:', error);
      // 解析失败时提供默认选项
      dataSourceOptions.value = [
        { label: '币安 (Binance)', value: 'binance' },
        { label: 'OKX', value: 'okx' },
        { label: '火币 (Huobi)', value: 'huobi' },
        { label: 'Bybit', value: 'bybit' }
      ];
    }
  } else {
    // 如果没有提供数据源配置，提供默认选项
    dataSourceOptions.value = [
      { label: '币安 (Binance)', value: 'binance' },
      { label: 'OKX', value: 'okx' },
      { label: '火币 (Huobi)', value: 'huobi' },
      { label: 'Bybit', value: 'bybit' }
    ];
  }
  
  loadingDataSources.value = false;
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
  try {
    // 验证表单数据
    if (!addForm.value.data_type) {
      Message.error('请选择数据类型');
      return;
    }
    
    if (!addForm.value.data_source) {
      Message.error('请选择数据源');
      return;
    }
    
    // 验证动态表单字段
    if (currentFieldConfigs.value.length > 0) {
      for (const field of currentFieldConfigs.value) {
        const value = dynamicFormData.value[field.field_key];
        if (field.is_required && (value === undefined || value === null || value === '' || (Array.isArray(value) && value.length === 0))) {
          Message.error(`请输入${field.field_name}`);
          return;
        }
      }
    }
    
    // 处理分配类型数据
    if (addForm.value.assignment_type === 'fixed') {
      addForm.value.assigned_nodes = JSON.stringify(assignedNodesList.value || []);
    }
    
    // 处理采集参数
    addForm.value.collect_params = JSON.stringify(dynamicFormData.value || {});
    
    // 准备请求数据
    const requestData: any = {
      project_id: addForm.value.project_id || selectedProjectId.value || '',
      data_type: addForm.value.data_type,
      data_source: addForm.value.data_source,
      assignment_type: addForm.value.assignment_type,
      assigned_nodes: addForm.value.assigned_nodes || '[]',
      node_pattern: addForm.value.node_pattern || '',
      collect_params: addForm.value.collect_params,
      enabled: addForm.value.enabled || 'true',
      creator: addForm.value.creator || account.value.user?.userName || ''
    };
    
    // 如果是修改操作，添加rule_id
    if (title.value.includes('修改') && addForm.value.rule_id) {
      requestData.rule_id = addForm.value.rule_id;
    }
    
    const endpoint = title.value.includes('新建') ? '/gateway/collector/CreateTaskRule' : '/gateway/collector/UpdateTaskRule';
    
    // 发送请求
    const response = await service.post(endpoint, requestData, {
      headers: {
        'app_id': 'moox_frontend',
        'app_key': '2521e0d21b6be0347b72bca93904a0dd'
      }
    });

    const data = response as any;
    
    if (data.code === 200) {
      if (title.value.includes('新建')) {
        const ruleId = data.data && data.data.rule_id ? data.data.rule_id : '未知';
        Message.success(`创建成功，规则ID：${ruleId}`);
      } else {
        Message.success('更新成功');
      }
      open.value = false;
      getTaskList();
    } else {
      Message.error(data.message || (title.value.includes('新建') ? '创建失败' : '更新失败'));
    }
  } catch (error) {
    if (error && typeof error === 'object' && (error as any).message) {
      Message.error(`网络请求失败: ${(error as any).message}`);
    } else if (error instanceof SyntaxError) {
      Message.error('JSON格式错误，请检查输入');
    } else {
      Message.error(title.value.includes('新建') ? '创建失败，请检查网络连接' : '更新失败，请检查网络连接');
    }
  }
};



const handleEnableChange = async (record: TaskConfig, value: boolean) => {
  try {
    const response = await service.post('/gateway/collector/UpdateTaskRule', { 
      ...record,
      enabled: value ? 'true' : 'false'
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


const onViewDetails = (record: TaskConfig) => {
  detailData.value = record;
  detailVisible.value = true;
};

// Watch for project changes
watch(selectedProjectId, () => {
  getTaskList();
});

// Watch for search data type changes
watch(() => form.value.dataType, (newDataType) => {
  // 当数据类型变化时，清空数据源选择
  if (newDataType) {
    form.value.dataSource = '';
  }
});

onMounted(() => {
  getTaskList();
  getNodeList();
  getDataTypeConfigs();
});
</script>

<style scoped>
.moox-page {
  padding: 16px;
  height: 100%;
}

.moox-inner {
  height: 100%;
  background: #fff;
  padding: 16px;
  border-radius: 4px;
}

.moox-inner .a-row {
  margin-top: 16px;
}

.moox-inner .a-table {
  margin-top: 16px;
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