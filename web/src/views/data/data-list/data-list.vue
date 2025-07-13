<template>
  <div class="moox-page">
    <div class="container">
      <!-- 加载状态 -->
      <div v-if="pageLoading" class="loading-container">
        <a-spin :size="32" tip="加载中..." />
      </div>

      <!-- Tab切换区域 -->
      <a-card v-else :bordered="false">
        <div v-if="datasets.length === 0" class="empty-state">
          <a-empty description="暂无数据集" />
        </div>

        <a-tabs v-else :type="type" :size="size" v-model:active-key="activeTab">
          <a-tab-pane
            v-for="dataset in datasets"
            :key="dataset.dataset_id.toString()"
            :title="dataset.dataset_name"
          >
            <!-- 数据列表内容区域 -->
            <div class="data-list-content">
              <!-- 左侧对象树形列表 -->
              <div class="left-box">
                <a-input v-model="objectSearchKeyword" placeholder="请输入对象名称" allow-clear>
                  <template #prefix>
                    <icon-search />
                  </template>
                </a-input>
                <div class="tree-box">
                  <div v-if="loading" class="tree-loading">
                    <a-spin :size="24" tip="加载中..." />
                  </div>
                  <div v-else-if="filteredObjectTree.length === 0" class="tree-empty">
                    <a-empty description="暂无数据对象" size="small" />
                  </div>
                  <a-tree
                    v-else
                    ref="objectTreeRef"
                    :data="filteredObjectTree"
                    :field-names="objectFieldNames"
                    :selected-keys="selectedObjectKeys"
                    show-line
                    @select="onSelectObject"
                  >
                  </a-tree>
                </div>
              </div>

              <!-- 右侧数据列表 -->
              <div class="right-box">
                <!-- 搜索区域 -->
                <a-space wrap align="center" style="width: 100%; justify-content: space-between; margin-bottom: 1px;">
                  <!-- 左侧搜索组件 -->
                  <a-space wrap>
                    <a-range-picker v-model="searchForm.dateRange" show-time format="YYYY-MM-DD HH:mm" allow-clear />
                    <a-button type="primary" @click="handleSearch">
                      <template #icon><icon-search /></template>
                      <span>查询</span>
                    </a-button>
                    <a-button @click="handleReset">
                      <template #icon><icon-refresh /></template>
                      <span>重置</span>
                    </a-button>
                  </a-space>

                  <!-- 右侧当前选中对象信息 -->
                  <a-alert v-if="currentObject" type="info" show-icon class="compact-alert">
                    <template #icon><icon-info-circle /></template>
                    当前查看对象：<strong>{{ currentObject.object_id }}</strong>
                    <span v-if="currentObject.object_name">（{{ currentObject.object_name }}）</span>
                  </a-alert>
                </a-space>

                <!-- 数据列表表格容器 -->
                <div class="table-box">
                  <!-- 数据列表表格 -->
                  <a-table
                  row-key="row_id"
                  :data="dataList"
                  :bordered="{ cell: true }"
                  :loading="loading"
                  :scroll="{ x: '120%' }"
                  :pagination="{
                    current: pagination.current,
                    pageSize: pagination.pageSize,
                    total: pagination.total,
                    showTotal: true,
                    showPageSize: true,
                    showJumper: true,
                    hideOnSinglePage: false,
                    pageSizeOptions: [10, 20, 50, 100]
                  }"
                  size="small"
                  @page-change="onPageChange"
                  @page-size-change="onPageSizeChange"
                >
                  <template #columns>
                    <a-table-column title="序号" :width="64" align="center">
                      <template #cell="{ rowIndex }">{{ (pagination.current - 1) * pagination.pageSize + rowIndex + 1 }}</template>
                    </a-table-column>
                    <a-table-column title="行ID" data-index="row_id" :width="120"></a-table-column>
                    <a-table-column v-if="currentObject && isTimeSeriesData" title="时间" data-index="times" :width="180"></a-table-column>
                    <!-- 动态字段列 - 只显示数据列表字段(metadata_flag!=1)且在数据中实际存在的字段 -->
                    <a-table-column
                      v-for="field in actualDisplayFields"
                      :key="field.interface_name"
                      :title="field.field_name || field.interface_name"
                      :width="150"
                      :ellipsis="true"
                      :tooltip="true"
                    >
                      <template #cell="{ record }">
                        {{ formatFieldValue(record.fields[field.interface_name]) }}
                      </template>
                    </a-table-column>
                    <a-table-column title="操作" :width="120" align="center" :fixed="'right'">
                      <template #cell="{ record }">
                        <a-space>
                          <a-button type="primary" size="mini" @click="handleView(record)">
                            <template #icon><icon-eye /></template>
                            <span>查看</span>
                          </a-button>
                        </a-space>
                      </template>
                    </a-table-column>
                  </template>
                  </a-table>
                </div>
              </div>
            </div>
          </a-tab-pane>
        </a-tabs>
      </a-card>
    </div>

    <!-- 数据详情查看弹窗 -->
    <a-modal
      v-model:visible="dataModalVisible"
      title="数据详情"
      width="800px"
      :footer="false"
    >
      <div v-if="currentDataRow">
        <a-descriptions :column="2" bordered>
          <a-descriptions-item label="行ID">{{ currentDataRow.row_id }}</a-descriptions-item>
          <a-descriptions-item v-if="currentDataRow.times" label="时间">{{ currentDataRow.times }}</a-descriptions-item>
        </a-descriptions>
        
        <a-divider>字段数据</a-divider>
        
        <a-table
          :data="fieldDataList"
          :pagination="false"
          :bordered="{ cell: true }"
          size="small"
        >
          <template #columns>
            <a-table-column title="字段名" data-index="field_name" :width="150"></a-table-column>
            <a-table-column title="字段值" data-index="field_value" :ellipsis="true" :tooltip="true"></a-table-column>
            <a-table-column title="字段类型" data-index="field_type" :width="100" align="center"></a-table-column>
          </template>
        </a-table>
      </div>
    </a-modal>
  </div>
