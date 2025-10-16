<template>
  <div class="moox-page">
    <a-spin :loading="loading">
      <div class="moox-inner">
        <a-space wrap>
          <a-select v-model="form.cloudAccountId" placeholder="请选择云账户" style="width: 200px" allow-clear>
            <a-option v-for="account in cloudAccountOptions" :key="account.account_id" :value="account.account_id">
              {{ account.account_name }} ({{ getProviderName(account.provider) }})
            </a-option>
          </a-select>
          <a-input v-model="form.namespace" placeholder="请输入命名空间" allow-clear />
          <a-select placeholder="地区" v-model="form.region" style="width: 180px" allow-clear>
            <a-option value="ap-bangkok">亚太东南（曼谷）</a-option>
            <a-option value="ap-beijing">华北地区（北京）</a-option>
            <a-option value="ap-chengdu">西南地区（成都）</a-option>
            <a-option value="ap-chongqing">西南地区（重庆）</a-option>
            <a-option value="ap-guangzhou">华南地区（广州）</a-option>
            <a-option value="ap-hongkong">港澳台地区（中国香港）</a-option>
            <a-option value="ap-jakarta">亚太东南（雅加达）</a-option>
            <a-option value="ap-nanjing">华东地区（南京）</a-option>
            <a-option value="ap-seoul">亚太东北（首尔）</a-option>
            <a-option value="ap-shanghai">华东地区（上海）</a-option>
            <a-option value="ap-shanghai-fsi">华东地区（上海金融）</a-option>
            <a-option value="ap-shenzhen-fsi">华南地区（深圳金融）</a-option>
            <a-option value="ap-singapore">亚太东南（新加坡）</a-option>
            <a-option value="ap-tokyo">亚太东北（东京）</a-option>
            <a-option value="eu-frankfurt">欧洲地区（法兰克福）</a-option>
            <a-option value="na-ashburn">美国东部（弗吉尼亚）</a-option>
            <a-option value="na-siliconvalley">美国西部（硅谷）</a-option>
            <a-option value="sa-saopaulo">南美地区（圣保罗）</a-option>
          </a-select>
          <a-select placeholder="节点类型" v-model="form.nodeType" style="width: 120px" allow-clear>
            <a-option value="scf">云函数</a-option>
            <a-option value="server">服务器</a-option>
          </a-select>
          <a-select placeholder="节点状态" v-model="form.status" style="width: 120px" allow-clear>
            <a-option value="1">在线</a-option>
            <a-option value="0">离线</a-option>
            <a-option value="2">维护中</a-option>
            <a-option value="3">过载</a-option>
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
            <a-button type="primary" status="success" @click="onBatchAdd" :disabled="taskPolling">
              <template #icon><icon-plus-circle /></template>
              <span>批量新增</span>
            </a-button>
            <a-button type="primary" status="warning" @click="batchDeploy" :disabled="taskPolling">
              <template #icon><icon-upload /></template>
              <span>批量部署</span>
            </a-button>
            <a-button type="primary" status="danger" @click="batchDelete" :disabled="taskPolling">
              <template #icon><icon-delete /></template>
              <span>批量删除</span>
            </a-button>
            <a-button type="outline" @click="onCloudAccountManage">
              <template #icon><icon-settings /></template>
              <span>云账户管理</span>
            </a-button>
            <a-button type="outline" @click="onFunctionPackageManage">
              <template #icon><icon-code /></template>
              <span>云函数版本</span>
            </a-button>
          </a-space>
        </a-row>

        <!-- 任务进度提示 -->
        <a-alert
          v-if="currentTaskStatus && currentTaskStatus.task_status === 1"
          type="info"
          style="margin: 16px 0;"
          closable
          @close="handleCloseTaskAlert"
        >
          <template #title>
            <a-space>
              <icon-loading spin />
              <span>任务执行中</span>
            </a-space>
          </template>
          <div>
            <div>任务类型：{{ getTaskTypeText(currentTaskStatus.task_type) }}</div>
            <div>处理进度：{{ currentTaskStatus.success_count + currentTaskStatus.failed_count }} / {{ currentTaskStatus.total_count }}</div>
            <div>成功：{{ currentTaskStatus.success_count }}，失败：{{ currentTaskStatus.failed_count }}</div>
            <a-progress 
              :percent="(Number(currentTaskStatus.progress) || 0) / 100" 
              :status="currentTaskStatus.failed_count > 0 ? 'warning' : 'normal'"
              :stroke-width="8"
              style="margin-top: 8px"
            />
          </div>
        </a-alert>

        <!-- 选择状态提示 -->
        <a-alert
          v-if="selectedKeys.length > 0 && !taskPolling"
          type="info"
          style="margin: 16px 0;"
          :closable="true"
          @close="selectedKeys = []"
        >
          <template #title>
            已选择 {{ selectedKeys.length }} 个节点
          </template>
          <div style="font-size: 12px; color: #86909c;">
            提示：批量操作只会对当前选中的节点生效。切换页面时会保留其他页的选择状态。
          </div>
        </a-alert>

        <a-table
          row-key="node_id"
          :data="functionList"
          :bordered="{ cell: true }"
          :loading="loading"
          :scroll="{ x: '100%', y: '100%', minWidth: 1200 }"
          :pagination="paginationConfig"
          :row-selection="taskPolling ? undefined : { type: 'checkbox', showCheckedAll: true }"
          :selected-keys="selectedKeys"
          @select="select"
          @select-all="selectAll"
          @page-change="onPageChange"
          @page-size-change="onPageSizeChange"
        >
          <template #columns>
            <a-table-column title="节点ID" data-index="node_id" :width="120">
              <template #cell="{ record }">
                <a-link @click="onViewNodeDetail(record)">{{ record.node_id }}</a-link>
              </template>
            </a-table-column>
            <a-table-column title="命名空间" data-index="namespace" :width="120"></a-table-column>
            <a-table-column title="节点类型" data-index="node_type" :width="100">
              <template #cell="{ record }">
                <a-tag bordered size="small" :color="record.node_type === 'scf' ? 'blue' : 'orange'">
                  {{ record.node_type === 'scf' ? '云函数' : '服务器' }}
                </a-tag>
              </template>
            </a-table-column>
            <a-table-column title="地区" data-index="region" :width="150">
              <template #cell="{ record }">
                {{ getRegionName(record.region) }}
              </template>
            </a-table-column>
            <a-table-column title="IP地址" data-index="ip_address" :width="120"></a-table-column>
            <a-table-column title="支持的采集器" data-index="supported_collectors" :width="160">
              <template #cell="{ record }">
                <div v-if="record.supported_collectors">
                  <a-tag v-for="item in parseJSON(record.supported_collectors)" :key="item" 
                    bordered size="small" style="margin: 2px">{{ item }}</a-tag>
                </div>
              </template>
            </a-table-column>
            <a-table-column title="状态" :width="100" align="center">
              <template #cell="{ record }">
                <a-tag bordered size="small" 
                  :color="getStatusColor(record.status)">
                  {{ getStatusText(record.status) }}
                </a-tag>
              </template>
            </a-table-column>
            <a-table-column title="操作" :width="240" align="center" fixed="right">
              <template #cell="{ record }">
                <a-space>
                  <a-button v-if="record.node_type === 'scf'" type="primary" size="mini" @click="onDeploy(record)" :disabled="taskPolling">
                    <template #icon><icon-upload /></template>
                    <span>部署</span>
                  </a-button>
                  <a-button type="primary" size="mini" status="success" @click="onEdit(record)" :disabled="taskPolling">
                    <template #icon><icon-edit /></template>
                    <span>编辑</span>
                  </a-button>
                  <a-popconfirm
                    content="确定要删除该节点吗？删除后将无法恢复。"
                    ok-text="确定"
                    cancel-text="取消"
                    @ok="() => onDelete(record)"
                    position="tr"
                  >
                    <a-button 
                      type="primary" 
                      size="mini" 
                      status="danger" 
                      :disabled="taskPolling"
                    >
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
    </a-spin>

    <!-- 批量新增弹窗 -->
    <a-modal
      v-model:visible="batchAddVisible"
      title="批量新增云函数节点"
      :width="600"
      :mask-closable="false"
      @cancel="handleBatchAddCancel"
      @ok="handleBatchAddOk"
    >
      <a-form :model="batchAddForm" layout="vertical">
        <a-form-item field="cloudAccountId" label="云账户" required>
          <a-select v-model="batchAddForm.cloudAccountId" placeholder="请选择云账户" style="width: 100%">
            <a-option v-for="account in cloudAccountOptions" :key="account.account_id" :value="account.account_id">
              {{ account.account_name }} ({{ getProviderName(account.provider) }})
            </a-option>
          </a-select>
        </a-form-item>
        
        <a-form-item field="region" label="地区" required>
          <a-select v-model="batchAddForm.region" placeholder="请选择地区" style="width: 100%">
            <a-option value="ap-bangkok">亚太东南（曼谷）</a-option>
            <a-option value="ap-beijing">华北地区（北京）</a-option>
            <a-option value="ap-chengdu">西南地区（成都）</a-option>
            <a-option value="ap-chongqing">西南地区（重庆）</a-option>
            <a-option value="ap-guangzhou">华南地区（广州）</a-option>
            <a-option value="ap-hongkong">港澳台地区（中国香港）</a-option>
            <a-option value="ap-jakarta">亚太东南（雅加达）</a-option>
            <a-option value="ap-nanjing">华东地区（南京）</a-option>
            <a-option value="ap-seoul">亚太东北（首尔）</a-option>
            <a-option value="ap-shanghai">华东地区（上海）</a-option>
            <a-option value="ap-shanghai-fsi">华东地区（上海金融）</a-option>
            <a-option value="ap-shenzhen-fsi">华南地区（深圳金融）</a-option>
            <a-option value="ap-singapore">亚太东南（新加坡）</a-option>
            <a-option value="ap-tokyo">亚太东北（东京）</a-option>
            <a-option value="eu-frankfurt">欧洲地区（法兰克福）</a-option>
            <a-option value="na-ashburn">美国东部（弗吉尼亚）</a-option>
            <a-option value="na-siliconvalley">美国西部（硅谷）</a-option>
            <a-option value="sa-saopaulo">南美地区（圣保罗）</a-option>
          </a-select>
        </a-form-item>
        
        <a-form-item field="nodeCount" label="节点数量" required>
          <a-input-number 
            v-model="batchAddForm.nodeCount" 
            :min="1" 
            :max="100" 
            placeholder="请输入要创建的节点数量"
            style="width: 100%"
          />
        </a-form-item>
        
        <a-form-item field="namespace" label="命名空间">
          <a-input v-model="batchAddForm.namespace" placeholder="请输入命名空间（可选）" />
        </a-form-item>
        
        <a-form-item field="supportedCollectors" label="支持的采集器">
          <a-select v-model="batchAddForm.supportedCollectors" placeholder="请选择支持的采集器" style="width: 100%" multiple>
            <a-option value="kline">K线数据</a-option>
            <a-option value="metrics">指标数据</a-option>
            <a-option value="logs">日志数据</a-option>
            <a-option value="traces">链路数据</a-option>
          </a-select>
        </a-form-item>
      </a-form>
    </a-modal>

    <!-- 批量部署弹窗 -->
    <a-modal
      v-model:visible="batchDeployVisible"
      title="批量部署云函数"
      :width="600"
      :mask-closable="false"
      @cancel="handleBatchDeployCancel"
      @ok="handleBatchDeployOk"
    >
      <a-form :model="batchDeployForm" layout="vertical">
        <a-form-item label="部署文件" required>
          <a-upload
            :custom-request="customUploadHandler"
            :show-file-list="false"
            accept=".zip"
          >
            <a-button type="outline" :loading="deployFileLoading">
              <template #icon><icon-upload /></template>
              {{ deployFileLoading ? '读取中...' : '选择部署包' }}
            </a-button>
          </a-upload>
          <div v-if="batchDeployForm.fileName" class="file-info" style="margin-top: 8px; color: #86909c;">
            <icon-file /> 已选择文件：{{ batchDeployForm.fileName }}
          </div>
          <a-typography-text type="secondary" style="font-size: 12px; display: block; margin-top: 8px;">
            请上传 .zip 格式的部署包文件
          </a-typography-text>
        </a-form-item>
        
        <a-form-item>
          <a-alert type="info">
            <div>将为以下 {{ selectedKeys.length }} 个节点部署相同的函数包：</div>
            <div style="margin-top: 8px; max-height: 200px; overflow-y: auto;">
              <a-tag v-for="nodeId in selectedKeys" :key="nodeId" style="margin: 4px;">
                {{ nodeId }}
              </a-tag>
            </div>
          </a-alert>
        </a-form-item>
      </a-form>
    </a-modal>

    <!-- 云账户管理弹窗 -->
    <CloudAccountManage 
      v-model="cloudAccountManageVisible" 
      @refresh="loadCloudAccounts"
    />

    <!-- 云函数版本管理弹窗 -->
    <FunctionPackageManage 
      v-model="functionPackageManageVisible" 
      @refresh="loadData"
    />

    <!-- 节点详情弹窗 -->
    <a-modal
      v-model:visible="nodeDetailVisible"
      title="云函数节点详情"
      :width="800"
      :footer="false"
      :mask-closable="true"
    >
      <div v-if="selectedNodeDetail">
        <a-descriptions
          :column="2"
          bordered
          :label-style="{ fontWeight: 'bold', width: '140px' }"
        >
          <a-descriptions-item label="节点ID">
            {{ selectedNodeDetail.node_id }}
          </a-descriptions-item>
          <a-descriptions-item label="云账户ID">
            {{ selectedNodeDetail.cloud_account_id }}
          </a-descriptions-item>
          <a-descriptions-item label="命名空间">
            {{ selectedNodeDetail.namespace || '-' }}
          </a-descriptions-item>
          <a-descriptions-item label="节点类型">
            <a-tag bordered size="small" :color="selectedNodeDetail.node_type === 'scf' ? 'blue' : 'orange'">
              {{ selectedNodeDetail.node_type === 'scf' ? '云函数' : '服务器' }}
            </a-tag>
          </a-descriptions-item>
          <a-descriptions-item label="地区">
            {{ getRegionName(selectedNodeDetail.region) }}
          </a-descriptions-item>
          <a-descriptions-item label="IP地址">
            {{ selectedNodeDetail.ip_address || '-' }}
          </a-descriptions-item>
          <a-descriptions-item label="版本">
            {{ selectedNodeDetail.version || '-' }}
          </a-descriptions-item>
          <a-descriptions-item label="容量">
            {{ selectedNodeDetail.capacity || '-' }}
          </a-descriptions-item>
          <a-descriptions-item label="当前负载">
            {{ selectedNodeDetail.current_load || '-' }}
          </a-descriptions-item>
          <a-descriptions-item label="状态">
            <a-tag bordered size="small" :color="getStatusColor(selectedNodeDetail.status)">
              {{ getStatusText(selectedNodeDetail.status) }}
            </a-tag>
          </a-descriptions-item>
          <a-descriptions-item label="支持的采集器" :span="2">
            <div v-if="selectedNodeDetail.supported_collectors">
              <a-tag v-for="item in parseJSON(selectedNodeDetail.supported_collectors)" :key="item" 
                bordered size="small" style="margin: 2px">{{ item }}</a-tag>
            </div>
            <span v-else>-</span>
          </a-descriptions-item>
          <a-descriptions-item label="元数据" :span="2">
            <div v-if="selectedNodeDetail.metadata" style="max-height: 200px; overflow-y: auto; white-space: pre-wrap; font-family: monospace; background: #f6f8fa; padding: 8px; border-radius: 4px;">{{ formatMetadata(selectedNodeDetail.metadata) }}</div>
            <span v-else>-</span>
          </a-descriptions-item>
          <a-descriptions-item label="创建时间">
            {{ formatDateTime(selectedNodeDetail.created_at) }}
          </a-descriptions-item>
          <a-descriptions-item label="更新时间">
            {{ formatDateTime(selectedNodeDetail.updated_at) }}
          </a-descriptions-item>
        </a-descriptions>
      </div>
    </a-modal>

  </div>
