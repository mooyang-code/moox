<template>
  <div class="moox-page">
    <a-spin :loading="loading">
      <div class="moox-inner">
        <a-space wrap>
          <a-input v-model="form.taskId" placeholder="请输入任务ID" allow-clear style="width: 200px" />
          <a-input v-model="form.ruleId" placeholder="请输入规则ID" allow-clear style="width: 200px" />
          <a-input v-model="form.nodeId" placeholder="请输入节点ID" allow-clear style="width: 220px" />
          <a-input v-model="form.symbol" placeholder="请输入交易标的" allow-clear style="width: 150px" />
          <a-select placeholder="执行状态" v-model="form.status" style="width: 120px" allow-clear>
            <a-option :value="0">待执行</a-option>
            <a-option :value="1">执行中</a-option>
            <a-option :value="2">成功</a-option>
            <a-option :value="3">失败</a-option>
            <a-option :value="4">超时</a-option>
            <a-option :value="5">已取消</a-option>
          </a-select>
          <a-select placeholder="是否有效" v-model="form.invalid" style="width: 120px" allow-clear>
            <a-option :value="0">有效</a-option>
            <a-option :value="1">无效</a-option>
          </a-select>
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
            <a-button type="primary" @click="refreshList">
              <template #icon><icon-sync /></template>
              <span>刷新</span>
            </a-button>
            <a-button type="primary" status="warning" @click="batchRetry">
              <template #icon><icon-redo /></template>
              <span>批量重试</span>
            </a-button>
            <a-button type="primary" status="danger" @click="batchInvalidate">
              <template #icon><icon-close-circle /></template>
              <span>批量作废</span>
            </a-button>
          </a-space>
        </a-row>

        <a-table
          row-key="TaskID"
          :data="instanceList"
          :bordered="{ cell: true }"
          :loading="loading"
          :scroll="{ x: 1800, y: '100%' }"
          :pagination="paginationConfig"
          :row-selection="{ type: 'checkbox', showCheckedAll: true }"
          :selected-keys="selectedKeys"
          @select="select"
          @select-all="selectAll"
          @page-change="onPageChange"
          @page-size-change="onPageSizeChange"
        >
          <template #columns>
            <a-table-column title="任务ID" data-index="TaskID" :width="200">
              <template #cell="{ record }">
                <a-link @click="onViewDetails(record)">{{ record.TaskID }}</a-link>
              </template>
            </a-table-column>
            <a-table-column title="规则ID" data-index="RuleID" :width="200">
              <template #cell="{ record }">
                <a-tooltip :content="record.RuleID">
                  <span class="ellipsis-text">{{ record.RuleID }}</span>
                </a-tooltip>
              </template>
            </a-table-column>
            <a-table-column title="交易标的" data-index="Symbol" :width="120">
              <template #cell="{ record }">
                <a-tag color="arcoblue" size="small">{{ record.Symbol }}</a-tag>
              </template>
            </a-table-column>
            <a-table-column title="执行节点" data-index="NodeID" :width="260">
              <template #cell="{ record }">
                <a-tooltip :content="record.NodeID">
                  <span class="ellipsis-text">{{ record.NodeID }}</span>
                </a-tooltip>
              </template>
            </a-table-column>
            <a-table-column title="任务参数" :width="180">
              <template #cell="{ record }">
                <a-popover title="任务参数详情" trigger="click">
                  <template #content>
                    <pre class="params-preview">{{ formatJSON(record.TaskParams) }}</pre>
                  </template>
                  <a-button type="text" size="mini">
                    <template #icon><icon-eye /></template>
                    查看参数
                  </a-button>
                </a-popover>
              </template>
            </a-table-column>
            <a-table-column title="执行状态" :width="100" align="center">
              <template #cell="{ record }">
                <a-tag bordered size="small" :color="getStatusColor(record.Status)">
                  {{ getStatusText(record.Status) }}
                </a-tag>
              </template>
            </a-table-column>
            <a-table-column title="有效性" :width="80" align="center">
              <template #cell="{ record }">
                <a-tag bordered size="small" :color="record.Invalid === 0 ? 'green' : 'red'">
                  {{ record.Invalid === 0 ? '有效' : '无效' }}
                </a-tag>
              </template>
            </a-table-column>
            <a-table-column title="开始时间" :width="170">
              <template #cell="{ record }">
                {{ formatDateTime(record.StartTime) }}
              </template>
            </a-table-column>
            <a-table-column title="结束时间" :width="170">
              <template #cell="{ record }">
                {{ formatDateTime(record.EndTime) }}
              </template>
            </a-table-column>
            <a-table-column title="创建时间" :width="170">
              <template #cell="{ record }">
                {{ formatDateTime(record.CreateTime) }}
              </template>
            </a-table-column>
            <a-table-column title="操作" :width="180" align="center" fixed="right">
              <template #cell="{ record }">
                <a-space>
                  <a-button type="primary" size="mini" @click="onViewDetails(record)">
                    <template #icon><icon-eye /></template>
                    详情
                  </a-button>
                  <a-button
                    v-if="canRetry(record)"
                    type="primary"
                    status="warning"
                    size="mini"
                    @click="handleRetry(record)">
                    <template #icon><icon-redo /></template>
                    重试
                  </a-button>
                  <a-button
                    v-if="record.Invalid === 0"
                    type="outline"
                    status="danger"
                    size="mini"
                    @click="handleInvalidate(record)">
                    <template #icon><icon-close-circle /></template>
                    作废
                  </a-button>
                </a-space>
              </template>
            </a-table-column>
          </template>
        </a-table>
      </div>
    </a-spin>

    <!-- 详情模态框 -->
    <a-modal v-model:visible="detailVisible" :footer="false" width="900px">
      <template #title>任务实例详情</template>
      <a-descriptions :column="2" bordered>
        <a-descriptions-item label="ID">{{ detailData.ID }}</a-descriptions-item>
        <a-descriptions-item label="任务ID">{{ detailData.TaskID }}</a-descriptions-item>
        <a-descriptions-item label="规则ID">{{ detailData.RuleID }}</a-descriptions-item>
        <a-descriptions-item label="执行节点">{{ detailData.NodeID }}</a-descriptions-item>
        <a-descriptions-item label="交易标的">
          <a-tag color="arcoblue">{{ detailData.Symbol }}</a-tag>
        </a-descriptions-item>
        <a-descriptions-item label="执行状态">
          <a-tag :color="getStatusColor(detailData.Status || 0)">
            {{ getStatusText(detailData.Status || 0) }}
          </a-tag>
        </a-descriptions-item>
        <a-descriptions-item label="有效性">
          <a-tag :color="detailData.Invalid === 0 ? 'green' : 'red'">
            {{ detailData.Invalid === 0 ? '有效' : '无效' }}
          </a-tag>
        </a-descriptions-item>
        <a-descriptions-item label="开始时间">{{ formatDateTime(detailData.StartTime) }}</a-descriptions-item>
        <a-descriptions-item label="结束时间">{{ formatDateTime(detailData.EndTime) }}</a-descriptions-item>
        <a-descriptions-item label="创建时间">{{ formatDateTime(detailData.CreateTime) }}</a-descriptions-item>
        <a-descriptions-item label="修改时间">{{ formatDateTime(detailData.ModifyTime) }}</a-descriptions-item>
      </a-descriptions>

      <a-divider />

      <a-descriptions :column="1" bordered>
        <a-descriptions-item label="任务参数">
          <pre class="detail-json">{{ formatJSON(detailData.TaskParams || '{}') }}</pre>
        </a-descriptions-item>
        <a-descriptions-item label="执行结果">
          <pre class="detail-json">{{ formatJSON(detailData.Result || '{}') }}</pre>
        </a-descriptions-item>
      </a-descriptions>
    </a-modal>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, onMounted, watch } from 'vue';
