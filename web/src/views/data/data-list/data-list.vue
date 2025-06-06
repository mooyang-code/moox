<template>
  <div class="moox-fill">
    <div class="moox-fill-inner container">
      <!-- 左侧对象树形列表 -->
      <div class="left-box">
        <a-input v-model="objectSearchKeyword" placeholder="请输入对象名称" allow-clear>
          <template #prefix>
            <icon-search />
          </template>
        </a-input>
        <div class="tree-box">
          <a-tree 
            ref="objectTreeRef" 
            :data="filteredObjectTree" 
            :field-names="objectFieldNames"
            show-line 
            @select="onSelectObject"
          >
            <template #icon="{ node }">
              <icon-storage v-if="!node.children" />
              <icon-folder v-else />
            </template>
          </a-tree>
        </div>
      </div>

      <!-- 右侧数据列表 -->
      <div class="right-box">
        <!-- 搜索区域 -->
        <a-space wrap style="margin-bottom: 16px">
          <a-input v-model="searchForm.keyword" placeholder="请输入数据内容" allow-clear />
          <a-select v-model="searchForm.status" placeholder="数据状态" style="width: 120px" allow-clear>
            <a-option value="">全部状态</a-option>
            <a-option value="1">正常</a-option>
            <a-option value="2">异常</a-option>
          </a-select>
          <a-range-picker v-model="searchForm.dateRange" show-time format="YYYY-MM-DD HH:mm" allow-clear />
          <a-button type="primary" @click="handleSearch">
            <template #icon><icon-search /></template>
            <span>查询</span>
          </a-button>
          <a-button @click="handleReset">
            <template #icon><icon-refresh /></template>
            <span>重置</span>
          </a-button>
        </a-space>

        <!-- 操作按钮 -->
        <a-row style="margin-bottom: 16px">
          <a-space wrap>
            <a-button type="primary" @click="handleAdd">
              <template #icon><icon-plus /></template>
              <span>新增数据</span>
            </a-button>
            <a-button type="primary" status="warning" @click="handleExport">
              <template #icon><icon-download /></template>
              <span>导出数据</span>
            </a-button>
            <a-button type="primary" status="danger" :disabled="selectedRowKeys.length === 0" @click="handleBatchDelete">
              <template #icon><icon-delete /></template>
              <span>批量删除</span>
            </a-button>
          </a-space>
        </a-row>

        <!-- 当前选中对象信息 -->
        <a-alert v-if="currentObject" style="margin-bottom: 16px" type="info" show-icon>
          <template #icon><icon-info-circle /></template>
          当前查看对象：<strong>{{ currentObject.title }}</strong>
          <span v-if="currentObject.description">（{{ currentObject.description }}）</span>
        </a-alert>

        <!-- 数据列表表格 -->
        <a-table
          row-key="id"
          :data="filteredDataList"
          :bordered="{ cell: true }"
          :loading="loading"
          :scroll="{ x: '120%', y: '100%' }"
          :pagination="pagination"
          :row-selection="{ 
            type: 'checkbox', 
            showCheckedAll: true,
            selectedRowKeys,
            onSelect: handleRowSelect,
            onSelectAll: handleSelectAll
          }"
          size="small"
        >
          <template #columns>
            <a-table-column title="序号" :width="64" align="center">
              <template #cell="{ rowIndex }">{{ rowIndex + 1 }}</template>
            </a-table-column>
            <a-table-column title="数据ID" data-index="dataId" :width="120"></a-table-column>
            <a-table-column title="对象名称" data-index="objectName" :width="120"></a-table-column>
            <a-table-column title="数据内容" data-index="content" :ellipsis="true" :tooltip="true"></a-table-column>
            <a-table-column title="数据类型" data-index="dataType" :width="100" align="center">
              <template #cell="{ record }">
                <a-tag size="small" :color="getDataTypeColor(record.dataType)">
                  {{ getDataTypeText(record.dataType) }}
                </a-tag>
              </template>
            </a-table-column>
            <a-table-column title="数据大小" data-index="dataSize" :width="100" align="center"></a-table-column>
            <a-table-column title="状态" data-index="status" :width="80" align="center">
              <template #cell="{ record }">
                <a-tag size="small" :color="record.status === 1 ? 'green' : 'red'">
                  {{ record.status === 1 ? '正常' : '异常' }}
                </a-tag>
              </template>
            </a-table-column>
            <a-table-column title="创建时间" data-index="createTime" :width="180"></a-table-column>
            <a-table-column title="更新时间" data-index="updateTime" :width="180"></a-table-column>
            <a-table-column title="操作" :width="200" align="center" :fixed="'right'">
              <template #cell="{ record }">
                <a-space>
                  <a-button type="primary" size="mini" @click="handleView(record)">
                    <template #icon><icon-eye /></template>
                    <span>查看</span>
                  </a-button>
                  <a-button type="primary" size="mini" @click="handleEdit(record)">
                    <template #icon><icon-edit /></template>
                    <span>编辑</span>
                  </a-button>
                  <a-popconfirm type="warning" content="确定删除该数据吗？">
                    <a-button type="primary" status="danger" size="mini" @click="handleDelete(record)">
                      <template #icon><icon-delete /></template>
                      <span>删除</span>
                    </a-button>
                  </a-popconfirm>
                </a-space>
              </template>
            </a-table-column>
          </template>
        </a-table>
      </div>
    </div>

    <!-- 数据详情/编辑弹窗 -->
    <a-modal
      v-model:visible="dataModalVisible"
      :title="modalTitle"
      width="800px"
      @ok="handleSaveData"
      @cancel="handleCancelData"
    >
      <a-form ref="dataFormRef" :model="dataForm" layout="vertical">
        <a-row :gutter="16">
          <a-col :span="12">
            <a-form-item
              label="数据ID"
              field="dataId"
              :rules="[{ required: true, message: '请输入数据ID' }]"
            >
              <a-input v-model="dataForm.dataId" placeholder="请输入数据ID" :disabled="isViewMode" />
            </a-form-item>
          </a-col>
          <a-col :span="12">
            <a-form-item
              label="对象名称"
              field="objectName"
              :rules="[{ required: true, message: '请选择对象' }]"
            >
              <a-select v-model="dataForm.objectName" placeholder="请选择对象" :disabled="isViewMode">
                <a-option v-for="obj in objectList" :key="obj.name" :value="obj.name">
                  {{ obj.name }}
                </a-option>
              </a-select>
            </a-form-item>
          </a-col>
        </a-row>
        
        <a-row :gutter="16">
          <a-col :span="12">
            <a-form-item
              label="数据类型"
              field="dataType"
              :rules="[{ required: true, message: '请选择数据类型' }]"
            >
              <a-select v-model="dataForm.dataType" placeholder="请选择数据类型" :disabled="isViewMode">
                <a-option value="json">JSON</a-option>
                <a-option value="xml">XML</a-option>
                <a-option value="text">TEXT</a-option>
                <a-option value="binary">BINARY</a-option>
              </a-select>
            </a-form-item>
          </a-col>
          <a-col :span="12">
            <a-form-item label="数据大小" field="dataSize">
              <a-input v-model="dataForm.dataSize" placeholder="数据大小" disabled />
            </a-form-item>
          </a-col>
        </a-row>

        <a-form-item
          label="数据内容"
          field="content"
          :rules="[{ required: true, message: '请输入数据内容' }]"
        >
          <a-textarea
            v-model="dataForm.content"
            placeholder="请输入数据内容"
            :auto-size="{ minRows: 6, maxRows: 12 }"
            :disabled="isViewMode"
          />
        </a-form-item>

        <a-form-item label="备注">
          <a-textarea
            v-model="dataForm.remark"
            placeholder="请输入备注信息"
            :max-length="200"
            show-word-limit
            :disabled="isViewMode"
          />
        </a-form-item>
      </a-form>
    </a-modal>
  </div>
