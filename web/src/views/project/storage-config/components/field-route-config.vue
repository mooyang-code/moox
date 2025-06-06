<template>
  <div class="moox-inner">
    <!-- 搜索区域 -->
    <a-space wrap>
      <a-input v-model="form.entity_id" placeholder="请输入存储实体ID" allow-clear />
      <a-select placeholder="数据类型" v-model="form.data_type" style="width: 150px" allow-clear>
        <a-option v-for="item in dataTypes" :key="item.value" :value="item.value">{{ item.name }}</a-option>
      </a-select>
      <a-input v-model="form.device_id" placeholder="请输入设备ID" allow-clear />
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
        <a-table-column title="存储实体ID" data-index="entity_id"></a-table-column>
        <a-table-column title="数据类型" data-index="data_type">
          <template #cell="{ record }">
            <a-tag :color="getDataTypeColor(record.data_type)">
              {{ getDataTypeName(record.data_type) }}
            </a-tag>
          </template>
        </a-table-column>
        <a-table-column title="设备ID" data-index="device_id"></a-table-column>
        <a-table-column title="设备名称" data-index="device_name"></a-table-column>
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
          <a-form-item field="entity_id" label="存储实体ID" validate-trigger="blur">
            <a-input-number v-model="formData.entity_id" placeholder="请输入存储实体ID" :min="1" />
          </a-form-item>
          <a-form-item field="data_type" label="数据类型" validate-trigger="blur">
            <a-select v-model="formData.data_type" placeholder="请选择数据类型">
              <a-option v-for="item in dataTypes" :key="item.value" :value="item.value">{{ item.name }}</a-option>
            </a-select>
          </a-form-item>
          <a-form-item field="device_id" label="设备ID" validate-trigger="blur">
            <a-input-number v-model="formData.device_id" placeholder="请输入设备ID" :min="1" />
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
interface FieldRoute {
  id?: number;
  entity_id: number;
  data_type: number;
  device_id: number;
  device_name?: string;
  status: number;
  createTime?: string;
}

// 数据类型配置
const dataTypes = ref([
  { value: 1, name: '字符串', color: 'blue' },
  { value: 2, name: '整数', color: 'green' },
  { value: 3, name: '浮点数', color: 'orange' },
  { value: 4, name: '布尔值', color: 'purple' },
  { value: 5, name: '日期时间', color: 'cyan' },
  { value: 6, name: 'JSON', color: 'magenta' },
]);

// 搜索表单
const form = ref({
  entity_id: '',
  data_type: null,
  device_id: '',
});

// 表格数据
const tableData = ref<FieldRoute[]>([]);
const loading = ref(false);
const selectedKeys = ref<number[]>([]);
const pagination = ref({
  total: 0,
  current: 1,
  pageSize: 10,
});

// 弹窗相关
const modalVisible = ref(false);
const modalTitle = ref('新增字段路由配置');
const formRef = ref();
const formData = ref<FieldRoute>({
  entity_id: 1,
  data_type: 1,
  device_id: 1,
  status: 1,
});

// 表单验证规则
const rules = {
  entity_id: [{ required: true, message: '请输入存储实体ID' }],
  data_type: [{ required: true, message: '请选择数据类型' }],
  device_id: [{ required: true, message: '请输入设备ID' }],
};

// 获取数据类型名称
const getDataTypeName = (type: number) => {
  const dataType = dataTypes.value.find(item => item.value === type);
  return dataType?.name || '未知';
};

// 获取数据类型颜色
const getDataTypeColor = (type: number) => {
  const dataType = dataTypes.value.find(item => item.value === type);
  return dataType?.color || 'gray';
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
        data_type: 1,
        device_id: 1,
        device_name: 'SQLite(一般用于存储静态数据)',
        status: 1,
        createTime: '2025-03-20 10:00:00',
      },
      {
        id: 2,
        entity_id: 1,
        data_type: 2,
        device_id: 2,
        device_name: 'DuckDB',
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
    entity_id: '',
    data_type: null,
    device_id: '',
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
  modalTitle.value = '新增字段路由配置';
  formData.value = {
    entity_id: 1,
    data_type: 1,
    device_id: 1,
    status: 1,
  };
  modalVisible.value = true;
};

// 编辑
const onUpdate = (record: FieldRoute) => {
  modalTitle.value = '编辑字段路由配置';
  formData.value = { ...record };
  modalVisible.value = true;
};

// 删除
const onDelete = (record: FieldRoute) => {
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