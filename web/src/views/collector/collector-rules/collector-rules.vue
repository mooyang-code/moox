<template>
  <div class="moox-page">
    <a-spin :loading="loading">
      <div class="moox-inner">
        <SpaceContextBar />
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
            <a-table-column title="云节点匹配规则" data-index="assignment_type" :width="120">
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
                    :type="record.enabled === 'true' ? 'outline' : 'primary'"
                    :status="record.enabled === 'true' ? 'warning' : 'success'"
                    size="mini"
                    @click="handleEnableChange(record, record.enabled !== 'true')">
                    <template #icon>
                      <icon-close-circle v-if="record.enabled === 'true'" />
                      <icon-check-circle v-else />
                    </template>
                    <span>{{ record.enabled === 'true' ? '禁用' : '启用' }}</span>
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
    <a-modal v-model:visible="open" @close="afterClose" @cancel="afterClose" width="900px" :ok-loading="submitLoading" @before-ok="handleOk">
      <template #title> {{ title }} </template>
      <div>
        <a-form ref="formRef" auto-label-width :rules="rules" :model="addForm" :layout="'vertical'">
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
              <a-form-item field="assignment_type" label="云节点匹配规则">
                <a-select v-model="addForm.assignment_type" placeholder="请选择云节点匹配规则" @change="onAssignmentTypeChange">
                  <a-option value="auto">自动分配</a-option>
                  <a-option value="fixed">固定节点</a-option>
                  <a-option value="pattern">模式匹配</a-option>
                  <a-option value="tag">标签匹配</a-option>
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

          <a-form-item v-if="addForm.assignment_type === 'tag'" label="节点标签">
            <a-select v-model="nodeTagsList" placeholder="请选择节点标签" multiple allow-clear>
              <a-option value="国内">国内</a-option>
              <a-option value="海外">海外</a-option>
            </a-select>
          </a-form-item>
        </a-form>

        <!-- 采集参数配置 - 完全独立于表单 -->
        <a-divider>采集参数配置</a-divider>

        <template v-if="addForm.data_type">
          <!-- 产品类型选择 (inst_type) - 仅K线、逐笔交易、行情、订单簿显示 -->
          <div v-if="hasField('inst_type')" class="custom-form-item">
            <div class="custom-form-label">产品类型</div>
            <a-radio-group v-model="instTypeValue" type="button">
              <a-radio value="SPOT">现货</a-radio>
              <a-radio value="SWAP">永续合约</a-radio>
              <a-radio value="FUTURES">交割合约</a-radio>
            </a-radio-group>
            <div class="custom-form-extra">选择采集的产品类型：现货交易对或合约交易对</div>
          </div>

          <!-- 产品类型多选 (inst_types) - 仅标的数据显示 -->
          <div v-if="hasField('inst_types')" class="custom-form-item">
            <div class="custom-form-label">产品类型</div>
            <a-checkbox-group v-model="instTypesValue">
              <a-checkbox value="SPOT">现货</a-checkbox>
              <a-checkbox value="SWAP">永续合约</a-checkbox>
              <a-checkbox value="FUTURES">交割合约</a-checkbox>
              <a-checkbox value="OPTION">期权</a-checkbox>
            </a-checkbox-group>
            <div class="custom-form-extra">选择要同步的产品类型，可多选</div>
          </div>

          <!-- 标的列表输入 (objects) -->
          <div v-if="hasField('objects')" class="custom-form-item">
            <div class="custom-form-label">交易标的</div>
            <div class="objects-input-wrapper">
              <a-checkbox
                v-model="objectsSelectAll"
                @change="onObjectsSelectAllChange">
                全部标的
              </a-checkbox>
              <a-input-tag
                v-show="!objectsSelectAll"
                v-model="objectsValue"
                placeholder="输入标的后按回车添加，如 BTC-USDT 或 BTC-*"
                allow-clear
                :style="{ marginTop: '8px' }" />
              <div v-show="objectsSelectAll" class="select-all-hint">
                已选择全部标的，将采集所有可用交易对数据
              </div>
            </div>
            <div class="custom-form-extra">支持通配符：* 匹配任意字符，如 BTC-* 匹配所有BTC交易对（注意：输入后按回车键才会生效！）</div>
          </div>

          <!-- K线周期选择 (intervals) -->
          <div v-if="hasField('intervals')" class="custom-form-item">
            <div class="custom-form-label">时间周期</div>
            <a-checkbox-group
              v-model="intervalsValue"
              :options="INTERVAL_OPTIONS">
            </a-checkbox-group>
          </div>

          <!-- 订单簿深度 (depth) -->
          <div v-if="hasField('depth')" class="custom-form-item">
            <div class="custom-form-label">订单簿深度</div>
            <a-input-number
              v-model="depthValue"
              placeholder="请输入订单簿深度"
              :min="1"
              :max="1000"
              :style="{ width: '200px' }" />
          </div>

          <!-- 新闻来源 (sources) -->
          <div v-if="hasField('sources')" class="custom-form-item">
            <div class="custom-form-label">新闻来源</div>
            <a-input-tag
              v-model="sourcesValue"
              placeholder="输入新闻来源后按回车添加"
              allow-clear />
          </div>

          <!-- 关键词 (keywords) -->
          <div v-if="hasField('keywords')" class="custom-form-item">
            <div class="custom-form-label">关键词</div>
            <a-input-tag
              v-model="keywordsValue"
              placeholder="输入关键词后按回车添加"
              allow-clear />
          </div>
        </template>

        <template v-else>
          <a-alert type="info">请先选择数据类型以配置采集参数</a-alert>
        </template>

        <!-- 其他表单字段 -->
        <a-form auto-label-width :layout="'vertical'" style="margin-top: 16px;">
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
        <a-descriptions-item label="云节点匹配规则">{{ getAssignmentText(detailData.assignment_type || '') }}</a-descriptions-item>
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
          <div v-else-if="detailData.assignment_type === 'tag'">
            节点标签：{{ detailData.node_tags }}
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
import SpaceContextBar from '@/components/SpaceContextBar/index.vue';
import { ref, computed, onMounted, watch } from 'vue';
import { Message } from '@arco-design/web-vue';
import { callControl } from '@/api/admin/http';
import { getNodeList as fetchCloudNodeList } from '@/api/cloud-node';
import { useSpaceStore } from '@/store/modules/space';
import { useUserInfoStore } from '@/store/modules/user-info';
import { storeToRefs } from 'pinia';

