<template>
  <div class="moox-page">
    <a-spin :loading="loading">
      <div class="moox-inner">
        <a-space class="cloud-node-action-bar" wrap>
          <a-button type="primary" status="danger" @click="batchDelete" :disabled="batchChangeProcessing">
            <template #icon><icon-delete /></template>
            <span>批量删除</span>
          </a-button>
          <a-button type="outline" @click="onCloudAccountManage">
            <template #icon><icon-settings /></template>
            <span>云账户管理</span>
          </a-button>
          <a-button type="outline" @click="onFunctionPackageManage">
            <template #icon><icon-code /></template>
            <span>代码包版本</span>
          </a-button>
          <a-button type="outline" @click="onSCFSyncOpen">
            <template #icon><icon-sync /></template>
            <span>同步云节点</span>
          </a-button>
        </a-space>
        <a-space class="cloud-node-filter-bar" wrap>
          <a-select v-model="form.cloudAccountId" placeholder="请选择云账户" style="width: 200px" allow-clear>
            <a-option v-for="account in cloudAccountOptions" :key="account.account_id" :value="account.account_id">
              {{ account.account_name }} ({{ getProviderName(account.provider) }})
            </a-option>
          </a-select>
          <a-input v-model="form.nodeId" placeholder="请输入节点ID" allow-clear />
          <a-select placeholder="地区" v-model="form.region" style="width: 200px" allow-clear>
            <a-option v-for="region in regionOptions" :key="region.code" :value="region.code">
              {{ region.name }}
              <a-tag
                v-if="region.tag"
                size="small"
                :color="region.tag === '国内' ? 'blue' : 'orange'"
                style="margin-left: var(--moox-space-1)"
              >
                {{ region.tag }}
              </a-tag>
            </a-option>
          </a-select>
          <a-select placeholder="节点类型" v-model="form.nodeType" style="width: 180px" allow-clear>
            <a-option value="scf-event">云函数（事件型）</a-option>
            <a-option value="scf-web">云函数（Web型）</a-option>
            <a-option value="server">服务器</a-option>
          </a-select>
          <a-button type="primary" @click="search">
            <template #icon><icon-search /></template>
            <span>查询</span>
          </a-button>
        </a-space>

        <!-- 批量变更进度提示 -->
        <a-alert
          v-if="currentBatchChangeStatus"
          type="info"
          style="margin: var(--moox-space-4) 0"
          closable
          @close="handleCloseBatchChangeAlert"
        >
          <template #title>
            <a-space>
              <icon-loading v-if="batchChangeProcessing" spin />
              <span>
                {{
                  batchChangeProcessing
                    ? "云节点批量变更处理中"
                    : batchPollingTimedOut
                      ? "已停止自动查询任务进度"
                      : "云节点批量变更已完成"
                }}
              </span>
            </a-space>
          </template>
          <div>
            <div>任务：{{ currentBatchChangeStatus.job.job_id }}</div>
            <div>变更类型：{{ getBatchChangeTypeText(currentBatchChangeStatus.job.operation) }}</div>
            <div>
              处理进度：{{ currentBatchChangeStatus.job.success_count + currentBatchChangeStatus.job.failed_count }} /
              {{ currentBatchChangeStatus.job.total_count }}
            </div>
            <div>
              成功：{{ currentBatchChangeStatus.job.success_count }}，失败：{{ currentBatchChangeStatus.job.failed_count }}
            </div>
            <a-progress
              :percent="(Number(currentBatchChangeStatus.job.progress_percent) || 0) / 100"
              :status="currentBatchChangeStatus.job.failed_count > 0 ? 'warning' : 'normal'"
              :stroke-width="8"
              style="margin-top: var(--moox-space-2)"
            />
          </div>
        </a-alert>

        <!-- 选择状态提示 -->
        <a-alert
          v-if="selectedKeys.length > 0 && !batchChangeProcessing"
          type="info"
          style="margin: var(--moox-space-4) 0"
          :closable="true"
          @close="clearDeleteSelection"
        >
          <template #title> 已选择 {{ selectedKeys.length }} 个节点 </template>
          <div style="font-size: 12px; color: #86909c">
            提示：批量操作只会对当前选中的节点生效。切换页面时会保留其他页的选择状态。
          </div>
        </a-alert>

        <CloudNodeTable :loading="loading" :selected-count="selectedKeys.length">
          <a-table
            row-key="node_id"
            size="small"
            :data="cloudNodeList"
            :bordered="{ cell: true }"
            :loading="loading"
            :scroll="{ x: '100%', y: '100%', minWidth: 1200 }"
            :pagination="paginationConfig"
            :row-selection="batchChangeProcessing ? undefined : { type: 'checkbox', showCheckedAll: true }"
            :selected-keys="selectedKeys"
            @select="select"
            @select-all="selectAll"
            @page-change="onPageChange"
            @page-size-change="onPageSizeChange"
          >
            <template #columns>
              <a-table-column title="节点ID" data-index="node_id" :width="360">
                <template #cell="{ record }">
                  <a-link class="node-id-link" @click="onViewNodeDetail(record)">{{ record.node_id }}</a-link>
                </template>
              </a-table-column>
              <a-table-column title="命名空间" data-index="namespace" :width="90"></a-table-column>
              <a-table-column title="节点类型" data-index="node_type" :width="135">
                <template #cell="{ record }">
                  <a-tag size="small" :color="getNodeTypeColor(record.node_type)">
                    {{ getNodeTypeLabel(record.node_type) }}
                  </a-tag>
                </template>
              </a-table-column>
              <a-table-column title="触发方式" data-index="trigger_type" :width="100">
                <template #cell="{ record }">{{ getTriggerTypeLabel(record.trigger_type) }}</template>
              </a-table-column>
              <a-table-column title="地区" data-index="region" :width="110">
                <template #cell="{ record }">
                  {{ getRegionName(record.region) }}
                </template>
              </a-table-column>
              <a-table-column title="标签" data-index="tag" :width="80">
                <template #cell="{ record }">
                  <a-tag v-if="record.tag" size="small" :color="record.tag === '国内' ? 'blue' : 'orange'">
                    {{ record.tag }}
                  </a-tag>
                  <span v-else>-</span>
                </template>
              </a-table-column>
              <a-table-column title="代码包版本" data-index="package_version" :width="150">
                <template #cell="{ record }">
                  <a-link
                    v-if="record.package_version && record.package_version !== '-'"
                    @click="onShowPackageDetail(record)"
                    style="cursor: pointer"
                  >
                    {{ record.package_version }}
                  </a-link>
                  <span v-else>-</span>
                </template>
              </a-table-column>
              <a-table-column title="操作" :width="72" align="center" fixed="right">
                <template #cell="{ record }">
                  <a-popconfirm
                    content="确定要删除该节点吗？删除后将无法恢复。"
                    ok-text="确定"
                    cancel-text="取消"
                    @ok="() => onDelete(record)"
                    position="tr"
                  >
                    <a-tooltip content="删除节点">
                      <a-button type="primary" size="mini" status="danger" :disabled="batchChangeProcessing">
                        <template #icon><icon-delete /></template>
                      </a-button>
                    </a-tooltip>
                  </a-popconfirm>
                </template>
              </a-table-column>
            </template>
          </a-table>
        </CloudNodeTable>
      </div>
    </a-spin>

    <!-- 云账户管理弹窗 -->
    <CloudAccountManage v-model="cloudAccountManageVisible" @refresh="loadCloudAccounts" />

    <SCFSyncDialog v-model:visible="scfSyncVisible" :accounts="cloudAccountOptions" @refresh="loadData" />

    <!-- 代码包版本管理弹窗 -->
    <FunctionPackageManage
      v-model="functionPackageManageVisible"
      mode="modal"
      :package-type="currentPackageType"
      :biz-type="currentBizType"
      @refresh="loadData"
    />

    <!-- 节点详情弹窗 -->
    <a-modal v-model:visible="nodeDetailVisible" title="云函数节点详情" :width="800" :footer="false" :mask-closable="true">
      <div v-if="selectedNodeDetail">
        <a-descriptions :column="2" bordered :label-style="{ fontWeight: 'bold', width: '140px' }">
          <a-descriptions-item label="节点ID">
            {{ selectedNodeDetail.node_id }}
          </a-descriptions-item>
          <a-descriptions-item label="云账户ID">
            {{ selectedNodeDetail.cloud_account_id }}
          </a-descriptions-item>
          <a-descriptions-item label="命名空间">
            {{ selectedNodeDetail.namespace || "-" }}
          </a-descriptions-item>
          <a-descriptions-item label="节点类型">
            <a-tag bordered size="small" :color="getNodeTypeColor(selectedNodeDetail.node_type)">
              {{ getNodeTypeLabel(selectedNodeDetail.node_type) }}
            </a-tag>
          </a-descriptions-item>
          <a-descriptions-item label="触发方式">
            {{ getTriggerTypeLabel(selectedNodeDetail.trigger_type) }}
          </a-descriptions-item>
          <template v-if="selectedNodeDetail.trigger_type === 'timer'">
            <a-descriptions-item label="Timer 状态">
              <a-tag size="small" :color="timerAvailableColor(selectedTimerMetadata.timer_available_status)">
                {{ timerAvailableLabel(selectedTimerMetadata.timer_available_status) }}
              </a-tag>
            </a-descriptions-item>
            <a-descriptions-item label="Timer Cron">
              {{ timerMetadataText("timer_cron") }}
            </a-descriptions-item>
            <a-descriptions-item label="Timer 开关">
              {{ timerEnabledLabel() }}
            </a-descriptions-item>
            <a-descriptions-item label="分配标的数">
              {{ timerMetadataText("assignment_count") }}
            </a-descriptions-item>
            <a-descriptions-item label="任务指纹">
              {{ timerMetadataText("assignment_hash") }}
            </a-descriptions-item>
            <a-descriptions-item label="DNS 指纹">
              {{ timerMetadataText("dns_hash") }}
            </a-descriptions-item>
            <a-descriptions-item label="最近协调时间" :span="2">
              {{ timerDateTimeText() }}
            </a-descriptions-item>
          </template>
          <a-descriptions-item label="地区">
            {{ getRegionName(selectedNodeDetail.region) }}
          </a-descriptions-item>
          <a-descriptions-item label="标签">
            <a-tag v-if="selectedNodeDetail.tag" size="small" :color="selectedNodeDetail.tag === '国内' ? 'blue' : 'orange'">
              {{ selectedNodeDetail.tag }}
            </a-tag>
            <span v-else>-</span>
          </a-descriptions-item>
          <a-descriptions-item label="代码包版本">
            {{ selectedNodeDetail.package_version || "-" }}
          </a-descriptions-item>
          <a-descriptions-item label="元数据" :span="2">
            <div
              v-if="selectedNodeDetail.metadata"
              style="
                max-height: 200px;
                overflow-y: auto;
                white-space: pre-wrap;
                font-family: monospace;
                background: #f6f8fa;
                padding: var(--moox-space-2);
                border-radius: 4px;
              "
            >
              {{ formatMetadata(selectedNodeDetail.metadata) }}
            </div>
            <span v-else>-</span>
          </a-descriptions-item>
          <a-descriptions-item label="创建时间">
            {{ formatDateTime(selectedNodeDetail.create_time) }}
          </a-descriptions-item>
          <a-descriptions-item label="更新时间">
            {{ formatDateTime(selectedNodeDetail.modify_time) }}
          </a-descriptions-item>
        </a-descriptions>
      </div>
    </a-modal>

    <!-- 代码包详情弹窗 -->
    <a-modal
      v-model:visible="packageDetailVisible"
      title="代码包详情"
      :width="800"
      :mask-closable="false"
      :footer="false"
      @cancel="handlePackageDetailCancel"
    >
      <div v-if="packageDetail" class="package-detail">
        <!-- 基本信息 -->
        <a-descriptions title="基本信息" :column="2" bordered size="medium" style="margin-bottom: var(--moox-space-4)">
          <a-descriptions-item label="代码包名称">{{ packageDetail.package_name }}</a-descriptions-item>
          <a-descriptions-item label="版本">{{ packageDetail.version }}</a-descriptions-item>
          <a-descriptions-item label="类型">
            <a-tag :color="getPackageTypeColor(packageDetail.package_type)">
              {{ getPackageTypeLabel(packageDetail.package_type) }}
            </a-tag>
          </a-descriptions-item>
          <a-descriptions-item label="运行时环境">{{ packageDetail.runtime }}</a-descriptions-item>
          <a-descriptions-item label="文件大小">{{ formatFileSize(packageDetail.file_size) }}</a-descriptions-item>
          <a-descriptions-item label="状态">
            <a-tag :color="getPackageStatusColor(packageDetail.status)">
              {{ getPackageStatusLabel(packageDetail.status) }}
            </a-tag>
          </a-descriptions-item>
          <a-descriptions-item label="文件MD5" :span="2">
            <a-typography-text copyable>{{ packageDetail.file_md5 || "-" }}</a-typography-text>
          </a-descriptions-item>
          <a-descriptions-item label="描述" :span="2">
            {{ packageDetail.description || "-" }}
          </a-descriptions-item>
        </a-descriptions>

        <!-- 存储信息 -->
        <a-descriptions title="存储信息" :column="2" bordered size="medium" style="margin-bottom: var(--moox-space-4)">
          <a-descriptions-item label="云账户ID">{{ packageDetail.cloud_account_id }}</a-descriptions-item>
          <a-descriptions-item label="COS区域">{{ packageDetail.cos_region }}</a-descriptions-item>
          <a-descriptions-item label="COS Bucket">{{ packageDetail.cos_bucket }}</a-descriptions-item>
          <a-descriptions-item label="COS路径">{{ packageDetail.cos_path }}</a-descriptions-item>
          <a-descriptions-item label="原始文件名" :span="2">{{ packageDetail.original_filename || "-" }}</a-descriptions-item>
        </a-descriptions>

        <!-- 审计信息 -->
        <a-descriptions title="审计信息" :column="2" bordered size="medium" style="margin-bottom: var(--moox-space-4)">
          <a-descriptions-item label="创建者">{{ packageDetail.created_by }}</a-descriptions-item>
          <a-descriptions-item label="创建时间">{{ formatDateTime(packageDetail.created_time) }}</a-descriptions-item>
          <a-descriptions-item label="最后部署时间" :span="2">
            {{ packageDetail.last_deploy_time ? formatDateTime(packageDetail.last_deploy_time) : "-" }}
          </a-descriptions-item>
        </a-descriptions>

        <div style="margin-top: var(--moox-space-4); text-align: right">
          <a-space>
            <a-button @click="handlePackageDetailCancel">关闭</a-button>
            <a-button
              type="primary"
              status="success"
              @click="onDownloadPackage(packageDetail)"
              :disabled="packageDetail.status !== 2"
              :loading="downloadProgress[packageDetail.id] !== undefined && downloadProgress[packageDetail.id] < 100"
            >
              <template #icon>
                <icon-download />
              </template>
              <span v-if="downloadProgress[packageDetail.id] !== undefined && downloadProgress[packageDetail.id] < 100">
                下载中...
              </span>
              <span v-else>下载</span>
            </a-button>
          </a-space>
        </div>
      </div>
      <div v-else style="text-align: center; padding: var(--moox-space-8)">
        <a-spin :loading="true" />
        <div style="margin-top: var(--moox-space-4)">加载中...</div>
      </div>
    </a-modal>
  </div>
