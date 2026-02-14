<template>
  <div class="moox-page">
    <div class="container">
      <!-- Tab切换区域 -->
      <a-card :bordered="false">
        <a-tabs :type="type" :size="size" v-model:active-key="activeTab">
          <a-tab-pane
            v-for="dataset in datasets"
            :key="dataset.dataset_id.toString()"
            :title="dataset.dataset_name"
          >
            <!-- 搜索表单 -->
            <div class="moox-inner">
              <a-form auto-label-width :model="formData.form">
                <a-row :gutter="16">
                  <!-- 动态生成字段搜索框 -->
                  <template v-for="fieldKey in searchFieldKeys" :key="fieldKey">
                    <!-- 时间类型字段使用时间范围选择器 -->
                    <a-col
                      v-if="isTimeField(fieldKey)"
                      :xs="24" :sm="24" :md="12" :lg="12" :xl="8" :xxl="8"
                    >
                      <a-form-item :field="fieldKey" hide-label>
                        <a-range-picker
                          v-model="timeRanges[fieldKey]"
                          show-time
                          format="YYYY-MM-DD HH:mm:ss"
                          :placeholder="['开始时间', '结束时间']"
                          allow-clear
                          style="width: 100%"
                        />
                      </a-form-item>
                    </a-col>
                    <!-- 其他字段使用普通输入框 -->
                    <a-col
                      v-else
                      :xs="24" :sm="24" :md="12" :lg="12" :xl="6" :xxl="6"
                    >
                      <a-form-item :field="fieldKey" hide-label>
                        <a-input
                          v-model="formData.form[fieldKey]"
                          :placeholder="`请输入${getFieldDisplayName(fieldKey)}`"
                          allow-clear
                        />
                      </a-form-item>
                    </a-col>
                  </template>

                  <!-- 操作按钮 -->
                  <a-col :xs="24" :sm="24" :md="12" :lg="12" :xl="6" :xxl="6">
                    <a-space class="search-btn">
                      <a-button type="primary" @click="search">
                        <template #icon>
                          <icon-search />
                        </template>
                        <template #default>查询</template>
                      </a-button>
                      <a-button @click="reset">
                        <template #icon>
                          <icon-refresh />
                        </template>
                        <template #default>重置</template>
                      </a-button>
                      <a-button type="text" @click="formData.search = !formData.search">
                        <template #icon>
                          <icon-up v-if="formData.search" />
                          <icon-down v-else />
                        </template>
                        <template #default>{{ formData.search ? "收起" : "展开" }}</template>
                      </a-button>
                    </a-space>
                  </a-col>
                </a-row>
              </a-form>

              <!-- 操作按钮区域 -->
              <div class="action-buttons">
                <a-space>
                  <a-button type="primary" @click="showCreateModal">
                    <template #icon>
                      <icon-plus />
                    </template>
                    新增
                  </a-button>
                  <a-button
                    type="primary"
                    status="danger"
                    :disabled="selectedKeys.length === 0"
                    @click="batchDelete"
                  >
                    <template #icon>
                      <icon-delete />
                    </template>
                    删除 ({{ selectedKeys.length }})
                  </a-button>
                </a-space>
              </div>

              <!-- 数据表格 -->
              <a-table
                row-key="object_id"
                size="small"
                :bordered="{ cell: true }"
                :scroll="{ x: 'max-content', y: '100%' }"
                :loading="loading"
                :columns="columns"
                :data="data"
                :row-selection="rowSelection"
                v-model:selectedKeys="selectedKeys"
                :pagination="pagination"
                @page-change="pageChange"
                @page-size-change="pageSizeChange"
              >
                <!-- 动态字段插槽 -->
                <template v-for="fieldKey in getAllFieldKeys()" :key="`slot_${fieldKey}`" #[`field_${fieldKey}`]="{ record }">
                  {{ getFieldDisplayValue(record, fieldKey) }}
                </template>

                <template #optional="{ record }">
                  <a-space>
                    <a-button size="mini" type="primary" @click="viewDetail(record)">详情</a-button>
                    <a-button size="mini" @click="editObject(record)">修改</a-button>
                    <a-popconfirm content="确定删除这个对象吗?" type="warning" @ok="deleteObject(record)">
                      <a-button size="mini" type="primary" status="danger">删除</a-button>
                    </a-popconfirm>
                  </a-space>
                </template>
              </a-table>
            </div>
          </a-tab-pane>
        </a-tabs>
      </a-card>
    </div>
  </div>

  <!-- 新增对象模态框 -->
  <a-modal
    v-model:visible="createModalVisible"
    title="新增对象"
    @ok="saveObject"
    @cancel="createModalVisible = false"
    :ok-loading="loading"
  >
    <a-form :model="objectForm" layout="vertical">
      <a-form-item label="对象ID" required>
        <a-input v-model="objectForm.object_id" placeholder="请输入对象ID" />
      </a-form-item>

      <a-form-item
        v-for="fieldKey in searchFieldKeys"
        :key="fieldKey"
        :label="getFieldDisplayName(fieldKey)"
      >
        <a-input
          v-model="objectForm[fieldKey]"
          :placeholder="getFieldPlaceholder(fieldKey)"
        />
      </a-form-item>
    </a-form>
  </a-modal>

  <!-- 编辑对象模态框 -->
  <a-modal
    v-model:visible="editModalVisible"
    title="编辑对象"
    @ok="saveObject"
    @cancel="editModalVisible = false"
    :ok-loading="loading"
  >
    <a-form :model="objectForm" layout="vertical">
      <a-form-item label="对象ID">
        <a-input v-model="objectForm.object_id" disabled />
      </a-form-item>

      <a-form-item
        v-for="fieldKey in searchFieldKeys"
        :key="fieldKey"
        :label="getFieldDisplayName(fieldKey)"
      >
        <a-input
          v-model="objectForm[fieldKey]"
          :placeholder="getFieldPlaceholder(fieldKey)"
        />
      </a-form-item>
    </a-form>
  </a-modal>

  <!-- 查看详情模态框 -->
  <a-modal
    v-model:visible="detailModalVisible"
    title="对象详情"
    :footer="false"
    width="800px"
  >
    <div v-if="currentObject" class="object-detail">
      <a-descriptions :column="2" bordered>
        <a-descriptions-item label="对象ID">
          {{ currentObject.object_id }}
        </a-descriptions-item>

        <a-descriptions-item
          v-for="(fieldValue, fieldKey) in currentObject.fields"
          :key="fieldKey"
          :label="getFieldDisplayName(String(fieldKey))"
        >
          {{ formatFieldValue(fieldValue) }}
        </a-descriptions-item>
      </a-descriptions>
    </div>
  </a-modal>