import { Message, Modal } from '@arco-design/web-vue';
import service from '@/api/index';
import { useProjectStore } from '@/store/modules/project';
import { storeToRefs } from 'pinia';

interface TaskInstance {
  ID: number;
  TaskID: string;
  RuleID: string;
  NodeID: string;
  Symbol: string;
  TaskParams: string;
  Status: number;
  StartTime: string | null;
  EndTime: string | null;
  Result: string;
  Invalid: number;
  CreateTime: string;
  ModifyTime: string;
}

const loading = ref(false);
const instanceList = ref<TaskInstance[]>([]);
const selectedKeys = ref<string[]>([]);
const detailVisible = ref(false);
const detailData = ref<Partial<TaskInstance>>({});

const form = ref({
  taskId: '',
  ruleId: '',
  nodeId: '',
  symbol: '',
  status: null as number | null,
  invalid: null as number | null
});

const pagination = ref({
  current: 1,
  pageSize: 10,
  total: 0,
  showTotal: true,
  showPageSize: true
});

// Get project store
const projectStore = useProjectStore();
const { selectedProjectId } = storeToRefs(projectStore);

const paginationConfig = computed(() => ({
  ...pagination.value,
  onChange: (current: number) => {
    pagination.value.current = current;
    getInstanceList();
  },
  onPageSizeChange: (pageSize: number) => {
    pagination.value.pageSize = pageSize;
    pagination.value.current = 1;
    getInstanceList();
  }
}));

