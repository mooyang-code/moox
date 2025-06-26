<template>
  <div class="moox-inner">
    <!-- 搜索区域 -->
    <a-space wrap>
      <a-select v-model="form.entity_id" placeholder="请选择存储实体" allow-clear style="width: 200px" :loading="entityLoading">
        <a-option 
          v-for="entity in entityOptions" 
          :key="entity.entity_id" 
          :value="entity.entity_id"
        >
          {{ entity.entity_alias }} ({{ entity.entity_id }})
        </a-option>
      </a-select>
      <a-input v-model="form.field_id" placeholder="请输入字段ID" allow-clear style="width: 150px" />
      <a-select placeholder="请选择数据类型" v-model="form.data_category" style="width: 150px" allow-clear>
        <a-option value="1">静态字段</a-option>
        <a-option value="2">时序字段</a-option>
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
      row-key="id"
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
        <a-table-column title="存储实体" data-index="entity_id">
          <template #cell="{ record }">
            {{ getEntityName(record.entity_id) }}
          </template>
        </a-table-column>
        <a-table-column title="字段ID" data-index="field_id"></a-table-column>
        <a-table-column title="数据类型" data-index="data_category">
          <template #cell="{ record }">
            <a-tag :color="getDataCategoryColor(record.data_category)">
              {{ getDataCategoryName(record.data_category) }}
            </a-tag>
          </template>
        </a-table-column>
        <a-table-column title="存储设备" data-index="device_id">
          <template #cell="{ record }">
            {{ getDeviceName(record.device_id) }}
          </template>
        </a-table-column>
        <a-table-column title="状态" :width="100" align="center">
          <template #cell="{ record }">
            <a-tag bordered size="small" color="arcoblue" v-if="record.invalid !== 1">正常</a-tag>
            <a-tag bordered size="small" color="red" v-else>已删除</a-tag>
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
              <a-popconfirm type="warning" content="确定删除该配置吗?" @ok="onDelete(record)" v-if="record.invalid !== 1">
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
          <a-form-item field="entity_id" label="存储实体" validate-trigger="blur">
            <a-select v-model="formData.entity_id" placeholder="请选择存储实体" allow-clear :loading="entityLoading">
              <a-option 
                v-for="entity in entityOptions" 
                :key="entity.entity_id" 
                :value="entity.entity_id"
              >
                {{ entity.entity_alias }} ({{ entity.entity_id }})
              </a-option>
            </a-select>
          </a-form-item>
          <a-form-item field="field_id" label="字段ID" validate-trigger="blur">
            <a-input v-model="formData.field_id" placeholder="请输入字段ID" allow-clear />
          </a-form-item>
          <a-form-item field="data_category" label="数据类型" validate-trigger="blur">
            <a-select v-model="formData.data_category" placeholder="请选择数据类型">
              <a-option :value="1">静态字段</a-option>
              <a-option :value="2">时序字段</a-option>
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
import { Message } from '@arco-design/web-vue';
import { IconSearch, IconRefresh, IconPlus, IconEdit, IconDelete } from '@arco-design/web-vue/es/icon';
import type { FieldRoute, StorageEntity, StorageDevice } from '@/api/storage-config';
import { 
  createFieldRoute, 
  updateFieldRoute, 
  deleteFieldRoute,
  listStorageEntities,
  listStorageDevices
} from '@/api/storage-config';

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
  refresh: [searchParams?: { entity_id?: number; field_id?: number; data_category?: number; device_id?: number }];
}>();

// 定义表单数据类型
interface RouteFormData {
  id?: number;
  entity_id: number;
  field_id: string | number;
  data_category: number;
  device_id: number;
}

// 搜索表单
const form = ref({
  entity_id: undefined as any,
  field_id: '',
  data_category: '',
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
  entity_id: undefined as any,
  field_id: '',
  data_category: 1,
  device_id: undefined as any,
});

// 存储实体选项
const entityOptions = ref<StorageEntity[]>([]);
const entityLoading = ref(false);

// 存储设备选项
const deviceOptions = ref<StorageDevice[]>([]);
const deviceLoading = ref(false);

