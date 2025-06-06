<template>
  <div class="moox-inner">
    <a-row :gutter="24">
      <a-col :span="24">
        <a-card title="存储实体配置">
          <template #extra>
            <a-button type="primary" @click="onAdd">
              <template #icon><icon-plus /></template>
              新增配置
            </a-button>
          </template>
          <a-alert style="width: 100%" type="info" class="content" :show-icon="false">
            <div>存储实体是数据存储的逻辑单元，每个实体对应一个独立的存储服务连接。</div>
            <div>配置项包括：实体ID、服务连接信息等。</div>
          </a-alert>
          <a-table :data="tableData" :loading="loading" :pagination="pagination" @page-change="onPageChange">
            <template #columns>
              <a-table-column title="存储实体ID" data-index="entity_id" />
              <a-table-column title="存储服务连接信息" data-index="entity_srv_conn" />
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
        <a-form-item field="entity_id" label="存储实体ID">
          <a-input-number v-model="formData.entity_id" placeholder="请输入存储实体ID" :min="1" />
        </a-form-item>
        <a-form-item field="entity_srv_conn" label="存储服务连接信息">
          <a-input v-model="formData.entity_srv_conn" placeholder="例如：ip://0.0.0.0:18103" />
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
interface StorageEntity {
  id?: number;
  entity_id: number;
  entity_srv_conn: string;
  status: boolean;
  updateTime?: string;
}

// 表格数据
const tableData = ref<StorageEntity[]>([]);
const loading = ref(false);
const pagination = ref({
  total: 0,
  current: 1,
  pageSize: 10,
});

// 弹窗相关
const modalVisible = ref(false);
const modalTitle = ref('新增存储实体');
const formRef = ref();
const formData = ref<StorageEntity>({
  entity_id: 1,
  entity_srv_conn: '',
  status: true,
});

// 表单验证规则
const rules = {
  entity_id: [{ required: true, message: '请输入存储实体ID' }],
  entity_srv_conn: [{ required: true, message: '请输入存储服务连接信息' }],
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
        entity_id: 1,
        entity_srv_conn: 'ip://0.0.0.0:18103',
        status: true,
        updateTime: '2025-03-20 10:00:00',
      },
    ];
    pagination.value.total = 1;
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
  modalTitle.value = '新增存储实体';
  formData.value = {
    entity_id: 1,
    entity_srv_conn: '',
    status: true,
  };
  modalVisible.value = true;
};

// 编辑
const onEdit = (record: StorageEntity) => {
  modalTitle.value = '编辑存储实体';
  formData.value = { ...record };
  modalVisible.value = true;
};

// 切换状态
const onToggleStatus = (record: StorageEntity) => {
  // TODO: 实现状态切换
  Message.info(`${record.status ? '禁用' : '启用'}存储实体 ${record.entity_id}`);
};

// 删除
const onDelete = (_record: StorageEntity) => {
  // TODO: 实现删除功能
  Message.info(`删除存储实体 ${_record.entity_id}`);
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