</template>

<script setup lang="ts">
import { ref, reactive, computed, onMounted, onBeforeUnmount, h, watch } from "vue";
import { useRoute, useRouter } from "vue-router";
import { storeToRefs } from "pinia";
import { Message, Modal } from "@arco-design/web-vue";
import { getFunctionPackageDetail, downloadPackageByURL, type FunctionPackage } from "@/api/function-package";
import { getCloudAccountList, type CloudAccount } from "@/api/cloud-account";
import {
  batchDeleteNodes,
  getNodeBatchChange,
  getNodeList,
  listCloudRegions,
  type BatchDeleteNodesRequest,
  type GetNodeBatchChangeResponse
} from "@/api/cloud-node";
import { useSpaceStore } from "@/store/modules/space";
import { createLatestRequestGuard } from "@/utils/latest-request";
import { getNodeBatchJobId, setNodeBatchJobId } from "@/utils/cloud-node-batch-change";
import CloudAccountManage from "../cloud-account/cloud-account-manage.vue";
import FunctionPackageManage from "./function-package-manage.vue";
import SCFSyncDialog from "./scf-sync-dialog.vue";
import CloudNodeTable from "./components/cloud-node-table.vue";
import { createCloudNodeBatchPoller } from "./cloud-node-batch-poller";
import { captureDeleteSelection, updateDeleteSelection, type CloudNodeDeleteSnapshotMap } from "./delete-selection";
import {
  formatDateTime,
  formatFileSize,
  formatMetadata,
  getBatchChangeTypeText,
  getNodeTypeColor,
  getTriggerTypeLabel,
  getNodeTypeLabel,
  getPackageStatusColor,
  getPackageStatusLabel,
  getPackageTypeColor,
  getPackageTypeLabel,
  getProviderName,
  normalizeCloudNodes,
  parseMetadata,
  type CloudNode,
  type RegionInfo
} from "./cloud-node-model";