interface TaskConfig {
  id?: number;
  rule_id: string;
  space_id: string;
  data_type: string;
  data_source: string;
  assignment_type: string;
  assigned_nodes: string;
  node_pattern: string;
  node_tags: string;
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
  data_source_options: Record<string, any>;
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
  default_value: any;
  field_options: Record<string, any>;
  data_source_options: Record<string, any>;
  sort_order: number;
  create_time: string;
  modify_time: string;
}

// interface DataTypeConfigDetail {
//   config: DataTypeConfig;
//   fields: FieldConfig;
// }

const loading = ref(false);
const submitLoading = ref(false);
const taskList = ref<TaskConfig[]>([]);
const selectedKeys = ref<string[]>([]);
const open = ref(false);
const title = ref('新建任务');
const formRef = ref();
const detailVisible = ref(false);
const detailData = ref<Partial<TaskConfig>>({});
const nodeOptions = ref<any[]>([]);
const assignedNodesList = ref<string[]>([]);
const nodeTagsList = ref<string[]>([]);
const activeDataType = ref(''); // 用于跟踪当前激活的数据类型，防止重复初始化

// 数据类型配置相关数据
const dataTypeConfigs = ref<DataTypeConfig[]>([]);
const currentFieldConfigs = ref<FieldConfig[]>([]);