</template>

<script setup lang="ts">
import { ref, reactive, computed, onMounted, watch } from 'vue';
import { useRoute } from 'vue-router';
import { Message } from '@arco-design/web-vue';
import { IconSearch, IconRefresh, IconUp, IconDown, IconPlus, IconDelete } from '@arco-design/web-vue/es/icon';
import { queryObjectAPI, upsertObjectAPI, deleteObjectAPI, OperatorConst, LogicalConst, type ObjectRow, type FieldValue, type UpdateObjectRow, type UpdateField, type QueryCond, type Options } from "@/api/modules/data/index";
import { listProjects, type Dataset } from '@/api/project';
import { searchFields, type SearchFieldReq, type FieldDetailInfo } from '@/api/field';
import { FormData, RowSelection, Pagination } from "./config";

// 路由信息
const route = useRoute();

// 获取当前项目ID
const currentProjectId = computed(() => {
  const projectId = route.params.projectId;
  return projectId ? Number(projectId) : null;
});

// Tab配置
const type = ref("rounded");
const size = ref("medium");
const activeTab = ref("");

// 数据状态
const loading = ref(false);
const datasets = ref<Dataset[]>([]);
const data = ref<ObjectRow[]>([]);
const fieldMapping = ref<Record<string, string>>({});
const datasetFields = ref<Record<string, FieldDetailInfo[]>>({});  // 存储每个数据集的字段信息