</template>

<script setup lang="ts">
import { Message } from '@arco-design/web-vue';
import { useRoute } from 'vue-router';

interface ObjectNode {
  key: string;
  title: string;
  description?: string;
  objectName?: string;
  children?: ObjectNode[];
}

interface DataRecord {
  id: string;
  dataId: string;
  objectName: string;
  content: string;
  dataType: 'json' | 'xml' | 'text' | 'binary';
  dataSize: string;
  status: number;
  createTime: string;
  updateTime: string;
  remark?: string;
}

const route = useRoute();

// 获取当前项目ID
const projectId = computed(() => route.params.projectId as string);

// 对象树形数据
const objectTreeData = reactive<ObjectNode[]>([
  {
    key: 'user',
    title: '用户数据',
    description: '用户相关数据对象',
    children: [
      {
        key: 'user_profile',
        title: '用户资料',
        description: '用户基本信息数据',
        objectName: 'user_profile'
      },
      {
        key: 'user_behavior',
        title: '用户行为',
        description: '用户行为日志数据',
        objectName: 'user_behavior'
      }
    ]
  },
  {
    key: 'order',
    title: '订单数据',
    description: '订单相关数据对象',
    children: [
      {
        key: 'order_info',
        title: '订单信息',
        description: '订单基本信息数据',
        objectName: 'order_info'
      },
      {
        key: 'order_detail',
        title: '订单详情',
        description: '订单详细信息数据',
        objectName: 'order_detail'
      }
    ]
  },
  {
    key: 'product',
    title: '商品数据',
    description: '商品相关数据对象',
    children: [
      {
        key: 'product_info',
        title: '商品信息',
        description: '商品基本信息数据',
        objectName: 'product_info'
      }
    ]
  }
]);