// 状态管理
const spaceStore = useSpaceStore();
const { selectedSpaceId } = storeToRefs(spaceStore);
const route = useRoute();
const router = useRouter();
const loading = ref(false);
const listGuard = createLatestRequestGuard();
const batchChangeProcessing = ref(false);
const batchPollingTimedOut = ref(false);
const currentBatchChangeStatus = ref<GetNodeBatchChangeResponse | null>(null);
const batchPoller = createCloudNodeBatchPoller(getNodeBatchChange);

const form = reactive({
  cloudAccountId: "",
  nodeId: "",
  region: "",
  nodeType: ""
});

// 数据列表
const cloudNodeList = ref<CloudNode[]>([]);
const selectedKeys = ref<string[]>([]);
const selectedNodeSnapshots = ref<CloudNodeDeleteSnapshotMap>({});
const cloudAccountOptions = ref<CloudAccount[]>([]);
const regionOptions = ref<RegionInfo[]>([]); // 地区选项
const scfSyncVisible = ref(false);
const REGION_UNLIMITED = "all";

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

// 当前页面是采集云节点管理页，代码包固定按采集器类型过滤。
const currentPackageType = computed(() => 1);

// Short-lived market-fetch SCF nodes are registered under this shared type.
const currentBizType = computed(() => "market_fetcher");

