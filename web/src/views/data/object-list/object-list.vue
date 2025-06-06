<template>
  <div class="moox-page">
    <div class="moox-inner">
      <a-form ref="formRef" auto-label-width :model="formData.form">
        <a-row :gutter="16">
          <a-col :xs="24" :sm="24" :md="12" :lg="12" :xl="6" :xxl="6">
            <a-form-item field="objectName" label="对象名称">
              <a-input v-model="formData.form.objectName" placeholder="请输入对象名称" allow-clear />
            </a-form-item>
          </a-col>
          <a-col :xs="24" :sm="24" :md="12" :lg="12" :xl="6" :xxl="6">
            <a-form-item field="objectType" label="对象类型">
              <a-select v-model="formData.form.objectType" placeholder="请选择对象类型" allow-clear>
                <a-option value="1">表</a-option>
                <a-option value="2">视图</a-option>
                <a-option value="3">存储过程</a-option>
              </a-select>
            </a-form-item>
          </a-col>
          <a-col :xs="24" :sm="24" :md="12" :lg="12" :xl="6" :xxl="6">
            <a-form-item field="objectSchema" label="模式">
              <a-input v-model="formData.form.objectSchema" placeholder="请输入模式名称" allow-clear />
            </a-form-item>
          </a-col>
          <a-col :xs="24" :sm="24" :md="12" :lg="12" :xl="6" :xxl="6">
            <a-space class="search-btn">
              <a-button type="primary" @click="getObjectList">
                <template #icon>
                  <icon-search />
                </template>
                <template #default>查询</template>
              </a-button>
              <a-button @click="onReset">
                <template #icon>
                  <icon-refresh />
                </template>
                <template #default>重置</template>
              </a-button>
              <a-button type="text" @click="formData.search = !formData.search">
                <template #icon>
                  <icon-up v-if="formData.search" />
                  <icon-down v-else />
                </template>
                <template #default>{{ formData.search ? "收起" : "展开" }}</template>
              </a-button>
            </a-space>
          </a-col>
        </a-row>
        <a-row :gutter="16" v-if="formData.search">
          <a-col :xs="24" :sm="24" :md="12" :lg="12" :xl="6" :xxl="6">
            <a-form-item field="description" label="描述">
              <a-input v-model="formData.form.description" placeholder="请输入描述" allow-clear />
            </a-form-item>
          </a-col>
          <a-col :xs="24" :sm="24" :md="12" :lg="12" :xl="6" :xxl="6">
            <a-form-item field="status" label="状态">
              <a-select v-model="formData.form.status" placeholder="请选择状态" allow-clear>
                <a-option value="1">启用</a-option>
                <a-option value="2">停用</a-option>
              </a-select>
            </a-form-item>
          </a-col>
        </a-row>
      </a-form>
      <a-table
        row-key="key"
        size="small"
        :bordered="{
          cell: true
        }"
        :scroll="{ x: '100%', y: '100%', minWidth: 1000 }"
        :loading="loading"
        :columns="columns"
        :data="data"
        :row-selection="rowSelection"
        v-model:selectedKeys="selectedKeys"
        :pagination="pagination"
        @page-change="pageChange"
        @page-size-change="pageSizeChange"
      >
        <template #objectType="{ record }">
          <a-space>
            <a-tag size="small" color="blue" v-if="record.objectType == 1">表</a-tag>
            <a-tag size="small" color="green" v-else-if="record.objectType == 2">视图</a-tag>
            <a-tag size="small" color="orange" v-else>存储过程</a-tag>
          </a-space>
        </template>
        <template #status="{ record }">
          <a-space>
            <a-tag size="small" color="green" v-if="record.status == 1">启用</a-tag>
            <a-tag size="small" color="red" v-else>停用</a-tag>
          </a-space>
        </template>
        <template #optional>
          <a-space>
            <a-button size="mini" type="primary">详情</a-button>
            <a-button size="mini">修改</a-button>
            <a-popconfirm content="确定删除这个对象吗?" type="warning">
              <a-button size="mini" type="primary" status="danger">删除</a-button>
            </a-popconfirm>
          </a-space>
        </template>
      </a-table>
    </div>
  </div>
</template>

<script setup lang="ts">
import { getObjectListAPI } from "@/api/modules/data/index";
import { ObjectList, FormData, RowSelection, Pagination } from "./config";

const formData = reactive<FormData>({
  form: {
    objectName: "",
    objectType: null,
    objectSchema: "",
    description: "",
    status: null
  },
  search: false
});

const selectedKeys = ref<string[]>([]);
const rowSelection = reactive<RowSelection>({
  type: "checkbox",
  showCheckedAll: true,
  onlyCurrent: false
});

const pagination = ref<Pagination>({ 
  showPageSize: true, 
  showTotal: true, 
  current: 1, 
  pageSize: 10, 
  total: 10 
});

const pageChange = (page: number) => {
  pagination.value.current = page;
};

const pageSizeChange = (pageSize: number) => {
  pagination.value.pageSize = pageSize;
};

const columns = [
  {
    title: "对象名称",
    dataIndex: "objectName"
  },
  {
    title: "对象类型",
    dataIndex: "objectType",
    slotName: "objectType",
    align: "center"
  },
  {
    title: "模式",
    dataIndex: "objectSchema"
  },
  {
    title: "描述",
    dataIndex: "description"
  },
  {
    title: "记录数",
    dataIndex: "recordCount",
    align: "center"
  },
  {
    title: "状态",
    dataIndex: "status",
    align: "center",
    slotName: "status"
  },
  {
    title: "创建时间",
    dataIndex: "createTime"
  },
  {
    title: "操作",
    slotName: "optional",
    align: "center",
    fixed: "right",
    width: "200"
  }
];

const formRef = ref();
const onReset = () => {
  formRef.value.resetFields();
  getObjectList();
};

const loading = ref<boolean>(false);
const data = reactive<ObjectList[]>([]);

const getObjectList = async () => {
  try {
    loading.value = true;
    let res = await getObjectListAPI();
    Object.assign(data, res.data.list);
    pagination.value.total = res.data.total;
  } finally {
    loading.value = false;
  }
};

getObjectList();
</script>

<style lang="scss" scoped>
.search-btn {
  margin-bottom: 20px;
}
</style> 