</template>

<script setup lang="ts">
import { ref, reactive, computed, onMounted, onBeforeUnmount, h } from 'vue';
import { Message, Modal } from '@arco-design/web-vue';
import { api } from '@/api/config';
import { AsyncTaskManager, asyncTaskManager } from '@/utils/async-task';
import type { TaskStatusResponse } from '@/utils/async-task';
import CloudAccountManage from '../cloud-account/cloud-account-manage.vue';
import FunctionPackageManage from './function-package-manage.vue';

// 接口定义
interface CloudFunction {
  node_id: string;
  cloud_account_id: string;
  namespace: string;
  node_type: string;
  region: string;
  ip_address: string;
  version: string;
  supported_collectors: string;
  capacity: string;
  current_load: string;
  metadata: string;
  status: number;
  enabled: number;
  created_at: string;
  updated_at: string;
}

interface CloudAccount {
  account_id: string;
  account_name: string;
  provider: string;
  secret_id: string;
  secret_key: string;
  extra_config: string;
  status: number;
  created_at: string;
  updated_at: string;
}

// 状态管理
const loading = ref(false);
const taskPolling = ref(false);
const currentTaskStatus = ref<TaskStatusResponse | null>(null);

const form = reactive({
  cloudAccountId: '',
  namespace: '',
  region: '',
  nodeType: '',
  status: ''
});

