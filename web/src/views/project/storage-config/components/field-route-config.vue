<template>
  <div class="moox-inner">
    <!-- 搜索区域 -->
    <a-space wrap>
      <a-select placeholder="请选择数据集" v-model="form.dataset_id" style="width: 150px" allow-clear>
        <a-option value="0">全部</a-option>
        <a-option
          v-for="dataset in datasetOptions"
          :key="dataset.dataset_id"
          :value="dataset.dataset_id"
        >
          {{ dataset.dataset_name }}
        </a-option>
      </a-select>
      <a-select
        v-model="form.field_id"
        placeholder="请选择字段"
        allow-clear
        allow-search
        :filter-option="false"
        @search="handleFieldSearch"
        style="width: 200px"
        :loading="fieldLoading"
      >
        <a-option value="999999999">全部</a-option>
        <a-option
          v-for="field in filteredFieldOptions"
          :key="field.field_id"
          :value="field.field_id"
        >
          {{ field.field_name }} ({{ field.field_id }})
        </a-option>
      </a-select>
      <a-select v-model="form.device_id" placeholder="请选择存储设备" allow-clear style="width: 200px" :loading="deviceLoading">
        <a-option
          v-for="device in deviceOptions"
          :key="device.device_id"
          :value="device.device_id"
        >
          {{ device.device_name }} ({{ device.device_id }})
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
    </a-space>

    <a-row>
      <a-space wrap>
        <a-button type="primary" @click="onAdd">
          <template #icon><icon-plus /></template>
          <span>新增</span>
        </a-button>
        <a-button type="primary" status="danger" @click="batchDelete">
          <template #icon><icon-delete /></template>
          <span>删除</span>
        </a-button>
      </a-space>
    </a-row>

    <a-table
      row-key="route_id"
      :data="tableData"
      :bordered="{ cell: true }"
      :loading="loading"
      :scroll="{ x: '100%', y: '100%', minWidth: 800 }"
      :pagination="pagination"
      :row-selection="{ type: 'checkbox', showCheckedAll: true }"
      :selected-keys="selectedKeys"
      @select="select"
      @select-all="selectAll"
      @page-change="onPageChange"
    >
      <template #columns>
        <a-table-column title="序号" :width="64">
          <template #cell="cell">{{ cell.rowIndex + 1 }}</template>
        </a-table-column>
        <a-table-column title="数据集" data-index="dataset_id">
          <template #cell="{ record }">
            {{ getDatasetName(record.dataset_id) }}
          </template>
        </a-table-column>
        <a-table-column title="字段ID" data-index="field_id">
          <template #cell="{ record }">
            {{ getFieldName(record.field_id) }}
          </template>
        </a-table-column>
        <a-table-column title="存储设备" data-index="device_id">
          <template #cell="{ record }">
            <a-tag :color="getDeviceColor(record.device_id)">
              {{ getDeviceName(record.device_id) }}
            </a-tag>
          </template>
        </a-table-column>
        <a-table-column title="状态" :width="100" align="center">
          <template #cell="{ record }">
            <a-tag bordered size="small" color="arcoblue" v-if="record.enabled === 'true'">启用</a-tag>
            <a-tag bordered size="small" color="red" v-else>禁用</a-tag>
          </template>
        </a-table-column>
        <a-table-column title="创建时间" data-index="ctime" :width="180"></a-table-column>
        <a-table-column title="操作" :width="200" align="center" :fixed="'right'">
          <template #cell="{ record }">
            <a-space>
              <a-button type="primary" size="mini" @click="onUpdate(record)">
                <template #icon><icon-edit /></template>
                <span>修改</span>
              </a-button>
              <a-popconfirm type="warning" content="确定删除该配置吗?" @ok="onDelete(record)" v-if="record.enabled === 'true'">
                <a-button type="primary" status="danger" size="mini">
                  <template #icon><icon-delete /></template>
                  <span>删除</span>
                </a-button>
              </a-popconfirm>
            </a-space>
          </template>
        </a-table-column>
      </template>
    </a-table>

    <!-- 新增/编辑弹窗 -->
    <a-modal v-model:visible="modalVisible" @close="afterClose" @ok="handleOk" @cancel="afterClose" width="600px">
      <template #title>{{ modalTitle }}</template>
      <div>
        <a-form ref="formRef" auto-label-width :rules="rules" :model="formData">
          <a-form-item field="dataset_id" label="数据集" validate-trigger="blur">
            <a-select v-model="formData.dataset_id" placeholder="请选择数据集">
              <a-option
                v-for="dataset in datasetOptions"
                :key="dataset.dataset_id"
                :value="dataset.dataset_id"
              >
                {{ dataset.dataset_name }}
              </a-option>
            </a-select>
          </a-form-item>
          <a-form-item field="field_id" label="字段ID" validate-trigger="blur">
            <a-select
              v-model="formData.field_id"
              placeholder="请选择字段"
              allow-clear
              allow-search
              :filter-option="false"
              @search="handleModalFieldSearch"
              :loading="fieldLoading"
            >
              <a-option :value="999999999">全部</a-option>
              <a-option
                v-for="field in filteredModalFieldOptions"
                :key="field.field_id"
                :value="field.field_id"
              >
                {{ field.field_name }} ({{ field.field_id }})
              </a-option>
            </a-select>
          </a-form-item>
          <a-form-item field="device_id" label="存储设备" validate-trigger="blur">
            <a-select v-model="formData.device_id" placeholder="请选择存储设备" allow-clear :loading="deviceLoading">
              <a-option
                v-for="device in deviceOptions"
                :key="device.device_id"
                :value="device.device_id"
              >
                {{ device.device_name }} ({{ device.device_id }})
              </a-option>
            </a-select>
          </a-form-item>
        </a-form>
      </div>
    </a-modal>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, watch, onMounted } from 'vue';
