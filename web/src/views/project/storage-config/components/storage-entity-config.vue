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
              <a-table-column title="存储实体别名" data-index="entity_alias" />
              <a-table-column title="存储服务连接信息" data-index="entity_srv_conn" />
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
        <a-form-item field="entity_alias" label="存储实体别名">
          <a-input v-model="formData.entity_alias" placeholder="请输入存储实体别名" />
        </a-form-item>
        <a-form-item field="entity_srv_conn" label="存储服务连接信息">
          <a-input v-model="formData.entity_srv_conn" placeholder="例如：ip://0.0.0.0:18101" :disabled="!!formData.entity_id" />
        </a-form-item>
      </a-form>
    </a-modal>
  </div>
</template>

<script lang="ts" setup>
import { ref, computed, watch } from 'vue';
import { Message, Modal } from '@arco-design/web-vue';
import { IconPlus, IconEdit, IconDelete } from '@arco-design/web-vue/es/icon';
import type { StorageEntity } from '@/api/storage-config';
import { 
  createStorageEntity, 
  updateStorageEntity, 
  deleteStorageEntity 
} from '@/api/storage-config';

// Props定义
interface Props {
  entities: StorageEntity[];
  loading: boolean;
}

const props = withDefaults(defineProps<Props>(), {
  entities: () => [],
  loading: false
});

// Emits定义
const emits = defineEmits<{
  refresh: [];
}>();

// 定义表单数据类型
interface EntityFormData {
  entity_id?: number;
  entity_alias: string;
  entity_srv_conn: string;
}

// 表格数据 - 基于props计算
const tableData = computed(() => props.entities);

const pagination = ref({
  total: 0,
  current: 1,
  pageSize: 10,
});

// 监听entities变化，更新分页总数
watch(() => props.entities, (newEntities) => {
  pagination.value.total = newEntities.length;
}, { immediate: true });

// 弹窗相关
const modalVisible = ref(false);
const modalTitle = ref('新增存储实体');
const formRef = ref();
const formData = ref<EntityFormData>({
  entity_alias: '',
  entity_srv_conn: '',
});

// 表单验证规则
const rules = {
  entity_alias: [{ required: true, message: '请输入存储实体别名' }],
  entity_srv_conn: [{ required: true, message: '请输入存储服务连接信息' }],
};

// 页码改变
const onPageChange = (current: number) => {
  pagination.value.current = current;
};

// 新增
const onAdd = () => {
  modalTitle.value = '新增存储实体';
  formData.value = {
    entity_alias: '',
    entity_srv_conn: '',
  };
  modalVisible.value = true;
};

// 编辑
const onEdit = (record: StorageEntity) => {
  modalTitle.value = '编辑存储实体';
  formData.value = {
    entity_id: record.entity_id,
    entity_alias: record.entity_alias,
    entity_srv_conn: record.entity_srv_conn,
  };
  modalVisible.value = true;
};

// 删除
const onDelete = async (record: StorageEntity) => {
  Modal.confirm({
    title: '确认删除',
    content: `确定要删除存储实体 "${record.entity_alias}" 吗？此操作不可恢复。`,
    okText: '确定删除',
    cancelText: '取消',
    async onOk() {
      try {
        await deleteStorageEntity({ entity_id: record.entity_id });
        Message.success(`删除存储实体 ${record.entity_alias} 成功`);
        // 删除后刷新数据
        emits('refresh');
      } catch (error: any) {
        console.error('删除存储实体失败:', error);
        Message.error(error.message || '删除存储实体失败');
      }
    }
  });
};

// 确认保存
const handleOk = async () => {
  try {
    await formRef.value?.validate();
    
    if (formData.value.entity_id) {
      // 编辑模式 - 调用更新接口
      await updateStorageEntity({
        entity_id: formData.value.entity_id,
        entity_alias: formData.value.entity_alias,
      });
      Message.success('更新存储实体成功');
    } else {
      // 新增模式 - 调用创建接口
      await createStorageEntity({
        entity_alias: formData.value.entity_alias,
        entity_srv_conn: formData.value.entity_srv_conn,
      });
      Message.success('创建存储实体成功');
    }
    
    modalVisible.value = false;
    // 保存后刷新数据
    emits('refresh');
  } catch (error: any) {
    console.error('保存存储实体失败:', error);
    Message.error(error.message || '保存存储实体失败');
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