// 表单数据
const formData = reactive<FormData>({
  form: {},
  search: false
});

// 时间范围选择器的值（按字段名存储）
const timeRanges = reactive<Record<string, string[] | undefined>>({});

// 字段一级格式常量
const FieldPrimaryFormat = {
  TIME: 4  // 时间类型
} as const;

const selectedKeys = ref<string[]>([]);
const rowSelection = reactive<RowSelection>({
  type: "checkbox",
  showCheckedAll: true,
  onlyCurrent: false
});

// 模态框状态
const createModalVisible = ref(false);
const editModalVisible = ref(false);
const detailModalVisible = ref(false);

// 当前操作的对象
const currentObject = ref<ObjectRow | null>(null);

// 表单数据
const objectForm = ref<Record<string, any>>({});

const pagination = ref<Pagination>({
  showPageSize: true,
  showTotal: true,
  current: 1,
  pageSize: 25,
  total: 0,
  pageSizeOptions: [25, 50, 100]
});



const pageChange = (page: number) => {
  pagination.value.current = page;
  getObjectList();
};

const pageSizeChange = (pageSize: number) => {
  pagination.value.pageSize = pageSize;
  pagination.value.current = 1;
  getObjectList();
};

// 动态生成表格列定义
const columns = computed(() => {
  const baseColumns = [
    {
      title: "对象ID",
      dataIndex: "object_id",
      width: 200,
      fixed: "left"
    }
  ];

  // 基于metadata字段动态添加字段列
  const fieldColumns: any[] = [];
  const metadataFields = getCurrentDatasetFields();

  // 按字段ID排序，确保显示顺序一致
  const sortedFields = metadataFields.sort((a, b) => a.field_id - b.field_id);

  // 为每个metadata字段创建一列
  sortedFields.forEach(field => {
    fieldColumns.push({
      title: field.field_name,  // 使用中文名作为列标题
      slotName: `field_${field.interface_name}`,
      width: 150,
      ellipsis: true,
      tooltip: true
    });
  });

  const operationColumn = {
    title: "操作",
    slotName: "optional",
    align: "center",
    fixed: "right",
    width: 200
  };

  const allColumns = [...baseColumns, ...fieldColumns, operationColumn];
  console.log('生成的表格列:', allColumns);
  return allColumns;
});

// 认证信息
const getAuthInfo = () => ({
  app_id: 'moox_frontend',
  app_key: '2521e0d21b6be0347b72bca93904a0dd'
});

// 加载项目数据集
const loadDatasets = async () => {
  if (!currentProjectId.value) {
    console.warn('当前项目ID为空，无法获取数据集列表');
    return;
  }

  try {
    const projects = await listProjects();
    const currentProject = projects.find(p => p.id === currentProjectId.value);

    if (currentProject && currentProject.datasets) {
      datasets.value = currentProject.datasets;

      // 设置默认激活的tab
      if (datasets.value.length > 0 && !activeTab.value) {
        activeTab.value = datasets.value[0].dataset_id.toString();
      }

      console.log('数据集列表加载成功:', datasets.value);
    } else {
      datasets.value = [];
      console.warn('当前项目无数据集或项目不存在');
    }
  } catch (error: any) {
    console.error('获取数据集列表失败:', error);
    Message.error(error.message || '获取数据集列表失败');
    datasets.value = [];
  }
};