// 生命周期钩子
onMounted(async () => {
  // 根据路由设置默认的节点类型筛选条件
  form.nodeType = "";

  await spaceStore.loadSpaces();
  if (selectedSpaceId.value) {
    await loadData();
  } else {
    Message.warning("请先选择空间");
  }
  await loadCloudAccounts();
  await loadRegions(); // 加载地区列表
  const jobId = getNodeBatchJobId(route.query);
  if (jobId) {
    await startBatchPolling(jobId);
  }
});

watch(selectedSpaceId, async (spaceId, oldSpaceId) => {
  if (spaceId === oldSpaceId) return;
  // 先同步作废所有在途请求，再做任何 await：路由更新等待期间旧空间的响应也不能继续推进
  listGuard.invalidate();
  batchPoller.stop();
  currentBatchChangeStatus.value = null;
  batchChangeProcessing.value = false;
  batchPollingTimedOut.value = false;
  if (getNodeBatchJobId(route.query)) {
    await replaceRouteJobId();
  }
  // 切换空间：立即清空列表、分页与详情，避免旧空间数据残留
  loading.value = false;
  cloudNodeList.value = [];
  pagination.value.total = 0;
  selectedNodeDetail.value = null;
  pagination.value.current = 1;
  clearDeleteSelection();
  if (!spaceId) {
    cloudNodeList.value = [];
    pagination.value.total = 0;
    return;
  }
  await loadData();
});