// 对象搜索
const objectSearchKeyword = ref('');
const filteredObjectTree = computed(() => {
  if (!objectSearchKeyword.value) return objectTreeData;
  
  const filterNode = (node: ObjectNode): ObjectNode | null => {
    if (node.title.toLowerCase().includes(objectSearchKeyword.value.toLowerCase())) {
      return node;
    }
    if (node.children) {
      const filteredChildren = node.children.map(filterNode).filter(Boolean) as ObjectNode[];
      if (filteredChildren.length > 0) {
        return { ...node, children: filteredChildren };
      }
    }
    return null;
  };
  
  return objectTreeData.map(filterNode).filter(Boolean) as ObjectNode[];
});

// 对象树配置
const objectFieldNames = {
  key: 'key',
  title: 'title',
  children: 'children'
};

const objectTreeRef = ref();
const currentObject = ref<ObjectNode | null>(null);

// 获取所有对象列表（扁平化）
const objectList = computed(() => {
  const extractObjects = (nodes: ObjectNode[]): { key: string; name: string; title: string; description?: string }[] => {
    const result: { key: string; name: string; title: string; description?: string }[] = [];
    nodes.forEach(node => {
      if (node.objectName) {
        result.push({
          key: node.key,
          name: node.objectName,
          title: node.title,
          description: node.description
        });
      }
      if (node.children) {
        result.push(...extractObjects(node.children));
      }
    });
    return result;
  };
  return extractObjects(objectTreeData);
});

// 选择对象
const onSelectObject = (selectedKeys: string[], event: any) => {
  if (selectedKeys.length > 0) {
    const selectedNode = event.node as ObjectNode;
    if (selectedNode.objectName) {
      currentObject.value = selectedNode;
      // 根据选中的对象过滤数据列表
      handleSearch();
    }
  } else {
    currentObject.value = null;
    handleSearch();
  }
};

// 搜索表单
const searchForm = reactive({
  keyword: '',
  status: '',
  dateRange: []
});

// 数据列表
const dataList = reactive<DataRecord[]>([
  {
    id: 'data-001',
    dataId: 'USR_001',
    objectName: 'user_profile',
    content: '{"id": 1, "name": "张三", "age": 25, "email": "zhangsan@example.com"}',
    dataType: 'json',
    dataSize: '156 B',
    status: 1,
    createTime: '2024-01-15 10:30:00',
    updateTime: '2024-01-20 14:20:00',
    remark: '用户基本信息'
  },
  {
    id: 'data-002',
    dataId: 'USR_002',
    objectName: 'user_behavior',
    content: '{"userId": 1, "action": "login", "timestamp": "2024-01-20T14:20:00Z"}',
    dataType: 'json',
    dataSize: '89 B',
    status: 1,
    createTime: '2024-01-16 14:20:00',
    updateTime: '2024-01-21 09:15:00',
    remark: '用户登录行为'
  },
  {
    id: 'data-003',
    dataId: 'ORD_001',
    objectName: 'order_info',
    content: '{"orderId": "12345", "userId": 1, "amount": 299.99, "status": "paid"}',
    dataType: 'json',
    dataSize: '78 B',
    status: 1,
    createTime: '2024-01-17 09:15:00',
    updateTime: '2024-01-22 16:30:00',
    remark: '订单基本信息'
  },
  {
    id: 'data-004',
    dataId: 'PRD_001',
    objectName: 'product_info',
    content: '<product><id>1</id><name>商品名称</name><price>99.99</price></product>',
    dataType: 'xml',
    dataSize: '134 B',
    status: 2,
    createTime: '2024-01-18 11:45:00',
    updateTime: '2024-01-23 10:20:00',
    remark: '商品信息数据'
  }
]);