// 加载数据集字段信息（获取关联本数据集且标记了table_type=1的字段）
const loadDatasetFields = async (datasetId: number) => {
  if (!currentProjectId.value) return;

  try {
    const searchParams: SearchFieldReq = {
      auth_info: getAuthInfo(),
      proj_id: currentProjectId.value,
      dataset_id: datasetId,  // 指定数据集ID
      page_info: {
        page_idx: 1,
        size: 1000  // 一次拉取所有字段
      }
    };

    const response = await searchFields(searchParams);

    // 过滤table_type=1的字段（数据对象表字段）
    const metadataFields = response.field_detail_infos.filter((field: FieldDetailInfo) =>
      field.table_type === 1
    );

    // 存储该数据集的字段信息
    datasetFields.value[datasetId.toString()] = metadataFields;

    // 构建字段映射：英文名 -> 中文名（仅包含metadata字段）
    const mapping: Record<string, string> = {};
    metadataFields.forEach((field: FieldDetailInfo) => {
      mapping[field.interface_name] = field.field_name;
    });

    fieldMapping.value = mapping;
    console.log(`数据集${datasetId}的metadata字段加载成功:`, metadataFields.length, '个字段');

    // 重新初始化搜索表单
    initSearchForm();
  } catch (error: any) {
    console.error('获取数据集字段失败:', error);
    Message.error(error.message || '获取数据集字段失败');
  }
};

// 获取字段显示名称（中文名优先，否则显示英文名）
const getFieldDisplayName = (fieldKey: string): string => {
  return fieldMapping.value[fieldKey] || fieldKey;
};

// 格式化时间为 YYYY-MM-DD HH:mm:ss 格式
// const formatDateTime = (date: Date = new Date()): string => {
//   return date.getFullYear() + '-' +
//     String(date.getMonth() + 1).padStart(2, '0') + '-' +
//     String(date.getDate()).padStart(2, '0') + ' ' +
//     String(date.getHours()).padStart(2, '0') + ':' +
//     String(date.getMinutes()).padStart(2, '0') + ':' +
//     String(date.getSeconds()).padStart(2, '0');
// };

// 获取字段输入框的占位符文本
const getFieldPlaceholder = (fieldKey: string): string => {
  const fieldName = getFieldDisplayName(fieldKey);

  // 为特定字段提供格式提示
  if (fieldKey === 'unshelve_time') {
    return `请输入${fieldName}，格式：YYYY-MM-DD HH:mm:ss，如：2099-01-01 00:00:00`;
  }

  // 其他字段使用默认占位符
  return `请输入${fieldName}`;
};

// 获取当前数据集的metadata字段列表
const getCurrentDatasetFields = (): FieldDetailInfo[] => {
  const currentDatasetId = activeTab.value;
  if (!currentDatasetId || !datasetFields.value[currentDatasetId]) {
    return [];
  }
  return datasetFields.value[currentDatasetId];
};

// 判断字段是否为时间类型
const isTimeField = (fieldKey: string): boolean => {
  const fields = getCurrentDatasetFields();
  const field = fields.find(f => f.interface_name === fieldKey);
  if (!field || !field.field_format_type) {
    return false;
  }
  return field.field_format_type.field_primary_format === FieldPrimaryFormat.TIME;
};

// 获取当前数据集的搜索字段列表（计算属性）
const searchFieldKeys = computed(() => {
  const fields = getCurrentDatasetFields();
  return fields.map(field => field.interface_name);
});

// 获取所有字段键（用于动态插槽）- 基于metadata字段而不是对象数据
const getAllFieldKeys = (): string[] => {
  return searchFieldKeys.value;
};

// 初始化搜索表单字段
const initSearchForm = () => {
  const newForm: { [key: string]: string } = {};

  // 为每个字段添加搜索框
  searchFieldKeys.value.forEach(fieldKey => {
    newForm[fieldKey] = formData.form[fieldKey] || "";
  });

  formData.form = newForm;
};

// 获取字段显示值
const getFieldDisplayValue = (record: ObjectRow, fieldKey: string): string => {
  const fieldValue = record.fields?.[fieldKey];
  if (!fieldValue) {
    return '-';
  }
  const result = formatFieldValue(fieldValue);
  return result || '-';
};