onBeforeUnmount(() => {
  listGuard.invalidate();
  batchPoller.dispose();
  currentBatchChangeStatus.value = null;
});

const replaceRouteJobId = (jobId?: string) => router.replace({ query: setNodeBatchJobId(route.query, jobId) });

const showBatchResult = (data: GetNodeBatchChangeResponse) => {
  if (data.job.failed_count === 0) {
    Message.success(`${getBatchChangeTypeText(data.job.operation)}成功！共处理 ${data.job.total_count} 个节点`);
    return;
  }

  const failedItems = data.items.filter(item => item.status === "NODE_BATCH_ITEM_STATUS_FAILED");
  const content = () =>
    h("div", { style: { maxHeight: "400px", overflowY: "auto" } }, [
      h("div", { style: { marginBottom: "var(--moox-space-3)" } }, [
        h("div", `变更类型：${getBatchChangeTypeText(data.job.operation)}`),
        h("div", `总数量：${data.job.total_count}`),
        h("div", `成功数量：${data.job.success_count}`),
        h("div", { style: { color: "#ff4d4f" } }, `失败数量：${data.job.failed_count}`)
      ]),
      ...failedItems.map(item =>
        h(
          "div",
          {
            key: item.item_id,
            style: {
              marginBottom: "var(--moox-space-3)",
              padding: "var(--moox-space-2)",
              backgroundColor: "#fff2f0",
              borderRadius: "4px",
              border: "1px solid #ffccc7"
            }
          },
          [
            h("div", { style: { fontWeight: "bold" } }, item.node_id || item.item_id),
            h("div", { style: { color: "#ff4d4f", fontSize: "12px" } }, item.error_message || "未知错误")
          ]
        )
      )
    ]);

  Modal.error({
    title: data.job.status === "NODE_BATCH_STATUS_PARTIAL" ? "批量变更部分失败" : "批量变更失败",
    content,
    width: 700,
    maskClosable: false
  });
};