</template>

<script setup lang="ts">
import { ref, reactive, computed, onMounted, watch } from 'vue';
import { Message } from '@arco-design/web-vue';
import { IconSearch, IconRefresh, IconInfoCircle, IconEye } from '@arco-design/web-vue/es/icon';
import { useRoute } from 'vue-router';
import { listProjects, type Project, type Dataset } from '@/api/project';
import { queryObjectAPI, searchDataAPI, type ObjectRow, type DataRow, type DataKey, type FieldValue } from '@/api/modules/data';
import { searchFields, type FieldDetailInfo } from '@/api/field';

// 路由信息
const route = useRoute();
const currentProjectId = ref<number>(Number(route.params.projectId));

// Tab相关
const type = ref('rounded');
const size = ref('medium');
const activeTab = ref<string>('');
const datasets = ref<Dataset[]>([]);

// 数据状态
const loading = ref(false);
const pageLoading = ref(false);

// 对象相关
const objectSearchKeyword = ref('');
const objectTreeRef = ref();
const currentObject = ref<ObjectRow | null>(null);
const objectList = ref<ObjectRow[]>([]);
const selectedObjectKeys = ref<string[]>([]);

// 数据相关
const dataList = ref<DataRow[]>([]);
const currentDataRow = ref<DataRow | null>(null);
const dataModalVisible = ref(false);

// 搜索表单
const searchForm = reactive({
  dateRange: [] as string[]
});

// 分页
const pagination = ref({
  showPageSize: true,
  showTotal: true,
  current: 1,
  pageSize: 10,
  total: 0
});

// 分页配置，用于表格组件
const paginationConfig = computed(() => {
  const config = {
    ...pagination.value,
    showTotal: true,
    showJumper: true,
    showPageSize: true,
    hideOnSinglePage: false, // 强制显示分页，即使只有一页
    pageSizeOptions: [10, 20, 50, 100],
    pageSizeProps: {
      style: { minWidth: '120px' }
    }
  };

  console.log('分页配置:', config);
  return config;
});

// 字段信息
const displayFields = ref<FieldDetailInfo[]>([]);

// 对象树配置
const objectFieldNames = {
  key: 'object_id',
  title: 'display_name', // 使用计算后的显示名称
  children: 'children'
};

// 计算属性
const filteredObjectTree = computed(() => {
  // 处理对象列表，确保每个对象都有显示名称
  const processedList = objectList.value.map(obj => ({
    ...obj,
    display_name: obj.object_name || obj.object_id // 如果没有object_name，使用object_id作为显示名称
  }));

  if (!objectSearchKeyword.value) return processedList;

  return processedList.filter(obj =>
    obj.object_id.toLowerCase().includes(objectSearchKeyword.value.toLowerCase()) ||
    (obj.object_name && obj.object_name.toLowerCase().includes(objectSearchKeyword.value.toLowerCase()))
  );
});

