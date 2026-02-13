<template>
  <div class="moox-page">
    <div class="moox-inner">
      <a-space wrap>
        <a-input v-model="form.name" placeholder="请输入因子名称" allow-clear />
        <a-input v-model="form.code" placeholder="请输入因子标识" allow-clear />
        <a-select placeholder="因子状态" v-model="form.status" style="width: 120px" allow-clear>
          <a-option v-for="item in openState" :key="item.value" :value="item.value">{{ item.name }}</a-option>
        </a-select>
        <a-range-picker v-model="form.time" show-time format="YYYY-MM-DD HH:mm" allow-clear />
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
          <a-button type="primary" status="danger">
            <template #icon><icon-delete /></template>
            <span>删除</span>
          </a-button>
        </a-space>
      </a-row>

      <a-table
        row-key="id"
        :data="factorList"
        :bordered="{ cell: true }"
        :loading="loading"
        :scroll="{ x: '100%', y: '100%', minWidth: 1000 }"
        :pagination="pagination"
        :row-selection="{ type: 'checkbox', showCheckedAll: true }"
        :selected-keys="selectedKeys"
        @select="select"
        @select-all="selectAll"
      >
        <template #columns>
          <a-table-column title="序号" :width="64">
            <template #cell="cell">{{ cell.rowIndex + 1 }}</template>
          </a-table-column>
          <a-table-column title="因子名称" data-index="name"></a-table-column>
          <a-table-column title="因子标识" data-index="code"></a-table-column>
          <a-table-column title="因子类型" data-index="type" :width="120" align="center"></a-table-column>
          <a-table-column title="排序" data-index="sort" :width="100" align="center"></a-table-column>
          <a-table-column title="状态" :width="100" align="center">
            <template #cell="{ record }">
              <a-tag bordered size="small" color="arcoblue" v-if="record.status === 1">启用</a-tag>
              <a-tag bordered size="small" color="red" v-else>禁用</a-tag>
            </template>
          </a-table-column>
          <a-table-column title="描述" data-index="description" :ellipsis="true" :tooltip="true"></a-table-column>
          <a-table-column title="创建时间" data-index="createTime" :width="180"></a-table-column>
          <a-table-column title="操作" :width="200" align="center" :fixed="'right'">
            <template #cell="{ record }">
              <a-space>
                <a-button type="primary" size="mini" @click="onUpdate(record)">
                  <template #icon><icon-edit /></template>
                  <span>修改</span>
                </a-button>
                <a-popconfirm type="warning" content="确定删除该因子吗?">
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
    </div>
    <a-modal width="40%" v-model:visible="open" @close="afterClose" @ok="handleOk" @cancel="afterClose">
      <template #title> {{ title }} </template>
      <div>
        <a-form ref="formRef" auto-label-width :rules="rules" :model="addFrom">
          <a-form-item field="name" label="因子名称" validate-trigger="blur">
            <a-input v-model="addFrom.name" placeholder="请输入因子名称" allow-clear />
          </a-form-item>
          <a-form-item field="code" label="因子标识" validate-trigger="blur">
            <a-input v-model="addFrom.code" placeholder="请输入因子标识" allow-clear />
          </a-form-item>
          <a-form-item field="type" label="因子类型" validate-trigger="blur">
            <a-select v-model="addFrom.type" placeholder="请选择因子类型" allow-clear>
              <a-option value="基础因子">基础因子</a-option>
              <a-option value="计算因子">计算因子</a-option>
              <a-option value="衍生因子">衍生因子</a-option>
            </a-select>
          </a-form-item>
          <a-form-item field="status" label="状态" validate-trigger="blur">
            <a-switch type="round" :checked-value="1" :unchecked-value="0" v-model="addFrom.status">
              <template #checked> 启用 </template>
              <template #unchecked> 禁用 </template>
            </a-switch>
          </a-form-item>
          <a-form-item field="sort" label="排序" validate-trigger="blur">
            <a-input-number
              v-model="addFrom.sort"
              :step="1"
              :precision="0"
              :min="1"
              :max="9999"
              :style="{ width: '150px' }"
              placeholder="请输入"
              mode="button"
              class="input-demo"
            />
          </a-form-item>
          <a-form-item field="description" label="描述" validate-trigger="blur">
            <a-textarea v-model="addFrom.description" placeholder="请输入描述" allow-clear />
          </a-form-item>
        </a-form>
      </div>
    </a-modal>
  </div>
</template>

<script setup lang="ts">
import { deepClone } from "@/utils";

const openState = ref(dictFilter("status"));
const form = ref({
  name: "",
  code: "",
  time: [],
  status: null
});
const search = () => {
  getFactorList();
};
const reset = () => {
  form.value = {
    name: "",
    code: "",
    time: [],
    status: null
  };
  getFactorList();
};

// 新增
const open = ref(false);
const rules = {
  name: [{ required: true, message: "请输入因子名称" }],
  code: [{ required: true, message: "请输入因子标识" }],
  type: [{ required: true, message: "请选择因子类型" }]
};
const addFrom = ref<any>({
  name: "",
  code: "",
  type: "",
  status: 1,
  sort: 1,
  description: ""
});

const title = ref("");
const formRef = ref();
const onAdd = () => {
  title.value = "新增因子";
  open.value = true;
};
const handleOk = async () => {
  let state = await formRef.value.validate();
  if (state) return (open.value = true);
  arcoMessage("success", "模拟提交成功");
  getFactorList();
};
const afterClose = () => {
  formRef.value.resetFields();
  addFrom.value = {
    name: "",
    code: "",
    type: "",
    status: 1,
    sort: 1,
    description: ""
  };
};
const onUpdate = (row: any) => {
  title.value = "修改因子";
  addFrom.value = deepClone(row);
  open.value = true;
};

// 获取列表
const loading = ref(false);
const pagination = ref({
  pageSize: 10,
  showPageSize: true
});
const factorList = ref([
  {
    id: 1,
    name: "CPU使用率",
    code: "cpu_usage",
    type: "基础因子",
    sort: 1,
    status: 1,
    description: "采集主机CPU使用率",
    createTime: "2025-06-01 10:00:00"
  },
  {
    id: 2,
    name: "内存使用率",
    code: "mem_usage",
    type: "基础因子",
    sort: 2,
    status: 1,
    description: "采集主机内存使用率",
    createTime: "2025-06-01 10:00:00"
  },
  {
    id: 3,
    name: "磁盘IO",
    code: "disk_io",
    type: "基础因子",
    sort: 3,
    status: 1,
    description: "采集主机磁盘IO读写速率",
    createTime: "2025-06-02 14:30:00"
  },
  {
    id: 4,
    name: "负载评分",
    code: "load_score",
    type: "计算因子",
    sort: 4,
    status: 1,
    description: "基于CPU和内存综合计算的负载评分",
    createTime: "2025-06-03 09:15:00"
  },
  {
    id: 5,
    name: "健康指数",
    code: "health_index",
    type: "衍生因子",
    sort: 5,
    status: 0,
    description: "综合多项基础因子衍生的健康指数",
    createTime: "2025-06-04 16:20:00"
  }
]);
const getFactorList = () => {
  loading.value = true;
  setTimeout(() => {
    loading.value = false;
  }, 300);
};
const selectedKeys = ref([]);
const select = (list: []) => {
  selectedKeys.value = list;
};
const selectAll = (state: boolean) => {
  selectedKeys.value = state ? (factorList.value.map((el: any) => el.id) as []) : [];
};

getFactorList();
</script>

<style lang="scss" scoped></style>