const startBatchPolling = async (jobId: string) => {
  batchChangeProcessing.value = true;
  batchPollingTimedOut.value = false;
  await batchPoller.start(jobId, {
    onUpdate: data => {
      currentBatchChangeStatus.value = data;
    },
    onTerminal: async data => {
      currentBatchChangeStatus.value = data;
      batchChangeProcessing.value = false;
      batchPollingTimedOut.value = false;
      clearDeleteSelection();
      await replaceRouteJobId();
      await loadData();
      showBatchResult(data);
    },
    onError: error => {
      console.warn("查询云节点批量变更进度失败，将继续重试:", error);
    },
    onPollingTimeout: () => {
      batchChangeProcessing.value = false;
      batchPollingTimedOut.value = true;
      Message.warning("已停止自动查询任务进度，刷新页面可继续查询");
    }
  });
};

// 关闭批量变更提示
const handleCloseBatchChangeAlert = () => {
  batchPoller.stop();
  currentBatchChangeStatus.value = null;
  batchChangeProcessing.value = false;
  batchPollingTimedOut.value = false;
  void replaceRouteJobId();
};

// 批量删除
const batchDelete = () => {
  if (selectedKeys.value.length === 0) {
    Message.warning("请选择要删除的节点");
    return;
  }

  const nodeIds = [...selectedKeys.value];
  let request: BatchDeleteNodesRequest;
  try {
    request = {
      space_id: selectedSpaceId.value ?? "",
      node_ids: nodeIds,
      node_snapshots: captureDeleteSelection(nodeIds, selectedNodeSnapshots.value)
    };
  } catch (error) {
    Message.warning(error instanceof Error ? error.message : "节点身份快照不完整，请刷新后重新选择");
    return;
  }

  Modal.warning({
    title: "批量删除确认",
    content: `确定要删除选中的 ${selectedKeys.value.length} 个节点吗？删除后将无法恢复。`,
    hideCancel: false,
    onOk: async () => {
      await executeBatchDelete(request);
    }
  });
};

// 执行批量删除
const executeBatchDelete = async (request: BatchDeleteNodesRequest) => {
  try {
    const total = request.node_ids.length;
    const rsp = await batchDeleteNodes(request);
    clearDeleteSelection();
    await loadData();
    Message.success(`已删除 ${rsp.processed_count} / ${total} 个节点`);
  } catch (error: any) {
    console.error("批量删除失败:", error);
    Message.error("批量删除失败: " + (error?.message || "未知错误"));
  }
};