const isTimeSeriesData = computed(() => {
  const currentDataset = datasets.value.find(d => d.dataset_id.toString() === activeTab.value);
  return currentDataset?.data_type === 2; // 2表示时序数据
});

// 计算属性：实际显示的字段列表（基于返回的数据中存在的字段）
const actualDisplayFields = computed(() => {
  if (!dataList.value.length || !displayFields.value.length) return displayFields.value;

  // 获取数据中实际存在的字段
  const existingFieldKeys = new Set<string>();
  dataList.value.forEach(row => {
    if (row.fields) {
      Object.keys(row.fields).forEach(key => existingFieldKeys.add(key));
    }
  });

  // 过滤出既在配置中又在数据中存在的字段
  return displayFields.value.filter(field => existingFieldKeys.has(field.interface_name));
});

const fieldDataList = computed(() => {
  if (!currentDataRow.value?.fields) return [];

  return Object.entries(currentDataRow.value.fields).map(([fieldKey, fieldValue]) => {
    const fieldInfo = displayFields.value.find(f => f.interface_name === fieldKey);
    return {
      field_name: fieldInfo?.field_name || fieldKey,
      field_value: formatFieldValue(fieldValue),
      field_type: getFieldTypeName(fieldValue.field_type)
    };
  });
});

// 方法
const loadDatasets = async () => {
  try {
    pageLoading.value = true;
    console.log('开始加载数据集，项目ID:', currentProjectId.value);

    const projects = await listProjects();
    console.log('获取到项目列表:', projects);

    const currentProject = projects.find(p => p.id === currentProjectId.value);
    console.log('当前项目:', currentProject);

    if (currentProject && currentProject.datasets) {
      datasets.value = currentProject.datasets;
      console.log('数据集列表:', datasets.value);

      if (datasets.value.length > 0) {
        activeTab.value = datasets.value[0].dataset_id.toString();
        console.log('设置默认tab:', activeTab.value);
      }
    } else {
      console.warn('未找到项目或项目没有数据集');
    }
  } catch (error: any) {
    console.error('获取数据集列表失败:', error);
    Message.error(error.message || '获取数据集列表失败');
  } finally {
    pageLoading.value = false;
  }
};

const loadObjectList = async () => {
  const currentDatasetId = Number(activeTab.value);
  if (!currentProjectId.value || !currentDatasetId) {
    console.warn('项目ID或数据集ID为空，无法获取对象列表');
    return;
  }

  try {
    loading.value = true;
    console.log('开始获取对象列表，项目ID:', currentProjectId.value, '数据集ID:', currentDatasetId);

    // 使用QueryObject接口获取对象列表（左侧树形列表）
    const response = await queryObjectAPI({
      project_id: currentProjectId.value,
      dataset_id: currentDatasetId,
      page_info: {
        page_idx: 1,
        size: 10000
      }
    });

    console.log('对象列表API响应:', response);

    // 检查响应状态
    if (response.ret_info && response.ret_info.code !== 0) {
      throw new Error(response.ret_info.msg || '获取对象列表失败');
    }

    if (response.object_rows) {
      objectList.value = response.object_rows;
      console.log('获取到对象列表:', objectList.value);

      // 默认选择第一个对象
      if (objectList.value.length > 0 && !currentObject.value) {
        currentObject.value = objectList.value[0];
        selectedObjectKeys.value = [objectList.value[0].object_id]; // 更新选中状态
        console.log('默认选择第一个对象:', currentObject.value);
        await loadDataList();
      }
    } else {
      console.warn('响应中没有对象列表数据');
      objectList.value = [];
    }
  } catch (error: any) {
    console.error('获取对象列表失败:', error);

    // 根据错误类型提供不同的提示
    let errorMessage = '获取对象列表失败';
    if (error.message) {
      if (error.message.includes('网络')) {
        errorMessage = '网络连接失败，请检查网络连接';
      } else if (error.message.includes('权限')) {
        errorMessage = '权限不足，请联系管理员';
      } else {
        errorMessage = error.message;
      }
    }

    Message.error(errorMessage);
    objectList.value = [];
  } finally {
    loading.value = false;
  }
};