// 动态字段使用独立的 ref，避免相互干扰
const instTypeValue = ref<string>('SPOT'); // 产品类型：SPOT-现货, SWAP-永续合约, FUTURES-交割合约
const instTypesValue = ref<string[]>(['SPOT']); // 产品类型多选：用于标的数据任务
const objectsValue = ref<string[]>([]);
const intervalsValue = ref<string[]>([]);
const depthValue = ref<number | undefined>(undefined);
const sourcesValue = ref<string[]>([]);
const keywordsValue = ref<string[]>([]);

// 数据源相关数据
const dataSourceOptions = ref<{ label: string; value: string }[]>([]);
const loadingDataSources = ref(false);

// 标的"全部"选项状态
const objectsSelectAll = ref(false);

// CollectParams 中定义的有效字段（根据数据类型动态过滤）
const COLLECT_PARAMS_FIELDS: { [dataType: string]: string[] } = {
  // 标的数据：产品类型（多选）、时间周期
  'symbol': ['inst_types', 'intervals'],
  // K线数据：产品类型、标的、周期
  'kline': ['inst_type', 'objects', 'intervals'],
  // 逐笔交易：产品类型、标的
  'trade': ['inst_type', 'objects'],
  // 行情数据：产品类型、标的
  'ticker': ['inst_type', 'objects'],
  // 订单簿：产品类型、标的、深度
  'orderbook': ['inst_type', 'objects', 'depth'],
  // 新闻资讯：来源、关键词（不需要产品类型）
  'news': ['sources', 'keywords'],
  // 默认：所有字段
  'default': ['inst_type', 'objects', 'intervals', 'depth', 'sources', 'keywords']
};

// Get Space store
const spaceStore = useSpaceStore();
const { selectedSpaceId } = storeToRefs(spaceStore);

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
  space_id: '',
  data_type: '',
  data_source: '',
  assignment_type: 'auto',
  assigned_nodes: '[]',
  node_pattern: '',
  node_tags: '[]',
  collect_params: '{}',
  enabled: 'true',
  creator: ''
});

const rules = {
  data_type: [{ required: true, message: '请选择数据类型' }],
  data_source: [{ required: true, message: '请选择数据源' }]
};

// 处理"全部标的"复选框变化
const onObjectsSelectAllChange = (checked: boolean | (string | number | boolean)[]) => {
  const isChecked = Array.isArray(checked) ? checked.length > 0 : checked;
  if (isChecked) {
    // 选择全部时，设置为 ["*"]
    objectsValue.value = ['*'];
  } else {
    // 取消选择全部时，清空列表
    objectsValue.value = [];
  }
};

// 检查当前数据类型是否需要某个字段
const hasField = (fieldKey: string) => {
  const dataType = addForm.value.data_type?.toLowerCase() || '';
  const allowedFields = COLLECT_PARAMS_FIELDS[dataType] || COLLECT_PARAMS_FIELDS['default'];
  return allowedFields.includes(fieldKey);
};

const normalizeCollectToken = (value: any, fallback = '') => {
  const text = String(value || '').trim().toLowerCase();
  return text || fallback;
};

const inferCollectMarket = (instTypeValue: any) => {
  const instType = String(instTypeValue || '').trim().toUpperCase();
  if (instType === 'SWAP' || instType === 'FUTURES') {
    return 'swap';
  }
  return 'spot';
};

const instTypeFromMarket = (marketValue: any) => {
  const market = String(marketValue || '').trim().toLowerCase();
  if (market === 'swap' || market === 'futures') {
    return 'SWAP';
  }
  return 'SPOT';
};

const inferCollectDatasetId = (exchange: string, market: string, dataType: string) => {
  return `${exchange || 'binance'}_${market || 'spot'}_${dataType || 'kline'}`;
};