// 加载数据（使用后端分页）
const loadData = async (showEmptyTip = false) => {
  if (!selectedSpaceId.value) {
    cloudNodeList.value = [];
    pagination.value.total = 0;
    return;
  }
  const requestSpaceId = selectedSpaceId.value;
  const request = listGuard.begin();
  loading.value = true;
  try {
    const response = await getNodeList({
      node_id: form.nodeId,
      cloud_account_id: form.cloudAccountId,
      region: form.region,
      node_type: form.nodeType,
      biz_type: currentBizType.value, // 根据路由添加业务类型过滤
      page: pagination.value.current,
      page_size: pagination.value.pageSize
    });

    if (!request.isLatest() || requestSpaceId !== selectedSpaceId.value) return;
    const data = response.items || [];
    cloudNodeList.value = normalizeCloudNodes(Array.isArray(data) ? data : [data].filter(Boolean));
    pagination.value.total = response.total || cloudNodeList.value.length;
    if (showEmptyTip && cloudNodeList.value.length === 0) {
      Message.info("查询结果为空");
    }
  } catch (error) {
    if (!request.isLatest()) return;
    console.error("加载数据失败:", error);
    Message.error("加载数据失败");
  } finally {
    if (request.isLatest()) loading.value = false;
  }
};

// 加载云账户列表
const loadCloudAccounts = async () => {
  try {
    const accounts = await getCloudAccountList();
    cloudAccountOptions.value = Array.isArray(accounts) ? accounts : [accounts].filter(Boolean);
  } catch (error) {
    console.error("加载云账户失败:", error);
    Message.error(error instanceof Error ? error.message : "加载云账户失败：请确认已登录且 moox-cloudnode 服务已部署");
  }
};

const onSCFSyncOpen = async () => {
  if (cloudAccountOptions.value.length === 0) {
    await loadCloudAccounts();
  }
  if (cloudAccountOptions.value.length === 0) {
    Message.warning("请先配置云账户");
    return;
  }
  scfSyncVisible.value = true;
};

// 加载地区列表
const loadRegions = async () => {
  try {
    const data = await listCloudRegions("tencent");
    regionOptions.value = Array.isArray(data) ? data : [data].filter(Boolean);
  } catch (error) {
    console.error("加载地区列表失败:", error);
    // 失败时使用空数组
    regionOptions.value = [];
  }
};

const getRegionName = (region: string) => {
  if (region === REGION_UNLIMITED) {
    return "不限";
  }
  // 从动态加载的地区列表中查找
  const regionInfo = regionOptions.value.find(r => r.code === region);
  return regionInfo ? regionInfo.name : region;
};

// 分页相关（使用后端分页）
const onPageChange = (page: number) => {
  pagination.value.current = page;
  loadData();
};

const onPageSizeChange = (pageSize: number) => {
  pagination.value.pageSize = pageSize;
  pagination.value.current = 1;
  loadData();
};

// 查询
const search = () => {
  pagination.value.current = 1;
  loadData(true);
};

// 选择处理
const select = (_rowKeys: string[], rowKey: string) => {
  const index = selectedKeys.value.indexOf(rowKey);
  if (index > -1) {
    selectedKeys.value.splice(index, 1);
    const row = cloudNodeList.value.find(node => node.node_id === rowKey);
    if (row) selectedNodeSnapshots.value = updateDeleteSelection(selectedNodeSnapshots.value, [row], false);
  } else {
    const row = cloudNodeList.value.find(node => node.node_id === rowKey);
    if (row) {
      selectedKeys.value.push(rowKey);
      selectedNodeSnapshots.value = updateDeleteSelection(selectedNodeSnapshots.value, [row], true);
    }
  }
};

const selectAll = (checked: boolean) => {
  if (checked) {
    const currentPageKeys = cloudNodeList.value.map(item => item.node_id);
    currentPageKeys.forEach(key => {
      if (!selectedKeys.value.includes(key)) {
        selectedKeys.value.push(key);
      }
    });
    selectedNodeSnapshots.value = updateDeleteSelection(selectedNodeSnapshots.value, cloudNodeList.value, true);
  } else {
    const currentPageKeys = cloudNodeList.value.map(item => item.node_id);
    selectedKeys.value = selectedKeys.value.filter(key => !currentPageKeys.includes(key));
    selectedNodeSnapshots.value = updateDeleteSelection(selectedNodeSnapshots.value, cloudNodeList.value, false);
  }
};