const loadDatasetFields = async (datasetId: number) => {
  try {
    // 获取全部字段列表，然后过滤出属于本数据集且metadata_flag!=1的字段
    const response = await searchFields({
      auth_info: {
        app_id: "moox_frontend",
        app_key: "2521e0d21b6be0347b72bca93904a0dd"
      },
      proj_id: currentProjectId.value,
      // 不指定dataset_id，获取项目下的全部字段
      page_info: {
        page_idx: 1,
        size: 1000  // 获取足够多的字段
      }
    });

    if (response.field_detail_infos) {
      // 过滤出属于当前数据集且metadata_flag!=1的字段（数据列表字段）
      const dataListFields = response.field_detail_infos.filter((field: FieldDetailInfo) => {
        // 检查字段是否属于当前数据集
        const belongsToDataset = field.dataset_ids && field.dataset_ids.includes(datasetId);
        // 检查是否为数据列表字段（metadata_flag != 1）
        const isDataListField = field.metadata_flag !== 1;

        return belongsToDataset && isDataListField;
      });

      displayFields.value = dataListFields;
      console.log('获取到数据列表字段信息:', displayFields.value);
      console.log('数据集ID:', datasetId, '过滤后的字段数量:', dataListFields.length);
      console.log('字段interface_name列表:', displayFields.value.map(f => f.interface_name));
      console.log('字段metadata_flag列表:', displayFields.value.map(f => ({ name: f.interface_name, metadata_flag: f.metadata_flag })));
    } else {
      console.warn('响应中没有字段信息');
      displayFields.value = [];
    }
  } catch (error: any) {
    console.error('获取字段信息失败:', error);
    displayFields.value = [];
  }
};

const loadDataList = async () => {
  if (!currentObject.value) {
    dataList.value = [];
    return;
  }

  const currentDatasetId = Number(activeTab.value);
  if (!currentProjectId.value || !currentDatasetId) {
    console.warn('项目ID或数据集ID为空，无法获取数据列表');
    return;
  }

  try {
    loading.value = true;

    // 获取当前数据集信息
    const currentDataset = datasets.value.find(d => d.dataset_id === currentDatasetId);
    if (!currentDataset) {
      console.warn('未找到当前数据集信息');
      return;
    }

    console.log('当前数据集信息:', currentDataset);
    console.log('时序周期 time_series_period:', currentDataset.time_series_period);

    const dataKey: DataKey = {
      project_id: currentProjectId.value,
      dataset_id: currentDatasetId,
      object_id: currentObject.value.object_id,
      freq: currentDataset.time_series_period || '' // 添加时序周期参数
    };

    console.log('构建DataKey:', dataKey);

    // 构建时间范围（如果有搜索条件）
    let timeRange = undefined;
    if (searchForm.dateRange && searchForm.dateRange.length === 2) {
      timeRange = {
        start: searchForm.dateRange[0],
        end: searchForm.dateRange[1]
      };
    }

    // 构建搜索选项 - includes参数可以为空，返回所有字段
    const options = {
      // 不设置includes参数，让API返回所有字段
      max_num: 1000
    };

    console.log('SearchData API 选项（不限制字段）:', options);

    console.log('SearchData API 请求参数:', {
      data_key: dataKey,
      time_range: timeRange,
      options: options,
      page_info: {
        page_idx: pagination.value.current,
        size: pagination.value.pageSize
      }
    });

    const response = await searchDataAPI({
      data_key: dataKey,
      time_range: timeRange,
      options: options,
      page_info: {
        page_idx: pagination.value.current,
        size: pagination.value.pageSize
      }
    });

    console.log('SearchData API 响应:', response);

    if (response.data_rows) {
      dataList.value = response.data_rows;
      // 确保 total 是数字类型
      const totalValue = response.total ? Number(response.total) : Math.max(dataList.value.length, 50);
      pagination.value.total = totalValue;
      console.log('获取到数据行:', dataList.value.length, '条');
      console.log('API返回的total:', response.total, '类型:', typeof response.total);
      console.log('转换后的total:', totalValue, '类型:', typeof totalValue);
      console.log('分页信息:', {
        current: pagination.value.current,
        pageSize: pagination.value.pageSize,
        total: pagination.value.total
      });

      // 调试：显示数据中包含的字段
      if (dataList.value.length > 0) {
        const firstRowFields = Object.keys(dataList.value[0].fields || {});
        console.log('数据中包含的字段:', firstRowFields);
        console.log('配置的显示字段:', displayFields.value.map(f => f.interface_name));
        console.log('实际显示的字段:', actualDisplayFields.value.map(f => f.interface_name));
      }
    } else {
      console.warn('响应中没有数据行');
      dataList.value = [];
    }
  } catch (error: any) {
    console.error('获取数据列表失败:', error);
    Message.error(error.message || '获取数据列表失败');
    dataList.value = [];
  } finally {
    loading.value = false;
  }
};