const buildStandardCollectParams = () => {
  const existingParams = normalizeObject(addForm.value.collect_params);
  const existingSource = normalizeObject(existingParams.source);
  const existingCollector = normalizeObject(existingParams.collector);
  const existingTarget = normalizeObject(existingParams.target);
  const existingSchedule = normalizeObject(existingParams.schedule);
  const dataType = normalizeCollectToken(addForm.value.data_type, 'kline');
  const exchange = normalizeCollectToken(addForm.value.data_source, 'binance');
  const instTypesValue = getDynamicFieldValue('inst_types');
  const instTypes = Array.isArray(instTypesValue) ? instTypesValue : [];
  const market = inferCollectMarket(getDynamicFieldValue('inst_type') || instTypes[0]);
  const intervalsValue = getDynamicFieldValue('intervals');
  const intervals = (Array.isArray(intervalsValue) ? intervalsValue : [])
    .map((item) => String(item || '').trim())
    .filter(Boolean);
  const normalizedIntervals = dataType === 'symbol' ? [] : (intervals.length > 0 ? intervals : ['1m']);
  const defaultDatasetId = inferCollectDatasetId(exchange, market, dataType);
  const defaultJobType = `collect.${dataType}`;
  const defaultCodePackageId = 'moox-collector_dev';
  const sameCollectShape = (
    normalizeCollectToken(existingCollector.exchange, exchange) === exchange &&
    normalizeCollectToken(existingCollector.market, market) === market &&
    normalizeCollectToken(existingCollector.data_type, dataType) === dataType
  );
  const datasetId = sameCollectShape
    ? String(existingSource.dataset_id || existingTarget.dataset_id || defaultDatasetId)
    : defaultDatasetId;
  const sourceKind = dataType === 'symbol'
    ? 'none'
    : (sameCollectShape ? normalizeCollectToken(existingSource.kind, 'dataset_subjects') : 'dataset_subjects');
  const jobType = sameCollectShape
    ? String(existingTarget.job_type || defaultJobType)
    : defaultJobType;
  const codePackageId = sameCollectShape
    ? String(existingTarget.code_package_id || defaultCodePackageId)
    : defaultCodePackageId;

  return {
    source: {
      kind: sourceKind,
      dataset_id: datasetId
    },
    collector: {
      exchange,
      market,
      data_type: dataType,
      intervals: normalizedIntervals
    },
    target: {
      dataset_id: datasetId,
      job_type: jobType,
      code_package_id: codePackageId
    },
    schedule: {
      interval: sameCollectShape ? String(existingSchedule.interval || '30m') : '30m',
      timezone: sameCollectShape ? String(existingSchedule.timezone || 'Asia/Shanghai') : 'Asia/Shanghai',
      intervals: normalizedIntervals
    }
  };
};

// K线周期选项（常量，避免每次渲染重新创建）
const INTERVAL_OPTIONS = [
  { label: '1分钟', value: '1m' },
  { label: '3分钟', value: '3m' },
  { label: '5分钟', value: '5m' },
  { label: '15分钟', value: '15m' },
  { label: '30分钟', value: '30m' },
  { label: '1小时', value: '1h' },
  { label: '2小时', value: '2h' },
  { label: '4小时', value: '4h' },
  { label: '6小时', value: '6h' },
  { label: '12小时', value: '12h' },
  { label: '1天', value: '1d' },
  { label: '1周', value: '1w' },
  { label: '1月', value: '1M' }
];

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
    pattern: '模式匹配',
    tag: '标签匹配'
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

// 当前 collector 只内置 Binance 实现。不要在管理台暴露尚未实现的数据源，
// 避免生成无法执行的采集规则。
const supportedDataSourceOptions = [
  { label: '币安 (Binance)', value: 'binance' }
];

const supportedDataSourceValues = new Set(supportedDataSourceOptions.map((option) => option.value));

const toSupportedDataSourceOptions = (options: Array<{ label?: string; value?: string }>) => {
  const normalized = options
    .filter((option) => option.value && supportedDataSourceValues.has(option.value))
    .map((option) => ({
      label: option.label || getDataSourceLabel(option.value as string),
      value: option.value as string
    }));
  return normalized.length > 0 ? normalized : supportedDataSourceOptions;
};

