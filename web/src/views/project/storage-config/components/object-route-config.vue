<template>
  <div class="moox-inner">
    <!-- 搜索区域 -->
    <a-space wrap>
      <a-input v-model="form.dataset_id" placeholder="请输入数据集ID" allow-clear />
      <a-input v-model="form.object_id" placeholder="请输入数据对象ID" allow-clear />
      <a-input v-model="form.entity_id" placeholder="请输入存储实体ID" allow-clear />
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
        <a-table-column title="数据集ID" data-index="dataset_id"></a-table-column>
        <a-table-column title="数据对象ID" data-index="object_id"></a-table-column>
        <a-table-column title="存储实体ID" data-index="entity_id"></a-table-column>
        <a-table-column title="状态" :width="100" align="center">
          <template #cell="{ record }">
            <a-tag bordered size="small" color="arcoblue" v-if="record.status === 1">启用</a-tag>
            <a-tag bordered size="small" color="red" v-else>禁用</a-tag>
          </template>
        </a-table-column>
        <a-table-column title="创建时间" data-index="createTime" :width="180"></a-table-column>
        <a-table-column title="操作" :width="200" align="center" :fixed="'right'">
          <template #cell="{ record }">
            <a-space>
              <a-button type="primary" size="mini" @click="onUpdate(record)">
                <template #icon><icon-edit /></template>
                <span>修改</span>
              </a-button>
              <a-popconfirm type="warning" content="确定删除该配置吗?" @ok="onDelete(record)">
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
    <a-modal v-model:visible="modalVisible" @close="afterClose" @ok="handleOk" @cancel="afterClose">
      <template #title>{{ modalTitle }}</template>
      <div>
        <a-form ref="formRef" auto-label-width :rules="rules" :model="formData">
          <a-form-item field="dataset_id" label="数据集ID" validate-trigger="blur">
            <a-input-number v-model="formData.dataset_id" placeholder="请输入数据集ID" :min="1" />
          </a-form-item>
          <a-form-item field="object_id" label="数据对象ID" validate-trigger="blur">
            <a-input v-model="formData.object_id" placeholder="请输入数据对象ID（* 表示所有对象）" allow-clear />
          </a-form-item>
          <a-form-item field="entity_id" label="存储实体ID" validate-trigger="blur">
            <a-input-number v-model="formData.entity_id" placeholder="请输入存储实体ID" :min="1" />
          </a-form-item>
          <a-form-item field="status" label="状态" validate-trigger="blur">
            <a-switch type="round" :checked-value="1" :unchecked-value="0" v-model="formData.status">
              <template #checked>启用</template>
              <template #unchecked>禁用</template>
            </a-switch>
          </a-form-item>
        </a-form>
      </div>
    </a-modal>
  </div>
</template>

<script setup lang="ts">
import { ref, onMounted } from 'vue';
import { Message } from '@arco-design/web-vue';
import { IconSearch, IconRefresh, IconPlus, IconEdit, IconDelete } from '@arco-design/web-vue/es/icon';

// 定义数据类型
interface ObjectRoute {
  id?: number;
  dataset_id: number;
  object_id: string;
  entity_id: number;
  status: number;
  createTime?: string;
}

// 搜索表单
const form = ref({
  dataset_id: '',
  object_id: '',
  entity_id: '',
});

// 表格数据
const tableData = ref<ObjectRoute[]>([]);
const loading = ref(false);
const selectedKeys = ref<number[]>([]);
const pagination = ref({
  total: 0,
  current: 1,
  pageSize: 10,
});

// 弹窗相关
const modalVisible = ref(false);
const modalTitle = ref('新增对象路由配置');
const formRef = ref();
const formData = ref<ObjectRoute>({
  dataset_id: 101,
  object_id: '*',
  entity_id: 1,
  status: 1,
});

// 表单验证规则
const rules = {
  dataset_id: [{ required: true, message: '请输入数据集ID' }],
  object_id: [{ required: true, message: '请输入数据对象ID' }],
  entity_id: [{ required: true, message: '请输入存储实体ID' }],
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
        dataset_id: 101,
        object_id: '*',
        entity_id: 1,
        status: 1,
        createTime: '2025-03-20 10:00:00',
      },
      {
        id: 2,
        dataset_id: 102,
        object_id: '*',
        entity_id: 1,
        status: 1,
        createTime: '2025-03-20 10:00:00',
      },
    ];
    pagination.value.total = 2;
  } catch {
    Message.error('获取数据失败');
  } finally {
    loading.value = false;
  }
};

// 搜索
const search = () => {
  pagination.value.current = 1;
  fetchData();
};

// 重置
const reset = () => {
  form.value = {
    dataset_id: '',
    object_id: '',
    entity_id: '',
  };
  fetchData();
};

// 页码改变
const onPageChange = (current: number) => {
  pagination.value.current = current;
  fetchData();
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
const onAdd = () => {
  modalTitle.value = '新增对象路由配置';
  formData.value = {
    dataset_id: 101,
    object_id: '*',
    entity_id: 1,
    status: 1,
  };
  modalVisible.value = true;
};

// 编辑
const onUpdate = (record: ObjectRoute) => {
  modalTitle.value = '编辑对象路由配置';
  formData.value = { ...record };
  modalVisible.value = true;
};

// 删除
const onDelete = (record: ObjectRoute) => {
  void record; // 避免未使用参数警告
  // TODO: 实现删除功能
  Message.success('删除成功');
  fetchData();
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
  fetchData();
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

// 关闭弹窗
const afterClose = () => {
  modalVisible.value = false;
};

onMounted(() => {
  fetchData();
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