// 数据列表
const functionList = ref<CloudFunction[]>([]);
const allFunctionList = ref<CloudFunction[]>([]);
const selectedKeys = ref<string[]>([]);
const cloudAccountOptions = ref<CloudAccount[]>([]);

// 批量新增相关
const batchAddVisible = ref(false);
const batchAddForm = reactive({
  cloudAccountId: '',
  region: 'ap-guangzhou',
  nodeCount: 5,
  namespace: '',
  supportedCollectors: ['kline'] // 默认支持kline
});

// 批量部署相关
const batchDeployVisible = ref(false);
const deployFileLoading = ref(false);
const batchDeployForm = reactive({
  fileName: '',
  fileBase64: '',
  deployConfig: {} // 可选的部署配置
});

// 分页配置
const pagination = ref({
  current: 1,
  pageSize: 10,
  total: 0,
  showTotal: true,
  showJumper: true,
  showPageSize: true,
  pageSizeOptions: [10, 20, 30, 50, 100]
});

const paginationConfig = computed(() => ({
  current: pagination.value.current,
  pageSize: pagination.value.pageSize,
  total: pagination.value.total,
  showTotal: pagination.value.showTotal,
  showJumper: pagination.value.showJumper,
  showPageSize: pagination.value.showPageSize,
  pageSizeOptions: pagination.value.pageSizeOptions
}));