const normalizeObject = (value: any): Record<string, any> => {
  if (!value) return {};
  if (typeof value === 'object') return value;
  try {
    const parsed = JSON.parse(String(value));
    return parsed && typeof parsed === 'object' ? parsed : { value: parsed };
  } catch {
    return { raw: String(value) };
  }
};

const normalizeArray = (value: any): string[] => {
  if (Array.isArray(value)) {
    return value.map((item) => String(item));
  }
  if (!value) return [];
  try {
    const parsed = JSON.parse(String(value));
    return Array.isArray(parsed) ? parsed.map((item) => String(item)) : [];
  } catch {
    return [];
  }
};

const normalizeTaskConfig = (raw: any): TaskConfig => ({
  id: raw.id,
  rule_id: raw.rule_id || '',
  space_id: raw.space_id || '',
  data_type: raw.data_type || '',
  data_source: raw.exchange || raw.data_source || '',
  assignment_type: raw.assignment_type || 'auto',
  assigned_nodes: JSON.stringify(normalizeArray(raw.assigned_nodes)),
  node_pattern: raw.node_pattern || '',
  node_tags: JSON.stringify(normalizeArray(raw.node_tags)),
  collect_params: JSON.stringify(normalizeObject(raw.collect_params)),
  enabled: (raw.enabled ?? true) ? 'true' : 'false',
  creator: raw.creator || '',
  create_time: raw.create_time || '',
  modify_time: raw.modify_time || ''
});

const dataSourceOptionsFromConfig = (value: any) => {
  const config = normalizeObject(value);
  if (config.options && Array.isArray(config.options)) {
    return toSupportedDataSourceOptions(config.options.map((option: any) => ({
      label: option.label || getDataSourceLabel(option.value),
      value: option.value
    })));
  }
  if (Array.isArray(value)) {
    return toSupportedDataSourceOptions(value.map((source: string) => ({
      label: getDataSourceLabel(source),
      value: source
    })));
  }
  return [];
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
  
  return dataSourceOptionsFromConfig(selectedConfig.data_source_options);
};

const formatJSON = (value: any) => JSON.stringify(normalizeObject(value), null, 2);

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

const onEnabledChange = () => {
  // 当启用状态开关变化时，重新查询列表
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
  const spaceId = selectedSpaceId.value || '';
  if (!spaceId) {
    taskList.value = [];
    pagination.value.total = 0;
    loading.value = false;
    return;
  }
  loading.value = true;
  try {
    const params: any = {
      space_id: spaceId,
      page: {
        page: pagination.value.current,
        size: pagination.value.pageSize
      }
    };

    if (form.value.ruleId) params.rule_id = form.value.ruleId;
    if (form.value.dataType) params.data_type = form.value.dataType;
    if (form.value.dataSource) params.exchange = form.value.dataSource;
    if (form.value.enabled !== null) params.enabled = form.value.enabled;

    const data = await callControl<typeof params, { rules?: any[]; page?: { total?: number } }>('collectmgr', 'GetTaskRuleList', params);
    taskList.value = (data.rules || []).map(normalizeTaskConfig);
    pagination.value.total = Number(data.page?.total) || (data.rules ? data.rules.length : 0);
  } catch (error) {
    console.error('获取任务列表失败:', error);
    Message.error('获取任务列表失败');
  } finally {
    loading.value = false;
  }
};

const getNodeList = async () => {
  try {
    const data = await fetchCloudNodeList();
    nodeOptions.value = data.items || [];
  } catch (error) {
    console.error('获取节点列表失败:', error);
  }
};