import { useRoute } from 'vue-router';
import { Message } from '@arco-design/web-vue';
import { IconSearch, IconRefresh, IconPlus, IconEdit, IconDelete } from '@arco-design/web-vue/es/icon';
import type { FieldRoute, StorageDevice } from '@/api/storage-config';
import {
  createFieldRoute,
  updateFieldRoute,
  deleteFieldRoute,
  listStorageDevices
} from '@/api/storage-config';
import { searchFields, type FieldDetailInfo, type AuthInfo } from '@/api/field';
import { listProjects, type Dataset } from '@/api/project';
import { getDeviceTypeColor } from '@/constants/storage-device';

// Props定义
interface Props {
  routes: FieldRoute[];
  loading: boolean;
}

const props = withDefaults(defineProps<Props>(), {
  routes: () => [],
  loading: false
});

// Emits定义
const emits = defineEmits<{
  refresh: [searchParams?: { field_id?: number; dataset_id?: number; device_id?: number }];
}>();

// 定义表单数据类型
interface RouteFormData {
  route_id?: number;
  field_id: string | number;
  dataset_id: number;
  device_id: number;
}

// 搜索表单
const form = ref({
  field_id: '',
  dataset_id: '',
  device_id: undefined as any,
});

// 表格数据 - 基于props计算
const tableData = computed(() => props.routes);
const selectedKeys = ref<number[]>([]);
const pagination = ref({
  total: 0,
  current: 1,
  pageSize: 10,
});

// 监听routes变化，更新分页总数
watch(() => props.routes, (newRoutes) => {
  pagination.value.total = newRoutes.length;
}, { immediate: true });

// 弹窗相关
const modalVisible = ref(false);
const modalTitle = ref('新增字段路由配置');
const formRef = ref();
const formData = ref<RouteFormData>({
  field_id: '',
  dataset_id: undefined as any,
  device_id: undefined as any,
});

// 路由信息
const route = useRoute();

// 获取当前项目ID
const currentProjectId = computed(() => {
  const projectId = route.params.projectId;
  return projectId ? Number(projectId) : null;
});

// 认证信息
const AUTH_INFO: AuthInfo = {
  app_id: 'moox_backend_service',
  app_key: 'moox_backend_service_key'
};

// 字段选项
const fieldOptions = ref<FieldDetailInfo[]>([]);
const fieldLoading = ref(false);

// 数据集选项
const datasetOptions = ref<Dataset[]>([]);
const datasetLoading = ref(false);

// 搜索区域的字段过滤
const searchFieldKeyword = ref('');
const filteredFieldOptions = computed(() => {
  if (!searchFieldKeyword.value) {
    return fieldOptions.value;
  }
  const keyword = searchFieldKeyword.value.toLowerCase();
  return fieldOptions.value.filter(field =>
    field.field_name.toLowerCase().includes(keyword) ||
    field.interface_name.toLowerCase().includes(keyword) ||
    field.field_id.toString().includes(keyword)
  );
});

