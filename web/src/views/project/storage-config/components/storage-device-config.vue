<template>
  <div class="moox-inner">
    <a-row :gutter="24">
      <a-col :span="24">
        <a-card title="存储设备配置">
          <template #extra>
            <a-button type="primary" @click="onAdd">
              <template #icon><icon-plus /></template>
              新增配置
            </a-button>
          </template>
          <a-alert style="width: 100%" type="info" class="content" :show-icon="false">
            <div>存储设备是具体的数据存储介质，支持SQLite、DuckDB、Bleve、CSV四种类型。</div>
            <div>配置项包括：设备ID、设备名称、设备类型、Schema需求、连接信息等。</div>
          </a-alert>
          <a-table :data="tableData" :loading="loading" :pagination="pagination" @page-change="onPageChange">
            <template #columns>
              <a-table-column title="设备ID" data-index="device_id" />
              <a-table-column title="设备名称" data-index="device_name" />
              <a-table-column title="设备类型" data-index="device_type">
                <template #cell="{ record }">
                  <a-tag :color="getDeviceTypeColor(record.device_type)">
                    {{ getDeviceTypeName(record.device_type) }}
                  </a-tag>
                </template>
              </a-table-column>
              <a-table-column title="是否Schema" :width="120" align="center">
                <template #cell="{ record }">
                  <a-tag bordered size="small" color="green" v-if="record.schema_required === 1">需要Schema</a-tag>
                  <a-tag bordered size="small" color="gray" v-else>无需Schema</a-tag>
                </template>
              </a-table-column>
              <a-table-column title="连接信息" data-index="conn_info" />
              <a-table-column title="状态" data-index="invalid">
                <template #cell="{ record }">
                  <a-tag :color="record.invalid === 1 ? 'red' : 'green'">
                    {{ record.invalid === 1 ? '已删除' : '正常' }}
                  </a-tag>
                </template>
              </a-table-column>
              <a-table-column title="更新时间" data-index="mtime" />
              <a-table-column title="操作" align="center">
                <template #cell="{ record }">
                  <a-space>
                    <a-button type="text" size="small" @click="onEdit(record)">
                      <template #icon><icon-edit /></template>
                      编辑
                    </a-button>
                    <a-button type="text" status="danger" size="small" @click="onDelete(record)" v-if="record.invalid !== 1">
                      <template #icon><icon-delete /></template>
                      删除
                    </a-button>
                  </a-space>
                </template>
              </a-table-column>
            </template>
          </a-table>
        </a-card>
      </a-col>
    </a-row>

    <!-- 新增/编辑弹窗 -->
    <a-modal v-model:visible="modalVisible" :title="modalTitle" @ok="handleOk" @cancel="handleCancel">
      <a-form ref="formRef" :model="formData" :rules="rules" auto-label-width>
        <a-form-item field="device_name" label="设备名称">
          <a-input v-model="formData.device_name" placeholder="请输入设备名称" />
        </a-form-item>
        <a-form-item field="device_type" label="设备类型">
          <a-select v-model="formData.device_type" placeholder="请选择设备类型" @change="onDeviceTypeChange" :disabled="!!formData.device_id">
            <a-option v-for="option in DEVICE_TYPE_OPTIONS" :key="option.value" :value="option.value">
              {{ option.label }}
            </a-option>
          </a-select>
        </a-form-item>
        <a-form-item field="schema_required" label="是否Schema">
          <a-select v-model="formData.schema_required" placeholder="请选择是否需要Schema" :disabled="!!formData.device_id">
            <a-option v-for="option in SCHEMA_REQUIRED_OPTIONS" :key="option.value" :value="option.value">
              {{ option.label }}
            </a-option>
          </a-select>
          <template #help>
            <div style="font-size: 12px; color: #999;">
              SQLite、DuckDB、CSV需要Schema初始化；Bleve无需Schema
            </div>
          </template>
        </a-form-item>
        <a-form-item field="conn_info" label="连接信息">
          <a-input v-model="formData.conn_info" placeholder="请输入连接信息" :disabled="!!formData.device_id" />
        </a-form-item>
      </a-form>
    </a-modal>
  </div>
</template>