// 事件处理
const onSelectObject = async (selectedKeys: string[], event: any) => {
  console.log('选择对象事件触发，selectedKeys:', selectedKeys, 'event:', event);

  // 更新选中状态
  selectedObjectKeys.value = selectedKeys;

  if (selectedKeys.length > 0) {
    const selectedObjectId = selectedKeys[0];
    console.log('选择的对象ID:', selectedObjectId);

    const selectedObject = objectList.value.find(obj => obj.object_id === selectedObjectId);
    console.log('找到的对象:', selectedObject);

    if (selectedObject) {
      currentObject.value = selectedObject;
      pagination.value.current = 1; // 重置页码
      console.log('开始加载选中对象的数据列表');
      await loadDataList();
    } else {
      console.warn('未找到对应的对象:', selectedObjectId);
    }
  } else {
    console.log('取消选择对象');
    currentObject.value = null;
    dataList.value = [];
  }
};

const handleSearch = async () => {
  pagination.value.current = 1; // 重置页码
  await loadDataList();
};

const handleReset = () => {
  searchForm.dateRange = [];
  pagination.value.current = 1;
  loadDataList();
};

const handleView = (record: DataRow) => {
  currentDataRow.value = record;
  dataModalVisible.value = true;
};

// 分页事件处理
const onPageChange = (current: number) => {
  console.log('页码变化:', current);
  pagination.value.current = current;
  loadDataList();
};

const onPageSizeChange = (pageSize: number) => {
  console.log('页大小变化:', pageSize);
  pagination.value.pageSize = pageSize;
  pagination.value.current = 1; // 重置到第一页
  loadDataList();
};

// 工具函数
const formatFieldValue = (fieldValue: FieldValue | undefined): string => {
  if (!fieldValue) return '';

  if (fieldValue.simple_value) {
    const simpleValue = fieldValue.simple_value;
    if (simpleValue.str !== undefined) return simpleValue.str;
    if (simpleValue.int !== undefined) return simpleValue.int.toString();
    if (simpleValue.float !== undefined) return simpleValue.float.toString();
    if (simpleValue.time !== undefined) return simpleValue.time;
  }

  if (fieldValue.map_value) {
    return JSON.stringify(fieldValue.map_value);
  }

  return '';
};

const getFieldTypeName = (fieldType: number): string => {
  const typeNames: Record<number, string> = {
    1: '字符串',
    2: '整数',
    3: '浮点数',
    4: '时间',
    5: '映射'
  };
  return typeNames[fieldType] || '未知';
};

// 监听tab切换，重新加载对应数据集的数据
watch(activeTab, async (newTab, oldTab) => {
  if (oldTab === undefined) return; // 避免初始化时触发

  console.log(`切换到数据集 ${newTab}，重新加载数据`);

  // 重置状态
  currentObject.value = null;
  dataList.value = [];
  pagination.value.current = 1;
  objectList.value = [];
  selectedObjectKeys.value = [];

  // 加载当前数据集的字段信息和对象列表
  if (newTab) {
    try {
      // 并行加载字段信息和对象列表
      await Promise.all([
        loadDatasetFields(Number(newTab)),
        loadObjectList()
      ]);
    } catch (error) {
      console.error('切换数据集时加载数据失败:', error);
    }
  }
});

// 初始化
onMounted(async () => {
  console.log('数据列表页面初始化，项目ID:', currentProjectId.value);

  // 添加测试数据，确保页面能显示
  if (!currentProjectId.value || isNaN(currentProjectId.value)) {
    console.warn('项目ID无效，使用测试数据');
    datasets.value = [
      { dataset_id: 1, dataset_name: '测试数据集1', data_type: 1, time_series_period: '', validation_rule: '', remark: '' },
      { dataset_id: 2, dataset_name: '测试数据集2', data_type: 2, time_series_period: '', validation_rule: '', remark: '' }
    ];
    activeTab.value = '1';
    pageLoading.value = false;
    return;
  }

  try {
    await loadDatasets();

    // 数据集加载完成后，加载第一个数据集的字段信息和对象列表
    if (datasets.value.length > 0) {
      const firstDatasetId = datasets.value[0].dataset_id;
      console.log('加载第一个数据集:', firstDatasetId);

      // 并行加载字段信息和对象列表
      await Promise.all([
        loadDatasetFields(firstDatasetId),
        loadObjectList()
      ]);
    } else {
      console.warn('没有找到数据集');
    }
  } catch (error) {
    console.error('页面初始化失败:', error);
    Message.error('页面初始化失败，请刷新重试');
    // 出错时也显示测试数据
    datasets.value = [
      { dataset_id: 1, dataset_name: '测试数据集1', data_type: 1, time_series_period: '', validation_rule: '', remark: '' }
    ];
    activeTab.value = '1';
    pageLoading.value = false;
  }
});
</script>