// 生命周期钩子
onMounted(async () => {
  await loadData();
  await loadCloudAccounts();
  
  // 检查并恢复任务状态
  await asyncTaskManager.checkAndRestoreTask(handleTaskRestore);
});

onBeforeUnmount(() => {
  // 清理轮询
  asyncTaskManager.stopPolling();
});

// 检查任务恢复
const handleTaskRestore = (taskId: string, status: TaskStatusResponse) => {
  // 检查任务是否已完成
  if (status.task_status !== 1) { // Not PROCESSING
    // 任务已完成，直接处理结果
    handleTaskComplete(status);
  } else {
    // 任务还在处理中，继续轮询
    taskPolling.value = true;
    currentTaskStatus.value = status;
    
    asyncTaskManager.startPolling(taskId, {
      onProgress: (data) => {
        console.log('Task progress data:', {
          total_count: data.total_count,
          success_count: data.success_count,
          failed_count: data.failed_count,
          progress: data.progress,
          calculated: data.total_count > 0 ? Math.round(((data.success_count + data.failed_count) / data.total_count) * 100) : 0
        });
        currentTaskStatus.value = data;
      },
      onSuccess: (data) => {
        handleTaskComplete(data);
      },
      onFailed: (data) => {
        handleTaskComplete(data);
      },
      onPartialSuccess: (data) => {
        handleTaskComplete(data);
      },
      showLoading: false
    });
  }
};