<script lang="ts" setup>
import { ref, computed, watch } from 'vue';
import { Message, Modal } from '@arco-design/web-vue';
import { IconPlus, IconEdit, IconDelete } from '@arco-design/web-vue/es/icon';
import type { StorageDevice } from '@/api/storage-config';
import {
  createStorageDevice,
  updateStorageDevice,
  deleteStorageDevice
} from '@/api/storage-config';
import {
  DEVICE_TYPE_OPTIONS,
  SCHEMA_REQUIRED_OPTIONS,
  getDeviceTypeName,
  getDeviceTypeColor,
  getDeviceSchemaRequired
} from '@/constants/storage-device';

// Props定义
interface Props {
  devices: StorageDevice[];
  loading: boolean;
}

const props = withDefaults(defineProps<Props>(), {
  devices: () => [],
  loading: false
});

// Emits定义
const emits = defineEmits<{
  refresh: [];
}>();

// 定义表单数据类型
interface DeviceFormData {
  device_id?: number;
  device_name: string;
  device_type: number;
  schema_required: number;
  conn_info: string;
}

// 表格数据 - 基于props计算
const tableData = computed(() => props.devices);

const pagination = ref({
  total: 0,
  current: 1,
  pageSize: 10,
});

// 监听devices变化，更新分页总数
watch(() => props.devices, (newDevices) => {
  pagination.value.total = newDevices.length;
}, { immediate: true });

// 弹窗相关
const modalVisible = ref(false);
const modalTitle = ref('新增存储设备');
const formRef = ref();
const formData = ref<DeviceFormData>({
  device_name: '',
  device_type: 1,
  schema_required: 1, // 默认需要Schema
  conn_info: '',
});

// 表单验证规则
const rules = {
  device_name: [{ required: true, message: '请输入设备名称' }],
  device_type: [{ required: true, message: '请选择设备类型' }],
  schema_required: [{ required: true, message: '请选择是否需要Schema' }],
  conn_info: [{ required: true, message: '请输入连接信息' }],
};

// 注意：getDeviceTypeName 和 getDeviceTypeColor 函数现在从常量文件中导入

// 页码改变
const onPageChange = (current: number) => {
  pagination.value.current = current;
};

// 设备类型变化时自动设置Schema需求
const onDeviceTypeChange = (deviceType: number) => {
  // 根据设备类型自动设置Schema需求
  formData.value.schema_required = getDeviceSchemaRequired(deviceType);
};

// 新增
const onAdd = () => {
  modalTitle.value = '新增存储设备';
  formData.value = {
    device_name: '',
    device_type: 1, // 默认SQLite
    schema_required: 1, // 默认需要Schema
    conn_info: '',
  };
  modalVisible.value = true;
};

// 编辑
const onEdit = (record: StorageDevice) => {
  modalTitle.value = '编辑存储设备';
  formData.value = {
    device_id: record.device_id,
    device_name: record.device_name,
    device_type: record.device_type,
    schema_required: record.schema_required !== undefined ? record.schema_required : 1, // 保持原值，如果未定义则默认为1
    conn_info: record.conn_info,
  };
  modalVisible.value = true;
};

// 删除
const onDelete = async (record: StorageDevice) => {
  Modal.confirm({
    title: '确认删除',
    content: `确定要删除存储设备 "${record.device_name}" 吗？此操作不可恢复。`,
    okText: '确定删除',
    cancelText: '取消',
    async onOk() {
      try {
        await deleteStorageDevice({ device_id: record.device_id });
        Message.success(`删除存储设备 ${record.device_name} 成功`);
        // 删除后刷新数据
        emits('refresh');
      } catch (error: any) {
        console.error('删除存储设备失败:', error);
        Message.error(error.message || '删除存储设备失败');
      }
    }
  });
};

// 确认保存
const handleOk = async () => {
  try {
    await formRef.value?.validate();
    
    if (formData.value.device_id) {
      // 编辑模式 - 调用更新接口
      await updateStorageDevice({
        device_id: formData.value.device_id,
        device_name: formData.value.device_name,
      });
      Message.success('更新存储设备成功');
    } else {
      // 新增模式 - 调用创建接口
      await createStorageDevice({
        device_name: formData.value.device_name,
        device_type: formData.value.device_type,
        schema_required: formData.value.schema_required,
        conn_info: formData.value.conn_info,
      });
      Message.success('创建存储设备成功');
    }
    
    modalVisible.value = false;
    // 保存后刷新数据
    emits('refresh');
  } catch (error: any) {
    console.error('保存存储设备失败:', error);
    Message.error(error.message || '保存存储设备失败');
  }
};

// 取消
const handleCancel = () => {
  modalVisible.value = false;
};
</script>

<style lang="scss" scoped>
.content {
  margin: $margin 0;
}
</style> 