const getStatusColor = (status: number) => {
  const colors: { [key: number]: string } = {
    0: 'gray',    // 待执行
    1: 'blue',    // 执行中
    2: 'green',   // 成功
    3: 'red',     // 失败
    4: 'orange',  // 超时
    5: 'orangered' // 已取消
  };
  return colors[status] || 'gray';
};

const getStatusText = (status: number) => {
  const texts: { [key: number]: string } = {
    0: '待执行',
    1: '执行中',
    2: '成功',
    3: '失败',
    4: '超时',
    5: '已取消'
  };
  return texts[status] || '未知';
};

const formatJSON = (str: string) => {
  try {
    return JSON.stringify(JSON.parse(str || '{}'), null, 2);
  } catch {
    return str || '-';
  }
};

// 格式化时间为本地时间格式
const formatDateTime = (dateTime: string | null | undefined) => {
  if (!dateTime) return '-';

  try {
    const date = new Date(dateTime);
    // 检查日期是否有效
    if (isNaN(date.getTime())) {
      return '-';
    }

    // 格式化日期为 YYYY-MM-DD HH:mm:ss
    const year = date.getFullYear();
    const month = String(date.getMonth() + 1).padStart(2, '0');
    const day = String(date.getDate()).padStart(2, '0');
    const hours = String(date.getHours()).padStart(2, '0');
    const minutes = String(date.getMinutes()).padStart(2, '0');
    const seconds = String(date.getSeconds()).padStart(2, '0');

    return `${year}-${month}-${day} ${hours}:${minutes}:${seconds}`;
  } catch {
    return '-';
  }
};

const canRetry = (record: TaskInstance) => {
  return record.Status === 3 || record.Status === 4; // 失败或超时
};

const select = (list: string[]) => {
  selectedKeys.value = list;
};

const selectAll = (state: boolean) => {
  selectedKeys.value = state ? instanceList.value.map(el => el.TaskID) : [];
};

const onPageChange = (current: number) => {
  pagination.value.current = current;
  getInstanceList();
};

const onPageSizeChange = (pageSize: number) => {
  pagination.value.pageSize = pageSize;
  pagination.value.current = 1;
  getInstanceList();
};

const search = () => {
  pagination.value.current = 1;
  getInstanceList();
};

const reset = () => {
  form.value = {
    taskId: '',
    ruleId: '',
    nodeId: '',
    symbol: '',
    status: null,
    invalid: null
  };
  getInstanceList();
};

const refreshList = () => {
  getInstanceList();
};

const getInstanceList = async () => {
  loading.value = true;
  try {
    const params: any = {
      page: pagination.value.current,
      size: pagination.value.pageSize
    };

    // Always include the selected project ID from the global dropdown
    if (selectedProjectId.value) {
      params.project_id = selectedProjectId.value;
    }

    if (form.value.taskId) params.task_id = form.value.taskId;
    if (form.value.ruleId) params.rule_id = form.value.ruleId;
    if (form.value.nodeId) params.node_id = form.value.nodeId;
    if (form.value.symbol) params.symbol = form.value.symbol;
    if (form.value.status !== null) params.status = form.value.status;
    if (form.value.invalid !== null) params.invalid = form.value.invalid;

    const response = await service.post('/gateway/collectmgr/ListTaskInstances', params, {
      headers: {
        'app_id': 'moox_frontend',
        'app_key': '2521e0d21b6be0347b72bca93904a0dd'
      }
    });

    const data = response as any;
    if (data.code === 200) {
      instanceList.value = data.data || [];
      pagination.value.total = data.total || (data.data ? data.data.length : 0);
    } else {
      Message.error(data.message || '获取任务实例列表失败');
    }
  } catch (error) {
    console.error('获取任务实例列表失败:', error);
    Message.error('获取任务实例列表失败');
  } finally {
    loading.value = false;
  }
};

const onViewDetails = (record: TaskInstance) => {
  detailData.value = record;
  detailVisible.value = true;
};

const handleRetry = async (record: TaskInstance) => {
  Modal.confirm({
    title: '重试确认',
    content: `确定要重试任务 ${record.TaskID} 吗？`,
    onOk: async () => {
      try {
        const response = await service.post('/gateway/collectmgr/RetryTaskInstance', {
          task_id: record.TaskID
        }, {
          headers: {
            'app_id': 'moox_frontend',
            'app_key': '2521e0d21b6be0347b72bca93904a0dd'
          }
        });

        const data = response as any;
        if (data.code === 200) {
          Message.success('重试成功');
          getInstanceList();
        } else {
          Message.error(data.message || '重试失败');
        }
      } catch (error) {
        console.error('重试失败:', error);
        Message.error('重试失败');
      }
    }
  });
};