// 过滤后的数据列表
const filteredDataList = computed(() => {
  let result = dataList;
  
  // 根据选中的对象过滤
  if (currentObject.value?.objectName) {
    result = result.filter(item => item.objectName === currentObject.value!.objectName);
  }
  
  // 根据搜索条件过滤
  if (searchForm.keyword) {
    result = result.filter(item => 
      item.content.toLowerCase().includes(searchForm.keyword.toLowerCase()) ||
      item.dataId.toLowerCase().includes(searchForm.keyword.toLowerCase())
    );
  }
  
  if (searchForm.status) {
    result = result.filter(item => item.status === Number(searchForm.status));
  }
  
  return result;
});

// 表格相关
const loading = ref(false);
const selectedRowKeys = ref<string[]>([]);
const pagination = ref({
  pageSize: 10,
  showPageSize: true
});

// 行选择
const handleRowSelect = (rowKeys: string[]) => {
  selectedRowKeys.value = rowKeys;
};

const handleSelectAll = (checked: boolean) => {
  selectedRowKeys.value = checked ? filteredDataList.value.map(item => item.id) : [];
};

// 弹窗相关
const dataModalVisible = ref(false);
const modalTitle = ref('');
const isViewMode = ref(false);
const dataForm = reactive({
  dataId: '',
  objectName: '',
  content: '',
  dataType: 'json' as 'json' | 'xml' | 'text' | 'binary',
  dataSize: '',
  remark: ''
});

// 工具函数
const getDataTypeColor = (type: string) => {
  const colors: Record<string, string> = {
    json: 'blue',
    xml: 'green',
    text: 'orange',
    binary: 'red'
  };
  return colors[type] || 'gray';
};

const getDataTypeText = (type: string) => {
  const texts: Record<string, string> = {
    json: 'JSON',
    xml: 'XML',
    text: 'TEXT',
    binary: 'BINARY'
  };
  return texts[type] || type.toUpperCase();
};

// 事件处理
const handleSearch = () => {
  // 搜索逻辑已在计算属性中实现
};

const handleReset = () => {
  searchForm.keyword = '';
  searchForm.status = '';
  searchForm.dateRange = [];
  currentObject.value = null;
  objectTreeRef.value?.selectNode([], true);
};

const handleAdd = () => {
  modalTitle.value = '新增数据';
  isViewMode.value = false;
  resetDataForm();
  dataModalVisible.value = true;
};

const handleView = (record: DataRecord) => {
  modalTitle.value = '查看数据';
  isViewMode.value = true;
  Object.assign(dataForm, record);
  dataModalVisible.value = true;
};

const handleEdit = (record: DataRecord) => {
  modalTitle.value = '编辑数据';
  isViewMode.value = false;
  Object.assign(dataForm, record);
  dataModalVisible.value = true;
};

const handleDelete = (record: DataRecord) => {
  const index = dataList.findIndex(item => item.id === record.id);
  if (index > -1) {
    dataList.splice(index, 1);
    Message.success('删除成功');
  }
};

const handleBatchDelete = () => {
  // 批量删除逻辑
  Message.success('批量删除成功');
  selectedRowKeys.value = [];
};

const handleExport = () => {
  Message.success('导出功能开发中');
};

const handleSaveData = () => {
  if (isViewMode.value) {
    dataModalVisible.value = false;
    return;
  }
  
  // 这里应该进行表单验证和保存逻辑
  Message.success('保存成功');
  dataModalVisible.value = false;
  resetDataForm();
};

const handleCancelData = () => {
  dataModalVisible.value = false;
  resetDataForm();
};

const resetDataForm = () => {
  Object.assign(dataForm, {
    dataId: '',
    objectName: '',
    content: '',
    dataType: 'json' as 'json' | 'xml' | 'text' | 'binary',
    dataSize: '',
    remark: ''
  });
};

onMounted(() => {
  // 页面加载时展开对象树
  setTimeout(() => {
    objectTreeRef.value?.expandAll();
  }, 100);
});
</script>

<style lang="scss" scoped>
.container {
  display: flex;
  column-gap: $padding;
  
  .left-box {
    display: flex;
    flex-direction: column;
    width: 280px;
    height: 100%;
    
    .tree-box {
      flex: 1;
      margin-top: $padding;
      overflow: auto;
      border: 1px solid var(--color-border-2);
      border-radius: 4px;
      padding: 8px;
    }
  }
  
  .right-box {
    flex: 1;
  }
}

:deep(.arco-tree-node-title) {
  display: flex;
  align-items: center;
  gap: 4px;
}
</style> 