// 任务完成处理
const handleTaskComplete = async (data: TaskStatusResponse) => {
  // 先更新状态为完成状态，让用户看到100%的进度
  currentTaskStatus.value = data;
  
  // 延迟1秒后再清理
  setTimeout(async () => {
    taskPolling.value = false;
    currentTaskStatus.value = null;
    
    // 清空选中项
    selectedKeys.value = [];
    
    // 移除URL中的任务ID
    AsyncTaskManager.removeTaskIdFromUrl();
    
    // 刷新数据
    await loadData();
  }, 1000);
  
  // 延迟显示结果弹窗，让用户先看到完成的进度
  setTimeout(() => {
    // 检查是否有失败项（通过failed_count判断）
    if (data.failed_count > 0) {
    // 有失败项，使用 Modal.error 显示失败详情
    const failedItems = data.failed_items || [];
    
    // 创建 Vue 渲染函数
    const content = () => h('div', { style: { maxHeight: '400px', overflowY: 'auto' } }, [
      h('div', { style: { marginBottom: '12px' } }, [
        h('div', `任务类型：${getTaskTypeText(data.task_type)}`),
        h('div', `总任务数：${data.total_count}`),
        h('div', `成功数：${data.success_count}`),
        h('div', { style: { color: '#ff4d4f' } }, `失败数：${data.failed_count}`)
      ]),
      failedItems.length > 0 && h('div', { style: { marginTop: '16px' } }, [
        h('strong', '失败详情：'),
        h('div', { style: { marginTop: '8px' } }, 
          failedItems.map((item: any, index: number) => 
            h('div', { 
              key: index, 
              style: { 
                marginBottom: '12px', 
                padding: '8px', 
                backgroundColor: '#fff2f0', 
                borderRadius: '4px',
                border: '1px solid #ffccc7'
              } 
            }, [
              h('div', { style: { fontWeight: 'bold', marginBottom: '4px' } }, item.item_name || item.item_id),
              h('div', { style: { color: '#ff4d4f', fontSize: '12px' } }, item.error_message || '未知错误')
            ])
          )
        )
      ])
    ]);
    
    Modal.error({
      title: '任务执行失败',
      content,
      width: 700,
      maskClosable: false
    });
    } else {
      // 全部成功，显示成功提示
      Message.success(`${getTaskTypeText(data.task_type)}成功！共处理 ${data.total_count} 个节点`);
    }
  }, 1200); // 稍微延迟比进度条消失时间长一点，避免冲突
};

// 关闭任务提示
const handleCloseTaskAlert = () => {
  currentTaskStatus.value = null;
  AsyncTaskManager.removeTaskIdFromUrl();
};

// 批量新增
const onBatchAdd = async () => {
  // 如果云账户列表为空，尝试重新加载
  if (cloudAccountOptions.value.length === 0) {
    await loadCloudAccounts();
    
    // 重新检查
    if (cloudAccountOptions.value.length === 0) {
      Message.warning('请先创建云账户');
      return;
    }
  }
  
  // 重置表单
  batchAddForm.cloudAccountId = cloudAccountOptions.value[0]?.account_id || '';
  batchAddForm.region = 'ap-guangzhou';
  batchAddForm.nodeCount = 5;
  batchAddForm.namespace = '';
  batchAddForm.supportedCollectors = ['kline'];
  
  // 显示批量新增弹窗
  batchAddVisible.value = true;
};

// 批量新增弹窗取消
const handleBatchAddCancel = () => {
  batchAddVisible.value = false;
};

// 批量新增弹窗确认
const handleBatchAddOk = async () => {
  // 表单验证
  if (!batchAddForm.cloudAccountId) {
    Message.warning('请选择云账户');
    return;
  }
  if (!batchAddForm.region) {
    Message.warning('请选择地区');
    return;
  }
  if (!batchAddForm.nodeCount || batchAddForm.nodeCount < 1) {
    Message.warning('请输入有效的节点数量');
    return;
  }
  
  // 关闭弹窗
  batchAddVisible.value = false;
  
  // 执行批量新增
  await executeBatchAdd();
};