// 格式化字段值显示
const formatFieldValue = (fieldValue: FieldValue): string => {
  if (!fieldValue || !fieldValue.simple_value) {
    return '';
  }

  const simpleValue = fieldValue.simple_value;

  // 根据实际API返回结构调整字段访问方式
  // 优先使用protobuf定义的字段名
  if (simpleValue.str !== undefined) return simpleValue.str;
  if (simpleValue.int !== undefined) return simpleValue.int.toString();
  if (simpleValue.float !== undefined) return simpleValue.float.toString();
  if (simpleValue.time !== undefined) return simpleValue.time;

  // 兼容性字段
  if (simpleValue.string_value !== undefined) return simpleValue.string_value;
  if (simpleValue.int_value !== undefined) return simpleValue.int_value.toString();
  if (simpleValue.double_value !== undefined) return simpleValue.double_value.toString();
  if (simpleValue.bool_value !== undefined) return simpleValue.bool_value.toString();

  return '';
};


// 格式化时间为 YYYY-MM-DD HH:mm:ss
const formatDateTime = (date: Date): string => {
  return date.getFullYear() + '-' +
    String(date.getMonth() + 1).padStart(2, '0') + '-' +
    String(date.getDate()).padStart(2, '0') + ' ' +
    String(date.getHours()).padStart(2, '0') + ':' +
    String(date.getMinutes()).padStart(2, '0') + ':' +
    String(date.getSeconds()).padStart(2, '0');
};

// 构建搜索条件
const buildSearchOptions = (): Options | undefined => {
  const conds: QueryCond[] = [];

  searchFieldKeys.value.forEach(fieldKey => {
    // 时间类型字段使用时间范围搜索
    if (isTimeField(fieldKey)) {
      const range = timeRanges[fieldKey];
      if (range && range.length === 2) {
        const [startTime, endTime] = range;
        if (startTime) {
          const startStr = typeof startTime === 'string' ? startTime : formatDateTime(new Date(startTime));
          conds.push({
            field_key: fieldKey,
            op: OperatorConst.gte,
            value: { str: startStr }
          });
        }
        if (endTime) {
          const endStr = typeof endTime === 'string' ? endTime : formatDateTime(new Date(endTime));
          conds.push({
            field_key: fieldKey,
            op: OperatorConst.lte,
            value: { str: endStr }
          });
        }
      }
    } else {
      // 其他字段使用 like 模糊匹配
      const searchValue = formData.form[fieldKey];
      if (searchValue && searchValue.trim()) {
        conds.push({
          field_key: fieldKey,
          op: OperatorConst.like,
          value: { str: `*${searchValue.trim()}*` }
        });
      }
    }
  });

  // 如果有搜索条件，返回 options
  if (conds.length > 0) {
    return {
      cond_groups: [{
        conds: conds,
        logical: LogicalConst.and  // 多个条件之间用 AND 连接
      }]
    };
  }

  return undefined;
};

// 获取对象列表（使用后端分页和搜索）
const getObjectList = async () => {
  const currentDatasetId = Number(activeTab.value);
  if (!currentProjectId.value || !currentDatasetId) {
    console.warn('项目ID或数据集ID为空，无法获取对象列表');
    return;
  }

  try {
    loading.value = true;

    // 构建搜索条件
    const options = buildSearchOptions();

    const response = await queryObjectAPI({
      project_id: currentProjectId.value,
      dataset_id: currentDatasetId,
      options: options,
      page_info: {
        page_idx: pagination.value.current,
        size: pagination.value.pageSize
      }
    });

    // 添加响应数据的安全检查
    if (!response) {
      throw new Error('获取对象列表失败：响应数据为空');
    }

    if (!response.ret_info) {
      throw new Error('获取对象列表失败：响应格式错误，缺少ret_info字段');
    }

    if (response.ret_info.code === 0) {
      // 直接使用后端返回的数据
      data.value = response.object_rows || [];
      // 使用后端返回的 total 字段
      pagination.value.total = typeof response.total === 'string' ? parseInt(response.total, 10) : (response.total || 0);

      console.log('对象列表加载成功:', data.value.length, '条数据，总数:', pagination.value.total);

      // 初始化搜索表单（基于metadata字段）
      initSearchForm();
    } else {
      throw new Error(response.ret_info.msg || '获取对象列表失败');
    }
  } catch (error: any) {
    console.error('获取对象列表失败:', error);
    Message.error(error.message || '获取对象列表失败');
    data.value = [];
    pagination.value.total = 0;
  } finally {
    loading.value = false;
  }
};