const handleInvalidate = async (record: TaskInstance) => {
  Modal.confirm({
    title: '作废确认',
    content: `确定要作废任务 ${record.TaskID} 吗？作废后任务将不再执行。`,
    onOk: async () => {
      try {
        const response = await service.post('/gateway/collectmgr/InvalidateTaskInstance', {
          task_id: record.TaskID
        }, {
          headers: {
            'app_id': 'moox_frontend',
            'app_key': '2521e0d21b6be0347b72bca93904a0dd'
          }
        });

        const data = response as any;
        if (data.code === 200) {
          Message.success('作废成功');
          getInstanceList();
        } else {
          Message.error(data.message || '作废失败');
        }
      } catch (error) {
        console.error('作废失败:', error);
        Message.error('作废失败');
      }
    }
  });
};

const batchRetry = () => {
  if (selectedKeys.value.length === 0) {
    Message.warning('请选择要重试的任务实例');
    return;
  }

  const retryableInstances = instanceList.value.filter(
    instance => selectedKeys.value.includes(instance.TaskID) && canRetry(instance)
  );

  if (retryableInstances.length === 0) {
    Message.warning('所选任务实例都不能重试（仅失败或超时的任务可重试）');
    return;
  }

  Modal.confirm({
    title: '批量重试确认',
    content: `确定要重试选中的 ${retryableInstances.length} 个失败任务吗？`,
    onOk: async () => {
      for (const instance of retryableInstances) {
        try {
          await service.post('/gateway/collectmgr/RetryTaskInstance', {
            task_id: instance.TaskID
          }, {
            headers: {
              'app_id': 'moox_frontend',
              'app_key': '2521e0d21b6be0347b72bca93904a0dd'
            }
          });
        } catch (error) {
          console.error(`重试任务 ${instance.TaskID} 失败:`, error);
        }
      }
      Message.success('批量重试完成');
      getInstanceList();
    }
  });
};

const batchInvalidate = () => {
  if (selectedKeys.value.length === 0) {
    Message.warning('请选择要作废的任务实例');
    return;
  }

  const invalidatableInstances = instanceList.value.filter(
    instance => selectedKeys.value.includes(instance.TaskID) && instance.Invalid === 0
  );

  if (invalidatableInstances.length === 0) {
    Message.warning('所选任务实例都已作废');
    return;
  }

  Modal.confirm({
    title: '批量作废确认',
    content: `确定要作废选中的 ${invalidatableInstances.length} 个任务吗？作废后任务将不再执行。`,
    onOk: async () => {
      for (const instance of invalidatableInstances) {
        try {
          await service.post('/gateway/collectmgr/InvalidateTaskInstance', {
            task_id: instance.TaskID
          }, {
            headers: {
              'app_id': 'moox_frontend',
              'app_key': '2521e0d21b6be0347b72bca93904a0dd'
            }
          });
        } catch (error) {
          console.error(`作废任务 ${instance.TaskID} 失败:`, error);
        }
      }
      Message.success('批量作废完成');
      getInstanceList();
    }
  });
};

// Watch for project changes
watch(selectedProjectId, () => {
  getInstanceList();
});

onMounted(() => {
  getInstanceList();
});
</script>

<style scoped>
.moox-page {
  padding: 16px;
  height: 100%;
}

.moox-inner {
  height: 100%;
  background: #fff;
  padding: 16px;
  border-radius: 4px;
}

.moox-inner .a-row {
  margin-top: 16px;
}

.moox-inner .a-table {
  margin-top: 16px;
}

.ellipsis-text {
  display: inline-block;
  max-width: 100%;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.params-preview {
  margin: 0;
  font-family: monospace;
  font-size: 12px;
  background: #f5f5f5;
  padding: 12px;
  border-radius: 4px;
  max-width: 400px;
  max-height: 300px;
  overflow: auto;
  white-space: pre-wrap;
  word-wrap: break-word;
}

.detail-json {
  margin: 0;
  font-family: monospace;
  font-size: 12px;
  background: #f5f5f5;
  padding: 12px;
  border-radius: 4px;
  max-height: 200px;
  overflow: auto;
  white-space: pre-wrap;
  word-wrap: break-word;
}
</style>
