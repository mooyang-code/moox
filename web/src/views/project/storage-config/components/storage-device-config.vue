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
              <a-table-column title="状态" data-index="status">
                <template #cell="{ record }">
                  <a-tag :color="record.status ? 'green' : 'red'">
                    {{ record.status ? '启用' : '禁用' }}
                  </a-tag>
                </template>
              </a-table-column>
              <a-table-column title="更新时间" data-index="updateTime" />
              <a-table-column title="操作" align="center">
                <template #cell="{ record }">
                  <a-space>
                    <a-button type="text" size="small" @click="onEdit(record)">
                      <template #icon><icon-edit /></template>
                      编辑
                    </a-button>
                    <a-button type="text" size="small" @click="onToggleStatus(record)">
                      <template #icon><icon-swap /></template>
                      {{ record.status ? '禁用' : '启用' }}
                    </a-button>
                    <a-button type="text" status="danger" size="small" @click="onDelete(record)">
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
        <a-form-item field="device_id" label="设备ID">
          <a-input-number v-model="formData.device_id" placeholder="请输入设备ID" :min="1" />
        </a-form-item>
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
        <a-form-item field="status" label="状态">
          <a-switch v-model="formData.status" checked-text="启用" unchecked-text="禁用" />
        </a-form-item>
      </a-form>
    </a-modal>
  </div>
</template>

<script lang="ts" setup>
import { ref, onMounted } from 'vue';
import { Message } from '@arco-design/web-vue';
import { IconPlus, IconEdit, IconDelete, IconSwap } from '@arco-design/web-vue/es/icon';

// 定义数据类型
interface StorageDevice {
  id?: number;
  device_id: number;
  device_name: string;
  device_type: number;
  conn_info: string;
  status: boolean;
  updateTime?: string;
}

// 表格数据
const tableData = ref<StorageDevice[]>([]);
const loading = ref(false);
const pagination = ref({
  total: 0,
  current: 1,
  pageSize: 10,
});

// 弹窗相关
const modalVisible = ref(false);
const modalTitle = ref('新增存储设备');
const formRef = ref();
const formData = ref<StorageDevice>({
  device_id: 1,
  device_name: '',
  device_type: 1,
  conn_info: '',
  status: true,
});

// 表单验证规则
const rules = {
  device_id: [{ required: true, message: '请输入设备ID' }],
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

// 获取数据
const fetchData = async () => {
  loading.value = true;
  try {
    // TODO: 调用接口获取数据
    // 模拟数据
    tableData.value = [
      {
        id: 1,
        device_id: 1,
        device_name: 'SQLite(一般用于存储静态数据)',
        device_type: 1,
        conn_info: 'localhost',
        status: true,
        updateTime: '2025-03-20 10:00:00',
      },
      {
        id: 2,
        device_id: 2,
        device_name: 'DuckDB',
        device_type: 2,
        conn_info: 'localhost',
        status: true,
        updateTime: '2025-03-20 10:00:00',
      },
    ];
    pagination.value.total = 2;
  } catch {
    Message.error('获取数据失败');
  } finally {
    loading.value = false;
  }
};

// 页码改变
const onPageChange = (current: number) => {
  pagination.value.current = current;
  fetchData();
};

// 新增
const onAdd = () => {
  modalTitle.value = '新增存储设备';
  formData.value = {
    device_id: 1,
    device_name: '',
    device_type: 1,
    conn_info: '',
    status: true,
  };
  modalVisible.value = true;
};

// 编辑
const onEdit = (record: StorageDevice) => {
  modalTitle.value = '编辑存储设备';
  formData.value = { ...record };
  modalVisible.value = true;
};

// 切换状态
const onToggleStatus = (record: StorageDevice) => {
  // TODO: 实现状态切换
  Message.info(`${record.status ? '禁用' : '启用'}存储设备 ${record.device_name}`);
};

// 删除
const onDelete = (_record: StorageDevice) => {
  // TODO: 实现删除功能
  Message.info(`删除存储设备 ${_record.device_name}`);
};

// 确认保存
const handleOk = async () => {
  try {
    await formRef.value?.validate();
    // TODO: 调用保存接口
    Message.success('保存成功');
    modalVisible.value = false;
    fetchData();
  } catch {
    // 表单验证失败
  }
};

// 取消
const handleCancel = () => {
  modalVisible.value = false;
};

onMounted(() => {
  fetchData();
});
</script>

<style lang="scss" scoped>
.content {
  margin: $margin 0;
}
</style> 