<style lang="scss" scoped>
.container {
  padding: 0;
}

.loading-container {
  display: flex;
  justify-content: center;
  align-items: center;
  height: 400px;
}

.empty-state {
  display: flex;
  justify-content: center;
  align-items: center;
  height: 300px;
}

.data-list-content {
  display: flex;
  column-gap: 16px;
  height: 800px;

  .left-box {
    display: flex;
    flex-direction: column;
    width: 160px;
    height: 100%;

    .tree-box {
      flex: 1;
      margin-top: 16px;
      overflow: auto;
      border: 1px solid var(--color-border-2);
      border-radius: 4px;
      padding: 8px;

      .tree-loading,
      .tree-empty {
        display: flex;
        justify-content: center;
        align-items: center;
        height: 200px;
        flex-direction: column;
      }
    }
  }

  .right-box {
    flex: 1;
    display: flex;
    flex-direction: column;
    overflow: hidden;

    .table-box {
      flex: 1;
      margin-top: 1px;
      overflow: auto;
      border: 1px solid var(--color-border-2);
      border-radius: 4px;
    }
  }
}

// Tab样式自定义 - 使用更高权重的选择器
:deep(.arco-tabs.arco-tabs-type-rounded) {
  .arco-tabs-tab.arco-tabs-tab-active {
    background-color: #e8f5e8 !important; // 浅绿色背景
    border-color: #52c41a !important; // 绿色边框

    .arco-tabs-tab-title {
      color: #389e0d !important; // 深绿色文字
      font-weight: 600 !important;
    }
  }

  .arco-tabs-tab:hover:not(.arco-tabs-tab-active) {
    background-color: #f6ffed !important; // 悬停时的浅绿色
  }
}

// 备用样式 - 如果上面的不生效，使用这个
:deep(.arco-tabs) {
  .arco-tabs-tab {
    &.arco-tabs-tab-active {
      background-color: #e8f5e8 !important;
      border-color: #52c41a !important;

      .arco-tabs-tab-title {
        color: #389e0d !important;
        font-weight: 600 !important;
      }
    }

    &:hover:not(.arco-tabs-tab-active) {
      background-color: #f6ffed !important;
    }
  }
}

:deep(.arco-tree-node-title) {
  display: flex;
  align-items: center;
  gap: 4px;
}

// 树节点选中状态样式
:deep(.arco-tree-node-selected) {
  .arco-tree-node-title {
    color: #1890ff !important; // 蓝色文字
    font-weight: 600 !important; // 加粗字体
  }
}

// 调整 arco-card-body 的高度
:deep(.arco-card-body) {
  min-height: 800px !important;
  padding: 12px 24px 24px 24px !important; // 减少顶部内边距
}

// 减少Tab导航与卡片顶部的间距
:deep(.arco-tabs-nav) {
  margin-top: -2px !important; // 向上移动Tab导航，保留更多空间避免截断
}

// 紧凑型提示组件样式
.compact-alert {
  margin: 0 !important;
  flex-shrink: 0 !important;
  max-width: 220px !important;
  height: 32px !important; // 与按钮高度一致
  min-height: 32px !important;
  display: flex !important;
  align-items: center !important;
  padding: 0 !important; // 移除外层padding

  :deep(.arco-alert-body) {
    padding: 0 8px !important; // 只保留左右内边距
    line-height: 32px !important; // 行高与组件高度一致
    display: flex !important;
    align-items: center !important;
    height: 100% !important;
  }

  :deep(.arco-alert-icon) {
    margin-right: 6px !important; // 减少图标间距
    padding: 0 4px !important; // 给图标添加少量padding
    display: flex !important;
    align-items: center !important;
    height: 100% !important;
  }

  :deep(.arco-alert-content) {
    font-size: 12px !important; // 减小字体
    white-space: nowrap !important; // 不换行
    overflow: hidden !important; // 隐藏溢出
    text-overflow: ellipsis !important; // 显示省略号
    line-height: 32px !important; // 与组件高度一致
    height: 32px !important;
    display: flex !important;
    align-items: center !important;
  }
}


</style>