// 执行批量新增
const executeBatchAdd = async () => {
  // 准备批量新增的数据
  const nodes = Array(batchAddForm.nodeCount).fill(null).map((_, index) => ({
    cloud_account_id: batchAddForm.cloudAccountId,
    namespace: batchAddForm.namespace || undefined,
    node_type: 'scf',
    region: batchAddForm.region,
    version: '1.0.0',
    supported_collectors: JSON.stringify(batchAddForm.supportedCollectors),
    capacity: '100',
    metadata: JSON.stringify({ env: 'prod', index })
  }));

  try {
    // 创建异步任务
    const taskId = await asyncTaskManager.createAsyncTask('BATCH_CREATE_NODE', {
      nodes
    });

    taskPolling.value = true;
    
    // 开始轮询任务状态
    asyncTaskManager.startPolling(taskId, {
      onProgress: (data) => {
        console.log('Task progress data:', {
          total_count: data.total_count,
          success_count: data.success_count,
          failed_count: data.failed_count,
          progress: data.progress,
          calculated: data.total_count > 0 ? Math.round(((data.success_count + data.failed_count) / data.total_count) * 100) : 0
        });
        currentTaskStatus.value = data;
      },
      onSuccess: (data) => {
        handleTaskComplete(data);
      },
      onFailed: (data) => {
        handleTaskComplete(data);
      },
      onPartialSuccess: (data) => {
        handleTaskComplete(data);
      },
      showLoading: false
    });
    
  } catch (error) {
    console.error('创建批量新增任务失败:', error);
  }
};

// 批量部署
const batchDeploy = () => {
  if (selectedKeys.value.length === 0) {
    Message.warning('请选择要部署的节点');
    return;
  }
  
  // 重置表单
  batchDeployForm.fileName = '';
  batchDeployForm.fileBase64 = '';
  batchDeployForm.deployConfig = {};
  deployFileLoading.value = false;
  
  // 显示批量部署弹窗
  batchDeployVisible.value = true;
};

// 批量删除
const batchDelete = () => {
  if (selectedKeys.value.length === 0) {
    Message.warning('请选择要删除的节点');
    return;
  }
  
  Modal.warning({
    title: '批量删除确认',
    content: `确定要删除选中的 ${selectedKeys.value.length} 个节点吗？删除后将无法恢复。`,
    hideCancel: false,
    onOk: async () => {
      await executeBatchDelete();
    }
  });
};

// 执行批量删除
const executeBatchDelete = async () => {
  // 后端期望的是字符串数组，不是对象数组
  const nodes = selectedKeys.value;

  try {
    // 创建异步任务
    const taskId = await asyncTaskManager.createAsyncTask('BATCH_DELETE_NODE', {
      nodes
    });

    taskPolling.value = true;
    
    // 开始轮询任务状态
    asyncTaskManager.startPolling(taskId, {
      onProgress: (data) => {
        console.log('Task progress data:', {
          total_count: data.total_count,
          success_count: data.success_count,
          failed_count: data.failed_count,
          progress: data.progress,
          calculated: data.total_count > 0 ? Math.round(((data.success_count + data.failed_count) / data.total_count) * 100) : 0
        });
        currentTaskStatus.value = data;
      },
      onSuccess: (data) => {
        handleTaskComplete(data);
      },
      onFailed: (data) => {
        handleTaskComplete(data);
      },
      onPartialSuccess: (data) => {
        handleTaskComplete(data);
      },
      showLoading: false
    });
    
  } catch (error) {
    console.error('创建批量删除任务失败:', error);
  }
};

// 加载数据
const loadData = async () => {
  loading.value = true;
  try {
    const response = await api.post('/collector/GetNodeList', {
      cloud_account_id: form.cloudAccountId,
      namespace: form.namespace,
      region: form.region,
      node_type: form.nodeType,
      status: form.status
    });
    
    // 兼容两种响应格式
    if (response.data?.code === 200 && response.data?.data) {
      // 新格式：处理数组格式的响应
      let data = response.data.data;
      if (Array.isArray(data)) {
        allFunctionList.value = data;
      } else {
        allFunctionList.value = [data].filter(Boolean);
      }
      pagination.value.total = allFunctionList.value.length;
      updateCurrentPageData();
    } else if (response.data?.ret_info?.code === 0) {
      // 旧格式
      let data = response.data.ret_info.data;
      if (Array.isArray(data)) {
        allFunctionList.value = data;
      } else {
        allFunctionList.value = [data].filter(Boolean);
      }
      pagination.value.total = allFunctionList.value.length;
      updateCurrentPageData();
    }
  } catch (error) {
    console.error('加载数据失败:', error);
    Message.error('加载数据失败');
  } finally {
    loading.value = false;
  }
};

// 加载云账户列表
const loadCloudAccounts = async () => {
  try {
    const response = await api.post('/collector/ListCloudAccounts', {});
    
    // 兼容两种响应格式
    if (response.data?.code === 200 && response.data?.data) {
      // 新格式：处理数组格式的响应
      let data = response.data.data;
      if (Array.isArray(data)) {
        cloudAccountOptions.value = data;
      } else {
        cloudAccountOptions.value = [data].filter(Boolean);
      }
    } else if (response.data?.ret_info?.code === 0) {
      // 旧格式：ret_info 包装
      let data = response.data.ret_info.data;
      if (Array.isArray(data)) {
        cloudAccountOptions.value = data;
      } else {
        cloudAccountOptions.value = [data].filter(Boolean);
      }
    } else {
      Message.error('加载云账户失败');
    }
  } catch (error) {
    console.error('加载云账户失败:', error);
    Message.error('加载云账户失败，请检查网络连接');
  }
};