// 表单操作
// 注意：不再使用formRef.value.resetFields()，改为手动重置表单数据

// 搜索函数（目前后端不支持搜索参数，仅重新加载数据）
const search = () => {
  pagination.value.current = 1; // 重置到第一页
  getObjectList();
};

// 重置函数
const reset = () => {
  // 手动重置表单数据
  const newForm: { [key: string]: string } = {};
  searchFieldKeys.value.forEach(fieldKey => {
    newForm[fieldKey] = "";
  });
  formData.form = newForm;

  // 重置所有时间范围
  Object.keys(timeRanges).forEach(key => {
    timeRanges[key] = undefined;
  });

  // 重置后重新搜索
  search();
};

// 兼容旧的重置函数名
// const onReset = reset;

// 操作方法
const viewDetail = (record: ObjectRow) => {
  currentObject.value = record;
  detailModalVisible.value = true;
};

const editObject = (record: ObjectRow) => {
  currentObject.value = record;
  // 初始化表单数据
  objectForm.value = { object_id: record.object_id };

  // 填充现有字段值
  Object.keys(record.fields || {}).forEach(fieldKey => {
    const fieldValue = record.fields[fieldKey];
    if (fieldValue && fieldValue.simple_value) {
      objectForm.value[fieldKey] = formatFieldValue(fieldValue);
    }
  });

  editModalVisible.value = true;
};

const deleteObject = async (record: ObjectRow) => {
  try {
    loading.value = true;

    // 调用DeleteObject接口进行真正的删除
    const response = await deleteObjectAPI({
      project_id: currentProjectId.value!,
      dataset_id: Number(activeTab.value),
      object_ids: [record.object_id]
    });

    if (response.ret_info.code === 0) {
      Message.success('删除成功');
      // 重新加载数据
      getObjectList();
    } else {
      throw new Error(response.ret_info.msg || '删除失败，请联系moox backend service管理员');
    }
  } catch (error: any) {
    console.error('删除对象失败:', error);
    Message.error(error.message || '删除对象失败，请联系moox backend service管理员');
  } finally {
    loading.value = false;
  }
};

// 显示新增模态框
const showCreateModal = () => {
  currentObject.value = null;
  objectForm.value = { object_id: '' };

  // 为所有字段初始化空值
  searchFieldKeys.value.forEach(fieldKey => {
    objectForm.value[fieldKey] = '';
  });

  createModalVisible.value = true;
};

// 批量删除
const batchDelete = async () => {
  if (selectedKeys.value.length === 0) {
    Message.warning('请选择要删除的对象');
    return;
  }

  try {
    loading.value = true;

    // 调用DeleteObject接口进行批量删除
    const response = await deleteObjectAPI({
      project_id: currentProjectId.value!,
      dataset_id: Number(activeTab.value),
      object_ids: selectedKeys.value
    });

    if (response.ret_info.code === 0) {
      Message.success(`成功删除 ${selectedKeys.value.length} 个对象`);
      selectedKeys.value = []; // 清空选择
      // 重新加载数据
      getObjectList();
    } else {
      throw new Error(response.ret_info.msg || '批量删除失败，请联系moox backend service管理员');
    }
  } catch (error: any) {
    console.error('批量删除失败:', error);
    Message.error(error.message || '批量删除失败，请联系moox backend service管理员');
  } finally {
    loading.value = false;
  }
};