// 模态框的字段过滤
const modalFieldKeyword = ref('');
const filteredModalFieldOptions = computed(() => {
  if (!modalFieldKeyword.value) {
    return fieldOptions.value;
  }
  const keyword = modalFieldKeyword.value.toLowerCase();
  return fieldOptions.value.filter(field =>
    field.field_name.toLowerCase().includes(keyword) ||
    field.interface_name.toLowerCase().includes(keyword) ||
    field.field_id.toString().includes(keyword)
  );
});

// 存储设备选项
const deviceOptions = ref<StorageDevice[]>([]);
const deviceLoading = ref(false);

// 表单验证规则
const rules = {
  field_id: [{ required: true, message: '请输入字段ID' }],
  dataset_id: [{ required: true, message: '请选择数据集' }],
  device_id: [{ required: true, message: '请选择存储设备' }],
};

// 获取数据集名称
const getDatasetName = (datasetId: number): string => {
  if (datasetId === 0) {
    return '全部';
  }
  const dataset = datasetOptions.value.find(d => d.dataset_id === datasetId);
  return dataset ? `${dataset.dataset_name} (${datasetId})` : `数据集 (${datasetId})`;
};

// 获取字段名称的映射函数
const getFieldName = (fieldId: number): string => {
  if (fieldId === 999999999) {
    return '全部';
  }
  const field = fieldOptions.value.find(f => f.field_id === fieldId);
  return field ? `${field.field_name} (${fieldId})` : `字段 (${fieldId})`;
};

// 获取存储设备名称的映射函数
const getDeviceName = (deviceId: number): string => {
  const device = deviceOptions.value.find(d => d.device_id === deviceId);
  return device ? `${device.device_name} (${deviceId})` : `存储设备 (${deviceId})`;
};

// 获取存储设备颜色的映射函数
const getDeviceColor = (deviceId: number): string => {
  const device = deviceOptions.value.find(d => d.device_id === deviceId);
  return device ? getDeviceTypeColor(device.device_type) : 'gray';
};

// 字段搜索处理函数
const handleFieldSearch = (value: string) => {
  searchFieldKeyword.value = value;
};

// 模态框字段搜索处理函数
const handleModalFieldSearch = (value: string) => {
  modalFieldKeyword.value = value;
};

// 获取字段列表
const loadFieldOptions = async () => {
  if (!currentProjectId.value) {
    console.warn('当前项目ID为空，无法获取字段列表');
    return;
  }

  try {
    fieldLoading.value = true;
    const response = await searchFields({
      auth_info: AUTH_INFO,
      proj_id: currentProjectId.value,
      page_info: {
        page_idx: 1,
        size: 200 // 获取足够多的字段
      }
    });

    fieldOptions.value = response.field_detail_infos || [];
    console.log('字段列表加载成功:', fieldOptions.value);
  } catch (error: any) {
    console.error('获取字段列表失败:', error);
    Message.error(error.message || '获取字段列表失败');
    fieldOptions.value = [];
  } finally {
    fieldLoading.value = false;
  }
};

// 获取存储设备列表
const loadDeviceOptions = async () => {
  try {
    deviceLoading.value = true;
    const response = await listStorageDevices();
    deviceOptions.value = response.devices || [];
    console.log('存储设备列表加载成功:', deviceOptions.value);
  } catch (error: any) {
    console.error('获取存储设备列表失败:', error);
    Message.error(error.message || '获取存储设备列表失败');
    deviceOptions.value = [];
  } finally {
    deviceLoading.value = false;
  }
};

// 获取数据集列表
const loadDatasetOptions = async () => {
  if (!currentProjectId.value) {
    console.warn('当前项目ID为空，无法获取数据集列表');
    return;
  }
  
  try {
    datasetLoading.value = true;
    const projects = await listProjects();
    const currentProject = projects.find(p => p.id === currentProjectId.value);
    
    if (currentProject && currentProject.datasets) {
      datasetOptions.value = currentProject.datasets;
      console.log('数据集列表加载成功:', datasetOptions.value);
    } else {
      datasetOptions.value = [];
      console.warn('当前项目无数据集或项目不存在');
    }
  } catch (error: any) {
    console.error('获取数据集列表失败:', error);
    Message.error(error.message || '获取数据集列表失败');
    datasetOptions.value = [];
  } finally {
    datasetLoading.value = false;
  }
};

// 初始化加载映射数据
const initMappingData = async () => {
  await Promise.allSettled([
    loadFieldOptions(),
    loadDatasetOptions(),
    loadDeviceOptions()
  ]);
};