// 工具函数
const getTaskTypeText = (taskType: string) => {
  const typeMap: Record<string, string> = {
    'BATCH_CREATE_NODE': '批量创建节点',
    'BATCH_UPDATE_NODE': '批量更新节点',
    'BATCH_DELETE_NODE': '批量删除节点',
    'BATCH_DEPLOY_NODE': '批量部署节点'
  };
  return typeMap[taskType] || taskType;
};

const getProviderName = (provider: string) => {
  const providerMap: Record<string, string> = {
    'tencent': '腾讯云',
    'aliyun': '阿里云',
    'aws': 'AWS'
  };
  return providerMap[provider] || provider;
};

const getRegionName = (region: string) => {
  const regionMap: Record<string, string> = {
    'ap-bangkok': '亚太东南（曼谷）',
    'ap-beijing': '华北地区（北京）',
    'ap-chengdu': '西南地区（成都）',
    'ap-chongqing': '西南地区（重庆）',
    'ap-guangzhou': '华南地区（广州）',
    'ap-hongkong': '港澳台地区（中国香港）',
    // ... 其他地区映射
  };
  return regionMap[region] || region;
};

const getStatusColor = (status: number) => {
  const colorMap: Record<number, string> = {
    0: 'red',
    1: 'green',
    2: 'orange',
    3: 'red'
  };
  return colorMap[status] || 'gray';
};

const getStatusText = (status: number) => {
  const textMap: Record<number, string> = {
    0: '离线',
    1: '在线',
    2: '维护中',
    3: '过载'
  };
  return textMap[status] || '未知';
};

const parseJSON = (str: string) => {
  try {
    return JSON.parse(str);
  } catch {
    return [];
  }
};

const formatDateTime = (dateTime: string) => {
  if (!dateTime) return '-';
  try {
    return new Date(dateTime).toLocaleString('zh-CN', {
      year: 'numeric',
      month: '2-digit',
      day: '2-digit',
      hour: '2-digit',
      minute: '2-digit',
      second: '2-digit'
    });
  } catch {
    return dateTime;
  }
};

const formatMetadata = (metadata: string) => {
  if (!metadata) return '-';
  try {
    const parsed = JSON.parse(metadata);
    return JSON.stringify(parsed, null, 2);
  } catch {
    return metadata;
  }
};

// 分页相关
const updateCurrentPageData = () => {
  const startIndex = (pagination.value.current - 1) * pagination.value.pageSize;
  const endIndex = startIndex + pagination.value.pageSize;
  functionList.value = allFunctionList.value.slice(startIndex, endIndex);
};

const onPageChange = (page: number) => {
  pagination.value.current = page;
  updateCurrentPageData();
};

const onPageSizeChange = (pageSize: number) => {
  pagination.value.pageSize = pageSize;
  pagination.value.current = 1;
  updateCurrentPageData();
};

// 查询和重置
const search = () => {
  pagination.value.current = 1;
  loadData();
};

const reset = () => {
  form.cloudAccountId = '';
  form.namespace = '';
  form.region = '';
  form.nodeType = '';
  form.status = '';
  search();
};

// 选择处理
const select = (_rowKeys: string[], rowKey: string) => {
  const index = selectedKeys.value.indexOf(rowKey);
  if (index > -1) {
    selectedKeys.value.splice(index, 1);
  } else {
    selectedKeys.value.push(rowKey);
  }
};

const selectAll = (checked: boolean) => {
  if (checked) {
    const currentPageKeys = functionList.value.map(item => item.node_id);
    currentPageKeys.forEach(key => {
      if (!selectedKeys.value.includes(key)) {
        selectedKeys.value.push(key);
      }
    });
  } else {
    const currentPageKeys = functionList.value.map(item => item.node_id);
    selectedKeys.value = selectedKeys.value.filter(key => !currentPageKeys.includes(key));
  }
};

// 单个操作（保留原有实现）

const onEdit = (_record: CloudFunction) => {
  Message.info('编辑功能开发中...');
};

const onDelete = async (record: CloudFunction) => {
  try {
    // 创建单个删除的异步任务
    const requestData = {
      nodes: [record.node_id]
    };
    
    const taskId = await asyncTaskManager.createAsyncTask('BATCH_DELETE_NODE', requestData);

    taskPolling.value = true;
    
    // 开始轮询任务状态
    asyncTaskManager.startPolling(taskId, {
      onProgress: (data) => {
        console.log('Task progress data:', {
          total_count: data.total_count,
          success_count: data.success_count,
          failed_count: data.failed_count,
          progress: data.progress,
          calculated: data.total_count > 0 ? Math.round(((data.success_count + data.failed_count) / data.total_count) * 100) : 0
        });
        currentTaskStatus.value = data;
      },
      onSuccess: (data) => {
        handleTaskComplete(data);
      },
      onFailed: (data) => {
        handleTaskComplete(data);
      },
      onPartialSuccess: (data) => {
        handleTaskComplete(data);
      },
      showLoading: false
    });
  } catch (error: any) {
    Message.error('删除失败: ' + (error?.message || '未知错误'));
  }
};