// 保存对象（新增或编辑）
const saveObject = async () => {
  try {
    if (!objectForm.value.object_id) {
      Message.error('请输入对象ID');
      return;
    }

    loading.value = true;

    // 构建字段更新数据
    const fields: Record<string, UpdateField> = {};

    // 处理所有字段
    Object.keys(objectForm.value).forEach(fieldKey => {
      if (fieldKey === 'object_id') return; // 跳过object_id

      const value = objectForm.value[fieldKey];
      if (value !== undefined && value !== '') {
        fields[fieldKey] = {
          field_key: fieldKey,
                      field_category: 1, // STRING类型
          update_type: 1, // SET_UPDATE
          simple_value: {
            str: String(value)
          }
        };
      }
    });

    // 移除自动设置unshelve_time的逻辑，让用户自己输入

    const updateRows: UpdateObjectRow[] = [{
      object_id: objectForm.value.object_id,
      fields: fields
    }];

    const response = await upsertObjectAPI({
      project_id: currentProjectId.value!,
      dataset_id: Number(activeTab.value),
      object_rows: updateRows
    });

    if (response.ret_info.code === 0) {
      const action = currentObject.value ? '更新' : '创建';
      Message.success(`${action}成功`);

      // 关闭模态框
      createModalVisible.value = false;
      editModalVisible.value = false;

      // 重新加载数据
      getObjectList();
    } else {
      throw new Error(response.ret_info.msg || '保存失败');
    }
  } catch (error: unknown) {
    console.error('保存对象失败:', error);
    const errorMessage = error instanceof Error ? error.message : '保存对象失败';
    Message.error(errorMessage);
  } finally {
    loading.value = false;
  }
};

// 监听tab切换，重新加载对应数据集的数据
watch(activeTab, async (newTab, oldTab) => {
  if (oldTab === undefined) return; // 避免初始化时触发

  console.log(`切换到数据集 ${newTab}，重新加载数据`);

  // 加载当前数据集的字段信息
  if (newTab) {
    await loadDatasetFields(Number(newTab));
  }

  pagination.value.current = 1; // 重置页码

  // 重置搜索表单并重新获取数据
  reset();
});

// 初始化
onMounted(async () => {
  await loadDatasets();

  // 数据集加载完成后，加载第一个数据集的字段信息和数据
  if (datasets.value.length > 0) {
    const firstDatasetId = datasets.value[0].dataset_id;
    await loadDatasetFields(firstDatasetId);
    getObjectList();
  }
});
</script>

<style lang="scss" scoped>
.search-btn {
  margin-bottom: 1px;
}

// 操作按钮区域样式
.action-buttons {
  margin: 8px 0 6px 0;
  padding: 0 0 6px 0;
  border-bottom: 1px solid #f0f0f0;
}

// 调整 arco-card-body 的内边距
:deep(.arco-card-body) {
  padding: 12px 12px 24px 12px !important;
}

// Tab导航左侧留出间距
:deep(.arco-tabs-nav) {
  margin-top: -2px !important;
  padding-left: 12px !important;
}

// Tab内容区减少顶部间距
:deep(.arco-tabs-content) {
  padding-top: 0 !important;
}

:deep(.arco-tabs-content-list) {
  padding-top: 0 !important;
}

:deep(.arco-tabs-pane) {
  padding-top: 0 !important;
}

// 表单项间距紧凑
:deep(.arco-form-item) {
  margin-bottom: 2px !important;
}

// Tab样式自定义
:deep(.arco-tabs.arco-tabs-type-rounded) {
  .arco-tabs-tab.arco-tabs-tab-active {
    background-color: #e8f5e8 !important;
    border-color: #52c41a !important;

    .arco-tabs-tab-title {
      color: #389e0d !important;
      font-weight: 600 !important;
    }
  }

  .arco-tabs-tab:hover:not(.arco-tabs-tab-active) {
    background-color: #f6ffed !important;
  }
}

// 表格行间距紧凑
:deep(.arco-table) {
  .arco-table-th {
    background-color: #f7f8fa;
    font-weight: 600;
    white-space: nowrap;
    padding: 6px 8px !important;
  }

  .arco-table-td {
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
    max-width: 150px;
    padding: 4px 8px !important;
  }

  // 对象ID列样式
  .arco-table-td:first-child {
    font-weight: 500;
    color: #1d2129;
  }
}

// 对象详情样式
.object-detail {
  .arco-descriptions-item-label {
    font-weight: 600;
    color: #1d2129;
  }

  .arco-descriptions-item-value {
    word-break: break-all;
  }
}
</style>