// 表单验证规则
const rules = {
  entity_id: [{ required: true, message: '请选择存储实体' }],
  field_id: [{ required: true, message: '请输入字段ID' }],
  data_category: [{ required: true, message: '请选择数据类型' }],
  device_id: [{ required: true, message: '请选择存储设备' }],
};

// 获取数据类型名称
const getDataCategoryName = (category: number) => {
  const categoryMap: Record<number, string> = {
    1: '静态字段',
    2: '时序字段',
  };
  return categoryMap[category] || '未知';
};

// 获取数据类型颜色
const getDataCategoryColor = (category: number) => {
  const colorMap: Record<number, string> = {
    1: 'blue',
    2: 'green',
  };
  return colorMap[category] || 'gray';
};

// 获取存储实体名称的映射函数
const getEntityName = (entityId: number): string => {
  const entity = entityOptions.value.find(e => e.entity_id === entityId);
  return entity ? `${entity.entity_alias} (${entityId})` : `存储实体 (${entityId})`;
};

// 获取存储设备名称的映射函数
const getDeviceName = (deviceId: number): string => {
  const device = deviceOptions.value.find(d => d.device_id === deviceId);
  return device ? `${device.device_name} (${deviceId})` : `存储设备 (${deviceId})`;
};

// 获取存储实体列表
const loadEntityOptions = async () => {
  try {
    entityLoading.value = true;
    const response = await listStorageEntities();
    entityOptions.value = response.entities || [];
    console.log('存储实体列表加载成功:', entityOptions.value);
  } catch (error: any) {
    console.error('获取存储实体列表失败:', error);
    Message.error(error.message || '获取存储实体列表失败');
    entityOptions.value = [];
  } finally {
    entityLoading.value = false;
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

// 初始化加载映射数据
const initMappingData = async () => {
  await Promise.all([
    loadEntityOptions(),
    loadDeviceOptions()
  ]);
};

// 搜索
const search = () => {
  pagination.value.current = 1;
  // 构建搜索参数，只传递有值的参数
  const searchParams: { entity_id?: number; field_id?: number; data_category?: number; device_id?: number } = {};
  if (form.value.entity_id) {
    searchParams.entity_id = form.value.entity_id;
  }
  if (form.value.field_id) {
    searchParams.field_id = Number(form.value.field_id);
  }
  if (form.value.data_category) {
    searchParams.data_category = Number(form.value.data_category);
  }
  if (form.value.device_id) {
    searchParams.device_id = form.value.device_id;
  }
  emits('refresh', searchParams);
};

// 重置
const reset = () => {
  form.value = {
    entity_id: undefined as any,
    field_id: '',
    data_category: '',
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
    selectedKeys.value = tableData.value.map(item => item.id!);
  } else {
    selectedKeys.value = [];
  }
};

// 新增
const onAdd = async () => {
  modalTitle.value = '新增字段路由配置';
  formData.value = {
    entity_id: undefined as any,
    field_id: '',
    data_category: 1,
    device_id: undefined as any,
  };
  modalVisible.value = true;
  
  // 加载下拉框数据
  await Promise.all([
    loadEntityOptions(),
    loadDeviceOptions()
  ]);
};

// 编辑
const onUpdate = async (record: FieldRoute) => {
  modalTitle.value = '编辑字段路由配置';
  formData.value = {
    id: record.id,
    entity_id: record.entity_id,
    field_id: record.field_id,
    data_category: record.data_category,
    device_id: record.device_id,
  };
  modalVisible.value = true;
  
  // 加载下拉框数据
  await Promise.all([
    loadEntityOptions(),
    loadDeviceOptions()
  ]);
};

// 删除
const onDelete = async (record: FieldRoute) => {
  try {
    await deleteFieldRoute({ id: record.id });
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
    
    if (formData.value.id) {
      // 编辑模式 - 调用更新接口
      await updateFieldRoute({
        id: formData.value.id,
        entity_id: formData.value.entity_id,
        field_id: Number(formData.value.field_id),
        data_category: formData.value.data_category,
        device_id: formData.value.device_id,
      });
      Message.success('更新字段路由配置成功');
    } else {
      // 新增模式 - 调用创建接口
      await createFieldRoute({
        entity_id: formData.value.entity_id,
        field_id: Number(formData.value.field_id),
        data_category: formData.value.data_category,
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