const onDeploy = (_record: CloudFunction) => {
  Message.info('部署功能开发中...');
};

const onViewNodeDetail = (record: CloudFunction) => {
  selectedNodeDetail.value = record;
  nodeDetailVisible.value = true;
};

// 云账户管理
const cloudAccountManageVisible = ref(false);

const onCloudAccountManage = () => {
  cloudAccountManageVisible.value = true;
};

// 云函数版本管理
const functionPackageManageVisible = ref(false);

const onFunctionPackageManage = () => {
  functionPackageManageVisible.value = true;
};

// 节点详情
const nodeDetailVisible = ref(false);
const selectedNodeDetail = ref<CloudFunction | null>(null);

// 自定义上传处理器
const customUploadHandler = (option: any) => {
  const { fileItem } = option;
  const file = fileItem.file;
  
  if (!file) {
    Message.error('文件不存在');
    option.onError();
    return;
  }
  
  // 文件大小限制检查 (300MB)
  const maxSize = 300 * 1024 * 1024;
  if (file.size > maxSize) {
    Message.error('文件大小不能超过 300MB');
    option.onError();
    return;
  }
  
  // 先设置文件名，让用户看到已选择文件
  batchDeployForm.fileName = file.name;
  deployFileLoading.value = true;
  
  // 使用 FileReader 读取文件并转换为 base64
  const reader = new FileReader();
  reader.onload = (e) => {
    try {
      const result = e.target?.result as string;
      // 移除 data:application/zip;base64, 前缀，只保留 base64 字符串
      const base64String = result.split(',')[1];
      batchDeployForm.fileBase64 = base64String;
      deployFileLoading.value = false;
      Message.success(`已选择文件: ${file.name}`);
      option.onSuccess();
    } catch (error) {
      console.error('文件读取失败:', error);
      Message.error('文件读取失败');
      batchDeployForm.fileName = '';
      batchDeployForm.fileBase64 = '';
      deployFileLoading.value = false;
      option.onError();
    }
  };
  
  reader.onerror = () => {
    Message.error('文件读取失败');
    batchDeployForm.fileName = '';
    batchDeployForm.fileBase64 = '';
    deployFileLoading.value = false;
    option.onError();
  };
  
  reader.readAsDataURL(file);
  
  return {
    abort: () => {
      reader.abort();
      console.log('上传中止');
    }
  };
};

// 批量部署弹窗取消
const handleBatchDeployCancel = () => {
  batchDeployVisible.value = false;
  // 清理表单
  batchDeployForm.fileName = '';
  batchDeployForm.fileBase64 = '';
  batchDeployForm.deployConfig = {};
  deployFileLoading.value = false;
};

// 批量部署弹窗确认
const handleBatchDeployOk = async () => {
  // 表单验证
  if (!batchDeployForm.fileBase64 || !batchDeployForm.fileName) {
    Message.warning('请选择部署文件');
    return;
  }
  
  // 关闭弹窗
  batchDeployVisible.value = false;
  
  // 执行批量部署
  await executeBatchDeploy();
};

// 执行批量部署
const executeBatchDeploy = async () => {
  try {
    // 构建批量部署请求数据
    const deployData = {
      nodes: selectedKeys.value.map(nodeId => ({
        node_id: nodeId,
        zip_file_base64: batchDeployForm.fileBase64,
        file_name: batchDeployForm.fileName
      }))
    };
    
    // 创建异步任务
    const taskId = await asyncTaskManager.createAsyncTask('BATCH_DEPLOY_NODE', deployData);
    
    taskPolling.value = true;
    
    // 开始轮询任务状态
    asyncTaskManager.startPolling(taskId, {
      onProgress: (data) => {
        console.log('Task progress data:', {
          total_count: data.total_count,
          success_count: data.success_count,
          failed_count: data.failed_count,
          progress: data.progress,
          calculated: data.total_count > 0 ? Math.round(((data.success_count + data.failed_count) / data.total_count) * 100) : 0
        });
        currentTaskStatus.value = data;
      },
      onSuccess: (data) => {
        handleTaskComplete(data);
        Message.success('批量部署完成');
      },
      onFailed: (data) => {
        handleTaskComplete(data);
      },
      onPartialSuccess: (data) => {
        handleTaskComplete(data);
      },
      showLoading: false
    });
    
    // 清理表单
    batchDeployForm.fileName = '';
    batchDeployForm.fileBase64 = '';
    batchDeployForm.deployConfig = {};
    
  } catch (error: any) {
    console.error('创建批量部署任务失败:', error);
    Message.error('创建批量部署任务失败: ' + (error?.message || '未知错误'));
  }
};

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
</style>