const clearDeleteSelection = () => {
  selectedKeys.value = [];
  selectedNodeSnapshots.value = {};
};

// 单个操作（保留原有实现）

const onDelete = async (record: CloudNode) => {
  try {
    const snapshot = updateDeleteSelection({}, [record], true);
    const rsp = await batchDeleteNodes({
      space_id: selectedSpaceId.value ?? "",
      node_ids: [record.node_id],
      node_snapshots: captureDeleteSelection([record.node_id], snapshot)
    });
    await loadData();
    Message.success(rsp.processed_count > 0 ? "删除成功" : "节点无需删除");
  } catch (error: any) {
    Message.error("删除失败: " + (error?.message || "未知错误"));
  }
};

const onViewNodeDetail = (record: CloudNode) => {
  selectedNodeDetail.value = record;
  nodeDetailVisible.value = true;
};

// 云账户管理
const cloudAccountManageVisible = ref(false);

const onCloudAccountManage = () => {
  cloudAccountManageVisible.value = true;
};

// 代码包版本管理
const functionPackageManageVisible = ref(false);

const onFunctionPackageManage = () => {
  functionPackageManageVisible.value = true;
};

// 节点详情
const nodeDetailVisible = ref(false);
const selectedNodeDetail = ref<CloudNode | null>(null);
const selectedTimerMetadata = computed(() => parseMetadata(selectedNodeDetail.value?.metadata));
const timerMetadataText = (key: string) => {
  const value = selectedTimerMetadata.value[key];
  if (value === undefined || value === null || value === "") return "-";
  return String(value);
};
const timerEnabledLabel = () => {
  const value = timerMetadataText("timer_enabled");
  if (value === "-") return "未知";
  return value === "true" ? "已开启" : "已关闭";
};
const timerDateTimeText = () => {
  const value = timerMetadataText("runtime_config_reconciled_at");
  return value === "-" ? "-" : formatDateTime(value);
};
const timerAvailableLabel = (value: unknown) => {
  const normalized = String(value || "").toLowerCase();
  if (normalized === "available") return "可用";
  if (normalized === "") return "未知";
  return String(value);
};
const timerAvailableColor = (value: unknown) => (String(value || "").toLowerCase() === "available" ? "green" : "orange");

// 代码包详情
const packageDetailVisible = ref(false);
const packageDetail = ref<FunctionPackage | null>(null);
const downloadProgress = ref<Record<string, number>>({});

// 显示代码包详情
const onShowPackageDetail = async (record: CloudNode) => {
  // 如果没有package_id，则不显示
  if (!record.package_id) {
    Message.warning("该节点未关联代码包");
    return;
  }

  packageDetail.value = null;
  packageDetailVisible.value = true;

  try {
    const detail = await getFunctionPackageDetail(record.package_id);
    if (detail) {
      packageDetail.value = detail;
    } else {
      throw new Error("获取详情失败");
    }
  } catch (error) {
    console.error("获取代码包详情失败:", error);
    Message.error({
      content: "获取代码包详情失败",
      duration: 5000
    });
    packageDetailVisible.value = false;
  }
};

// 关闭代码包详情弹窗
const handlePackageDetailCancel = () => {
  packageDetailVisible.value = false;
  packageDetail.value = null;
};

// 下载代码包
const onDownloadPackage = async (pkg: FunctionPackage) => {
  if (pkg.status !== 2) {
    Message.warning("只能下载可用状态的代码包");
    return;
  }

  try {
    downloadProgress.value[pkg.package_id] = 0;

    Message.info({
      content: "开始下载代码包...",
      duration: 2000
    });

    await downloadPackageByURL(pkg.package_id);

    downloadProgress.value[pkg.package_id] = 100;

    Message.success({
      content: "代码包下载成功",
      duration: 3000
    });

    // 3秒后清除进度
    setTimeout(() => {
      delete downloadProgress.value[pkg.package_id];
    }, 3000);
  } catch (error) {
    console.error("下载代码包失败:", error);
    delete downloadProgress.value[pkg.package_id];
    Message.error({
      content: "代码包下载失败",
      duration: 5000
    });
  }
};
</script>

<style scoped src="./cloud-node.scss"></style>
