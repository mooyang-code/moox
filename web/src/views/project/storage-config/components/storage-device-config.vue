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
            <div>存储设备是具体的数据存储介质，如SQLite、DuckDB等数据库。</div>
            <div>配置项包括：设备ID、设备名称、设备类型、连接信息等。</div>
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
          <a-select v-model="formData.device_type" placeholder="请选择设备类型">
            <a-option :value="1">SQLite</a-option>
            <a-option :value="2">DuckDB</a-option>
            <a-option :value="3">MySQL</a-option>
            <a-option :value="4">PostgreSQL</a-option>
          </a-select>
        </a-form-item>
        <a-form-item field="conn_info" label="连接信息">
          <a-input v-model="formData.conn_info" placeholder="请输入连接信息" />
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
  conn_info: '',
});

// 表单验证规则
const rules = {
  device_name: [{ required: true, message: '请输入设备名称' }],
  device_type: [{ required: true, message: '请选择设备类型' }],
  conn_info: [{ required: true, message: '请输入连接信息' }],
};

// 获取设备类型名称
const getDeviceTypeName = (type: number) => {
  const typeMap: Record<number, string> = {
    1: 'SQLite',
    2: 'DuckDB',
    3: 'MySQL',
    4: 'PostgreSQL',
  };
  return typeMap[type] || '未知';
};

// 获取设备类型颜色
const getDeviceTypeColor = (type: number) => {
  const colorMap: Record<number, string> = {
    1: 'blue',
    2: 'green',
    3: 'orange',
    4: 'purple',
  };
  return colorMap[type] || 'gray';
};

// 页码改变
const onPageChange = (current: number) => {
  pagination.value.current = current;
};

// 新增
const onAdd = () => {
  modalTitle.value = '新增存储设备';
  formData.value = {
    device_name: '',
    device_type: 1,
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
        device_type: formData.value.device_type,
        conn_info: formData.value.conn_info,
      });
      Message.success('更新存储设备成功');
    } else {
      // 新增模式 - 调用创建接口
      await createStorageDevice({
        device_name: formData.value.device_name,
        device_type: formData.value.device_type,
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