// 获取数据类型配置
const getDataTypeConfigs = async () => {
  try {
    const data = await callControl<Record<string, never>, { configs?: DataTypeConfig[] }>('collectmgr', 'GetDataTypeConfigs', {});
    dataTypeConfigs.value = data.configs || [];
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
    const data = await callControl<{ data_type: string }, { detail?: { config?: DataTypeConfig; fields?: FieldConfig[] } }>(
      'collectmgr',
      'GetDataTypeConfigWithFields',
      { data_type: dataType }
    );
    const detail = data.detail || null;
    if (detail) {
      currentFieldConfigs.value = detail.fields || [];
      // 注意：不在这里调用 initializeDynamicFormData，由调用方控制初始化
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
  } catch (error) {
    console.error('获取字段配置失败:', error);
    Message.error('获取字段配置失败');
    currentFieldConfigs.value = [];
  }
};

// 初始化动态表单数据（使用独立的 ref 变量）
const initializeDynamicFormData = (existingParams?: { [key: string]: any }) => {
  objectsSelectAll.value = false;
  const collectorParams = normalizeObject(existingParams?.collector);
  const scheduleParams = normalizeObject(existingParams?.schedule);
  const instType = existingParams?.inst_type ?? instTypeFromMarket(collectorParams.market);
  const intervalParams = existingParams?.intervals ?? collectorParams.intervals ?? scheduleParams.intervals;

  // 解析并设置 inst_type（产品类型）
  if (instType !== undefined) {
    instTypeValue.value = instType || 'SPOT';
  } else {
    instTypeValue.value = 'SPOT';
  }

  // 解析并设置 inst_types（产品类型多选，用于标的数据）
  if (existingParams?.inst_types !== undefined) {
    const instTypesVal = existingParams.inst_types;
    instTypesValue.value = Array.isArray(instTypesVal) ? instTypesVal : (instTypesVal ? [instTypesVal] : ['SPOT']);
  } else if (collectorParams.market !== undefined) {
    instTypesValue.value = [instTypeFromMarket(collectorParams.market)];
  } else {
    instTypesValue.value = ['SPOT'];
  }

  // 解析并设置 objects
  if (existingParams?.objects !== undefined) {
    const objVal = existingParams.objects;
    objectsValue.value = Array.isArray(objVal) ? objVal : (objVal ? [objVal] : []);
    // 检查是否为全部标的
    if (objectsValue.value.length === 1 && objectsValue.value[0] === '*') {
      objectsSelectAll.value = true;
    }
  } else {
    objectsValue.value = [];
  }

  // 解析并设置 intervals
  if (intervalParams !== undefined) {
    const intVal = intervalParams;
    intervalsValue.value = Array.isArray(intVal) ? intVal : (intVal ? [intVal] : []);
  } else {
    intervalsValue.value = [];
  }

  // 解析并设置 depth
  if (existingParams?.depth !== undefined) {
    depthValue.value = typeof existingParams.depth === 'number' ? existingParams.depth : undefined;
  } else {
    depthValue.value = undefined;
  }

  // 解析并设置 sources
  if (existingParams?.sources !== undefined) {
    const srcVal = existingParams.sources;
    sourcesValue.value = Array.isArray(srcVal) ? srcVal : (srcVal ? [srcVal] : []);
  } else {
    sourcesValue.value = [];
  }

  // 解析并设置 keywords
  if (existingParams?.keywords !== undefined) {
    const kwVal = existingParams.keywords;
    keywordsValue.value = Array.isArray(kwVal) ? kwVal : (kwVal ? [kwVal] : []);
  } else {
    keywordsValue.value = [];
  }
};

// 重置所有动态字段
const resetDynamicFields = () => {
  instTypeValue.value = 'SPOT';
  instTypesValue.value = ['SPOT'];
  objectsValue.value = [];
  intervalsValue.value = [];
  depthValue.value = undefined;
  sourcesValue.value = [];
  keywordsValue.value = [];
  objectsSelectAll.value = false;
};

const onAdd = () => {
  title.value = '新建采集规则';
  addForm.value = {
    rule_id: '',
    space_id: selectedSpaceId.value || '',
    data_type: '',
    data_source: '',
    assignment_type: 'auto',
    assigned_nodes: '[]',
    node_pattern: '',
    node_tags: '[]',
    collect_params: '{}',
    enabled: 'true',
    creator: account.value.user.userName || ''
  };
  assignedNodesList.value = [];
  nodeTagsList.value = [];
  currentFieldConfigs.value = [];
  resetDynamicFields();
  dataSourceOptions.value = [];
  activeDataType.value = '';
  open.value = true;
};

const onUpdate = (record: TaskConfig) => {
  title.value = '修改采集规则';
  addForm.value = { ...record };
  activeDataType.value = record.data_type;

  // 解析 assigned_nodes
  try {
    assignedNodesList.value = JSON.parse(record.assigned_nodes || '[]');
  } catch {
    assignedNodesList.value = [];
  }

  // 解析 node_tags
  try {
    nodeTagsList.value = JSON.parse(record.node_tags || '[]');
  } catch {
    nodeTagsList.value = [];
  }

  // 解析现有的采集参数
  let existingParams: { [key: string]: any } = {};
  try {
    existingParams = JSON.parse(record.collect_params || '{}');
  } catch (error) {
    console.error('解析现有采集参数失败:', error);
  }

  // 使用现有参数初始化动态表单
  initializeDynamicFormData(existingParams);

  // 如果有数据类型，加载对应的字段配置
  if (record.data_type) {
    getFieldConfigs(record.data_type).then(() => {
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
  // 如果数据类型没有实际变化，不执行任何操作
  if (value === activeDataType.value) {
    return;
  }
  activeDataType.value = value;

  addForm.value.data_type = value;
  // 重置数据源选择
  addForm.value.data_source = '';
  // 重置全部标的选项
  objectsSelectAll.value = false;

  if (value) {
    // 注意：切换数据类型时不重置已填写的字段值，只加载字段配置
    getFieldConfigs(value);
  } else {
    currentFieldConfigs.value = [];
    resetDynamicFields();
    dataSourceOptions.value = [];
  }
};

// 加载数据源选项
const loadDataSourceOptions = (dataSources?: any) => {
  loadingDataSources.value = true;
  dataSourceOptions.value = [];
  
  if (dataSources) {
    const options = dataSourceOptionsFromConfig(dataSources);
    dataSourceOptions.value = options.length > 0 ? options : supportedDataSourceOptions;
  } else {
    // 如果没有提供数据源配置，仅提供当前已实现的数据源
    dataSourceOptions.value = supportedDataSourceOptions;
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
  if (value !== 'tag') {
    nodeTagsList.value = [];
    addForm.value.node_tags = '[]';
  }
};

const afterClose = () => {
  formRef.value?.clearValidate();
  open.value = false;
};

// 获取动态字段值的辅助函数
const getDynamicFieldValue = (fieldKey: string): any => {
  switch (fieldKey) {
    case 'inst_type': return instTypeValue.value;
    case 'inst_types': return instTypesValue.value;
    case 'objects': return objectsValue.value;
    case 'intervals': return intervalsValue.value;
    case 'depth': return depthValue.value;
    case 'sources': return sourcesValue.value;
    case 'keywords': return keywordsValue.value;
    default: return undefined;
  }
};

const handleOk = async (): Promise<boolean> => {
  try {
    // 验证表单数据
    if (!addForm.value.data_type) {
      Message.error('请选择数据类型');
      return false;
    }

    if (!addForm.value.data_source) {
      Message.error('请选择数据源');
      return false;
    }
    const spaceId = addForm.value.space_id || selectedSpaceId.value || '';
    if (!spaceId) {
      Message.error('请选择空间');
      return false;
    }

    // 验证交易标的（当数据类型需要 objects 字段时）
    if (hasField('objects') && (!objectsValue.value || objectsValue.value.length === 0)) {
      Message.error('请输入交易标的');
      return false;
    }

    // 处理云节点匹配规则数据
    if (addForm.value.assignment_type === 'fixed') {
      addForm.value.assigned_nodes = JSON.stringify(assignedNodesList.value || []);
    }
    if (addForm.value.assignment_type === 'tag') {
      addForm.value.node_tags = JSON.stringify(nodeTagsList.value || []);
    }

    addForm.value.collect_params = JSON.stringify(buildStandardCollectParams());
    const collectParams = normalizeObject(addForm.value.collect_params);

    // 准备请求数据
    const requestData: any = {
      space_id: spaceId,
      data_type: addForm.value.data_type,
      exchange: addForm.value.data_source,
      assignment_type: addForm.value.assignment_type,
      assigned_nodes: normalizeArray(addForm.value.assigned_nodes),
      node_pattern: addForm.value.node_pattern || '',
      node_tags: normalizeArray(addForm.value.node_tags),
      collect_params: collectParams,
      enabled: addForm.value.enabled !== 'false',
      creator: addForm.value.creator || account.value.user?.userName || ''
    };

    // 如果是修改操作，添加rule_id
    if (title.value.includes('修改') && addForm.value.rule_id) {
      requestData.rule_id = addForm.value.rule_id;
    }

    const method = title.value.includes('新建') ? 'CreateTaskRule' : 'UpdateTaskRule';

    submitLoading.value = true;
    const payload = title.value.includes('新建')
      ? { rule: requestData }
      : { space_id: requestData.space_id, rule_id: requestData.rule_id, rule: requestData };
    const data = await callControl<typeof payload, { rule_id?: string }>('collectmgr', method, payload);
    if (title.value.includes('新建')) {
      const ruleId = data.rule_id || '未知';
      Message.success(`创建成功，规则ID：${ruleId}`);
    } else {
      Message.success('更新成功');
    }
    getTaskList();
    return true;
  } catch (error) {
    if (error && typeof error === 'object' && (error as any).message) {
      Message.error(`网络请求失败: ${(error as any).message}`);
    } else if (error instanceof SyntaxError) {
      Message.error('JSON格式错误，请检查输入');
    } else {
      Message.error(title.value.includes('新建') ? '创建失败，请检查网络连接' : '更新失败，请检查网络连接');
    }
    return false;
  } finally {
    submitLoading.value = false;
  }
};



const handleEnableChange = async (record: TaskConfig, value: boolean) => {
  try {
    const spaceId = record.space_id || selectedSpaceId.value || '';
    if (!spaceId) {
      Message.error('请选择空间');
      return;
    }
    const rule = {
      space_id: spaceId,
      rule_id: record.rule_id,
      data_type: record.data_type,
      exchange: record.data_source,
      assignment_type: record.assignment_type,
      assigned_nodes: normalizeArray(record.assigned_nodes),
      node_pattern: record.node_pattern || '',
      node_tags: normalizeArray(record.node_tags),
      collect_params: normalizeObject(record.collect_params),
      enabled: value,
      creator: record.creator || account.value.user?.userName || ''
    };
    await callControl<Record<string, any>, Record<string, never>>('collectmgr', 'UpdateTaskRule', {
      space_id: spaceId,
      rule_id: record.rule_id,
      rule
    });
    Message.success('状态更新成功');
    getTaskList();
  } catch (error) {
    console.error('状态更新失败:', error);
    Message.error('状态更新失败');
  }
};


const onViewDetails = (record: TaskConfig) => {
  detailData.value = record;
  detailVisible.value = true;
};

// Watch for Space changes
watch(selectedSpaceId, () => {
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

.objects-input-wrapper {
  width: 100%;
}

.select-all-hint {
  margin-top: 8px;
  padding: 8px 12px;
  background: #f0f9eb;
  border: 1px solid #c6e7c6;
  border-radius: 4px;
  color: #67c23a;
  font-size: 13px;
}

.custom-form-item {
  margin-bottom: 20px;
}

.custom-form-label {
  margin-bottom: 8px;
  color: var(--color-text-2);
  font-size: 14px;
}

.custom-form-extra {
  margin-top: 4px;
  color: var(--color-text-3);
  font-size: 12px;
}

:deep(.arco-checkbox-group) {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
}

:deep(.arco-checkbox-group .arco-checkbox) {
  margin-right: 0;
}

:deep(.arco-input-tag) {
  min-height: 32px;
}
</style>