// 搜索
const search = () => {
  pagination.value.current = 1;
  // 构建搜索参数，只传递有值的参数，999999999表示全部字段，0表示全部数据集
  const searchParams: { field_id?: number; dataset_id?: number; device_id?: number } = {};
  if (form.value.field_id && form.value.field_id !== '' && Number(form.value.field_id) !== 999999999) {
    searchParams.field_id = Number(form.value.field_id);
  }
  if (form.value.dataset_id && form.value.dataset_id !== '' && Number(form.value.dataset_id) !== 0) {
    searchParams.dataset_id = Number(form.value.dataset_id);
  }
  if (form.value.device_id) {
    searchParams.device_id = form.value.device_id;
  }
  emits('refresh', searchParams);
};

// 重置
const reset = () => {
  form.value = {
    field_id: '',
    dataset_id: '',
    device_id: undefined as any,
  };
  // 重置时不传递搜索参数，获取全部数据
  emits('refresh');
};

// 页码改变
const onPageChange = (current: number) => {
  pagination.value.current = current;
};

// 表格选择
const select = (rowKeys: number[]) => {
  selectedKeys.value = rowKeys;
};

const selectAll = (checked: boolean) => {
  if (checked) {
    selectedKeys.value = tableData.value.map(item => item.route_id!);
  } else {
    selectedKeys.value = [];
  }
};

// 新增
const onAdd = async () => {
  modalTitle.value = '新增字段路由配置';
  formData.value = {
    field_id: '',
    dataset_id: undefined as any,
    device_id: undefined as any,
  };
  modalVisible.value = true;

  // 重置搜索关键词
  modalFieldKeyword.value = '';

  // 加载下拉框数据
  await Promise.allSettled([
    loadFieldOptions(),
    loadDatasetOptions(),
    loadDeviceOptions()
  ]);
};

// 编辑
const onUpdate = async (record: FieldRoute) => {
  modalTitle.value = '编辑字段路由配置';
  formData.value = {
    route_id: record.route_id,
    field_id: record.field_id,
    dataset_id: record.dataset_id,
    device_id: record.device_id,
  };
  modalVisible.value = true;

  // 重置搜索关键词
  modalFieldKeyword.value = '';

  // 加载下拉框数据
  await Promise.allSettled([
    loadFieldOptions(),
    loadDatasetOptions(),
    loadDeviceOptions()
  ]);
};

// 删除
const onDelete = async (record: FieldRoute) => {
  try {
    await deleteFieldRoute({ route_id: record.route_id });
    Message.success('删除字段路由配置成功');
    emits('refresh');
  } catch (error: any) {
    console.error('删除字段路由配置失败:', error);
    Message.error(error.message || '删除字段路由配置失败');
  }
};

// 批量删除
const batchDelete = () => {
  if (selectedKeys.value.length === 0) {
    Message.warning('请选择要删除的数据');
    return;
  }
  // TODO: 实现批量删除功能
  Message.success('批量删除成功');
  selectedKeys.value = [];
  emits('refresh');
};

// 确认保存
const handleOk = async () => {
  try {
    await formRef.value?.validate();

    // 处理字段ID和数据集ID，将999999999保持原样传递
    const fieldId = Number(formData.value.field_id);
    const datasetId = formData.value.dataset_id;

    if (formData.value.route_id) {
      // 编辑模式 - 调用更新接口
      await updateFieldRoute({
        route_id: formData.value.route_id,
        field_id: fieldId,
        dataset_id: datasetId,
        device_id: formData.value.device_id,
      });
      Message.success('更新字段路由配置成功');
    } else {
      // 新增模式 - 调用创建接口
      if (!currentProjectId.value) {
        Message.error('当前项目ID为空，无法创建字段路由配置');
        return;
      }
      await createFieldRoute({
        project_id: currentProjectId.value,
        field_id: fieldId,
        dataset_id: datasetId,
        device_id: formData.value.device_id,
      });
      Message.success('创建字段路由配置成功');
    }

    modalVisible.value = false;
    emits('refresh');
  } catch (error: any) {
    console.error('保存字段路由配置失败:', error);
    Message.error(error.message || '保存字段路由配置失败');
  }
};

// 关闭弹窗
const afterClose = () => {
  modalVisible.value = false;
  // 重置搜索关键词
  modalFieldKeyword.value = '';
};

// 组件挂载时初始化映射数据
onMounted(() => {
  initMappingData();
});
</script>

<style lang="scss" scoped>
.moox-inner {
  > .a-space {
    margin-bottom: 16px;
  }
  
  > .a-row {
    margin-bottom: 16px;
  }
}
</style> 
