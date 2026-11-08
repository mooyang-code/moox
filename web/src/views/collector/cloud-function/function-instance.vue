<template>
  <div class="moox-page">
    <a-spin :loading="loading">
      <div class="moox-inner">
        <a-space class="cloud-function-action-bar" wrap>
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
            <span>同步云函数实例</span>
          </a-button>
        </a-space>
        <a-space class="cloud-function-filter-bar" wrap>
          <a-select v-model="form.cloudAccountId" placeholder="请选择云账户" style="width: 200px" allow-clear @change="search">
            <a-option v-for="account in cloudAccountOptions" :key="account.account_id" :value="account.account_id">
              {{ account.account_name }} ({{ getProviderName(account.provider) }})
            </a-option>
          </a-select>
          <a-input
            v-model="form.instanceId"
            placeholder="请输入实例ID，回车查询"
            allow-clear
            @press-enter="search"
            @clear="search"
          />
          <a-select placeholder="地区" v-model="form.region" style="width: 200px" allow-clear @change="search">
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
          <a-select placeholder="实例类型" v-model="form.functionType" style="width: 180px" allow-clear @change="search">
            <a-option value="scf-event">云函数（事件型）</a-option>
            <a-option value="scf-web">云函数（Web型）</a-option>
            <a-option value="server">服务器</a-option>
          </a-select>
        </a-space>

        <!-- 选择状态提示 -->
        <a-alert
          v-if="selectedKeys.length > 0"
          type="info"
          style="margin: var(--moox-space-4) 0"
          :closable="true"
          @close="clearDeleteSelection"
        >
          <template #title> 已选择 {{ selectedKeys.length }} 个实例 </template>
          <div style="font-size: 12px; color: #86909c">
            提示：批量操作只会对当前选中的实例生效。切换页面时会保留其他页的选择状态。
          </div>
          <template #action>
            <a-button size="small" status="danger" @click="batchDelete">删除已选实例</a-button>
          </template>
        </a-alert>

        <div class="function-instance-table">
          <a-table
            row-key="instance_id"
            size="small"
            :data="cloudFunctionList"
            :bordered="{ cell: true }"
            :loading="loading"
            :scroll="{ x: '100%', y: '100%', minWidth: 1000 }"
            :pagination="paginationConfig"
            :row-selection="{ type: 'checkbox', showCheckedAll: true }"
            :selected-keys="selectedKeys"
            @select="select"
            @select-all="selectAll"
            @page-change="onPageChange"
            @page-size-change="onPageSizeChange"
          >
            <template #columns>
              <a-table-column title="实例ID" data-index="instance_id" :width="380">
                <template #cell="{ record }">
                  <a-link class="instance-id-link" @click="onViewInstanceDetail(record)">{{ record.instance_id }}</a-link>
                </template>
              </a-table-column>
              <a-table-column title="命名空间" data-index="namespace" :width="140"></a-table-column>
              <a-table-column title="实例类型" data-index="function_type" :width="122">
                <template #cell="{ record }">
                  <a-tag size="small" :color="getFunctionTypeColor(record.function_type)">
                    {{ getFunctionTypeLabel(record.function_type) }}
                  </a-tag>
                </template>
              </a-table-column>
              <a-table-column title="触发方式" data-index="trigger_type" :width="104">
                <template #cell="{ record }">
                  <span class="instance-trigger">
                    {{ getTriggerTypeLabel(record.trigger_type) }}
                    <a-tag
                      v-if="record.trigger_type === 'timer'"
                      size="small"
                      :color="getTimerAvailableColor(parseMetadata(record.metadata).timer_available_status)"
                    >
                      {{ getTimerAvailableLabel(parseMetadata(record.metadata).timer_available_status) }}
                    </a-tag>
                  </span>
                </template>
              </a-table-column>
              <a-table-column title="地区" data-index="region" :width="120">
                <template #cell="{ record }">
                  {{ getRegionName(record.region) }}
                  <a-tag
                    v-if="getRegionTag(record.region)"
                    size="small"
                    :color="getRegionTag(record.region) === '国内' ? 'blue' : 'orange'"
                  >
                    {{ getRegionTag(record.region) }}
                  </a-tag>
                </template>
              </a-table-column>
              <a-table-column title="代码包版本" data-index="package_version" :width="100">
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
              <a-table-column title="操作" :width="56" align="center" fixed="right">
                <template #cell="{ record }">
                  <a-popconfirm
                    :content="deleteInstanceText(record)"
                    ok-text="删除"
                    :ok-button-props="{ status: 'danger' }"
                    cancel-text="取消"
                    @ok="() => onDelete(record)"
                    position="tr"
                  >
                    <a-tooltip content="删除实例">
                      <a-button type="primary" size="mini" status="danger">
                        <template #icon><icon-delete /></template>
                      </a-button>
                    </a-tooltip>
                  </a-popconfirm>
                </template>
              </a-table-column>
            </template>
          </a-table>
        </div>
      </div>
    </a-spin>

    <!-- 云账户管理弹窗 -->
    <CloudAccountManage v-model="cloudAccountManageVisible" @refresh="loadCloudAccounts" />

    <SCFSyncDialog v-model:visible="scfSyncVisible" :accounts="cloudAccountOptions" @refresh="loadData" />

    <!-- 代码包版本管理弹窗 -->
    <FunctionPackageManage
      v-model="functionPackageManageVisible"
      :package-type="currentPackageType"
      :workload-type="currentWorkloadType"
      @refresh="loadData"
    />

    <!-- 实例详情抽屉（新做的详情一律用抽屉） -->
    <a-drawer v-model:visible="instanceDetailVisible" title="云函数实例详情" width="min(800px, 100vw)" :footer="false">
      <div v-if="selectedInstanceDetail">
        <a-descriptions :column="2" bordered :label-style="{ fontWeight: 'bold', width: '140px' }">
          <a-descriptions-item label="实例ID">
            {{ selectedInstanceDetail.instance_id }}
          </a-descriptions-item>
          <a-descriptions-item label="云账户ID">
            {{ selectedInstanceDetail.cloud_account_id }}
          </a-descriptions-item>
          <a-descriptions-item label="命名空间">
            {{ selectedInstanceDetail.namespace || "-" }}
          </a-descriptions-item>
          <a-descriptions-item label="实例类型">
            <a-tag bordered size="small" :color="getFunctionTypeColor(selectedInstanceDetail.function_type)">
              {{ getFunctionTypeLabel(selectedInstanceDetail.function_type) }}
            </a-tag>
          </a-descriptions-item>
          <a-descriptions-item label="触发方式">
            {{ getTriggerTypeLabel(selectedInstanceDetail.trigger_type) }}
          </a-descriptions-item>
          <template v-if="selectedInstanceDetail.trigger_type === 'timer'">
            <a-descriptions-item label="Timer 状态">
              <a-tag size="small" :color="getTimerAvailableColor(selectedTimerMetadata.timer_available_status)">
                {{ getTimerAvailableLabel(selectedTimerMetadata.timer_available_status) }}
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
            {{ getRegionName(selectedInstanceDetail.region) }}
          </a-descriptions-item>
          <a-descriptions-item label="标签">
            <a-tag
              v-if="getRegionTag(selectedInstanceDetail.region)"
              size="small"
              :color="getRegionTag(selectedInstanceDetail.region) === '国内' ? 'blue' : 'orange'"
            >
              {{ getRegionTag(selectedInstanceDetail.region) }}
            </a-tag>
            <span v-else>-</span>
          </a-descriptions-item>
          <a-descriptions-item label="代码包版本">
            {{ selectedInstanceDetail.package_version || "-" }}
          </a-descriptions-item>
          <a-descriptions-item label="元数据" :span="2">
            <div
              v-if="selectedInstanceDetail.metadata"
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
              {{ formatMetadata(selectedInstanceDetail.metadata) }}
            </div>
            <span v-else>-</span>
          </a-descriptions-item>
          <a-descriptions-item label="创建时间">
            {{ formatDateTime(selectedInstanceDetail.create_time, displayZone) }}
          </a-descriptions-item>
          <a-descriptions-item label="更新时间">
            {{ formatDateTime(selectedInstanceDetail.modify_time, displayZone) }}
          </a-descriptions-item>
        </a-descriptions>
      </div>
    </a-drawer>

    <!-- 代码包详情弹窗 -->
    <FunctionPackageDetail v-model:visible="packageDetailVisible" :package-id="packageDetailId" />
  </div>
</template>

<script setup lang="ts">
import { useDisplayZone } from "@/hooks/useDisplayZone";
import { ref, reactive, computed, onMounted, onBeforeUnmount, watch } from "vue";
import { storeToRefs } from "pinia";
import { Message, Modal } from "@arco-design/web-vue";
import { getCloudAccountList, type CloudAccount } from "@/api/cloud-account";
import {
  deleteFunctionInstances,
  listFunctionInstances,
  listCloudRegions,
  type SubmitDeleteFunctionInstancesRequest
} from "@/api/function-instance";
import { useSpaceStore } from "@/store/modules/space";
import { createLatestRequestGuard } from "@/utils/latest-request";
import CloudAccountManage from "../cloud-account/cloud-account-manage.vue";
import FunctionPackageManage from "./function-package-manage.vue";
import SCFSyncDialog from "./scf-sync-dialog.vue";
import FunctionPackageDetail from "./function-package-detail.vue";
import { captureDeleteSelection, updateDeleteSelection, type FunctionInstanceDeleteSnapshotMap } from "./delete-selection";
import {
  formatDateTime,
  formatMetadata,
  getFunctionTypeColor,
  getTriggerTypeLabel,
  getTimerAvailableColor,
  getTimerAvailableLabel,
  getFunctionTypeLabel,
  getProviderName,
  normalizeFunctionInstances,
  parseMetadata,
  type FunctionInstance,
  type RegionInfo
} from "./function-instance-model";

// 状态管理
// 页面时间按当前空间登记的时区显示
const displayZone = useDisplayZone();
const spaceStore = useSpaceStore();
const { selectedSpaceId } = storeToRefs(spaceStore);
const loading = ref(false);
const listGuard = createLatestRequestGuard();

const form = reactive({
  cloudAccountId: "",
  instanceId: "",
  region: "",
  functionType: ""
});

// 数据列表
const cloudFunctionList = ref<FunctionInstance[]>([]);
const selectedKeys = ref<string[]>([]);
const selectedInstanceSnapshots = ref<FunctionInstanceDeleteSnapshotMap>({});
const cloudAccountOptions = ref<CloudAccount[]>([]);
const regionOptions = ref<RegionInfo[]>([]);
const getRegionTag = (region: string) => regionOptions.value.find(item => item.code === region)?.tag || ""; // 地区选项
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

// 当前页面是采集云函数实例管理页，代码包固定按采集器类型过滤。
const currentPackageType = computed(() => 1);

// Short-lived market-fetch SCF instances are registered under this shared type.
const currentWorkloadType = computed(() => "market_fetcher");

// 生命周期钩子
onMounted(async () => {
  // 根据路由设置默认的实例类型筛选条件
  form.functionType = "";

  await spaceStore.loadSpaces();
  if (selectedSpaceId.value) {
    await loadData();
  } else {
    Message.warning("请先选择空间");
  }
  await loadCloudAccounts();
  await loadRegions(); // 加载地区列表
});

watch(selectedSpaceId, async (spaceId, oldSpaceId) => {
  // 换空间时旧空间的代码包详情请求会被取消，弹窗一并关掉，不然会一直停在加载中
  packageDetailVisible.value = false;
  if (spaceId === oldSpaceId) return;
  // 先作废所有在途请求，旧空间的响应不能再写回列表
  listGuard.invalidate();
  // 切换空间：立即清空列表、分页与详情，避免旧空间数据残留
  loading.value = false;
  cloudFunctionList.value = [];
  pagination.value.total = 0;
  selectedInstanceDetail.value = null;
  pagination.value.current = 1;
  clearDeleteSelection();
  if (!spaceId) {
    cloudFunctionList.value = [];
    pagination.value.total = 0;
    return;
  }
  await loadData();
});

// 删除要续租并等后端任务结束：离开页面时中止这两个循环，不在后台一直跑完
const deleteAbort = new AbortController();
onBeforeUnmount(() => {
  deleteAbort.abort();
  listGuard.invalidate();
});

// 批量删除
const batchDelete = () => {
  if (selectedKeys.value.length === 0) {
    Message.warning("请选择要删除的实例");
    return;
  }

  const instanceIds = [...selectedKeys.value];
  let request: SubmitDeleteFunctionInstancesRequest;
  try {
    request = {
      space_id: selectedSpaceId.value ?? "",
      instance_ids: instanceIds,
      instance_snapshots: captureDeleteSelection(instanceIds, selectedInstanceSnapshots.value)
    };
  } catch (error) {
    Message.warning(error instanceof Error ? error.message : "实例身份快照不完整，请刷新后重新选择");
    return;
  }

  Modal.warning({
    title: "批量删除确认",
    content: `删除选中的 ${selectedKeys.value.length} 个实例：会同时删掉云端的云函数并清掉登记，删除后无法恢复。`,
    hideCancel: false,
    onOk: async () => {
      await executeBatchDelete(request);
    }
  });
};

// 执行批量删除
const executeBatchDelete = async (request: SubmitDeleteFunctionInstancesRequest) => {
  try {
    const total = request.instance_ids.length;
    const rsp = await deleteFunctionInstances(request, { signal: deleteAbort.signal });
    clearDeleteSelection();
    await loadData();
    Message.success(`已删除 ${rsp.processed_count} / ${total} 个实例`);
  } catch (error: any) {
    if (deleteAbort.signal.aborted) return;
    console.error("批量删除失败:", error);
    Message.error("批量删除失败: " + (error?.message || "未知错误"));
  }
};

// 加载数据（使用后端分页）
const loadData = async (showEmptyTip = false) => {
  if (!selectedSpaceId.value) {
    cloudFunctionList.value = [];
    pagination.value.total = 0;
    return;
  }
  const requestSpaceId = selectedSpaceId.value;
  const request = listGuard.begin();
  loading.value = true;
  try {
    const response = await listFunctionInstances({
      instance_id: form.instanceId,
      cloud_account_id: form.cloudAccountId,
      region: form.region,
      function_type: form.functionType,
      workload_type: currentWorkloadType.value, // 根据路由添加业务类型过滤
      page: pagination.value.current,
      page_size: pagination.value.pageSize
    });

    if (!request.isLatest() || requestSpaceId !== selectedSpaceId.value) return;
    const data = response.items || [];
    cloudFunctionList.value = normalizeFunctionInstances(Array.isArray(data) ? data : [data].filter(Boolean));
    pagination.value.total = response.total || cloudFunctionList.value.length;
    if (showEmptyTip && cloudFunctionList.value.length === 0) {
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
    Message.error(error instanceof Error ? error.message : "加载云账户失败：请确认已登录且 moox-cloudfunction 服务已部署");
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
    const row = cloudFunctionList.value.find(instance => instance.instance_id === rowKey);
    if (row) selectedInstanceSnapshots.value = updateDeleteSelection(selectedInstanceSnapshots.value, [row], false);
  } else {
    const row = cloudFunctionList.value.find(instance => instance.instance_id === rowKey);
    if (row) {
      selectedKeys.value.push(rowKey);
      selectedInstanceSnapshots.value = updateDeleteSelection(selectedInstanceSnapshots.value, [row], true);
    }
  }
};

const selectAll = (checked: boolean) => {
  if (checked) {
    const currentPageKeys = cloudFunctionList.value.map(item => item.instance_id);
    currentPageKeys.forEach(key => {
      if (!selectedKeys.value.includes(key)) {
        selectedKeys.value.push(key);
      }
    });
    selectedInstanceSnapshots.value = updateDeleteSelection(selectedInstanceSnapshots.value, cloudFunctionList.value, true);
  } else {
    const currentPageKeys = cloudFunctionList.value.map(item => item.instance_id);
    selectedKeys.value = selectedKeys.value.filter(key => !currentPageKeys.includes(key));
    selectedInstanceSnapshots.value = updateDeleteSelection(selectedInstanceSnapshots.value, cloudFunctionList.value, false);
  }
};

const clearDeleteSelection = () => {
  selectedKeys.value = [];
  selectedInstanceSnapshots.value = {};
};

// 单个操作（保留原有实现）

/** 单个删除的说明：删除会连云端的云函数一起删（后端 SubmitDeleteFunctionInstances），不只是清登记。 */
function deleteInstanceText(record: FunctionInstance) {
  const where = record.region ? `（${record.region}）` : "";
  return `删除实例「${record.instance_id}」${where}？会同时删掉云端的云函数并清掉登记，删除后无法恢复。`;
}

const onDelete = async (record: FunctionInstance) => {
  try {
    const snapshot = updateDeleteSelection({}, [record], true);
    const rsp = await deleteFunctionInstances(
      {
        space_id: selectedSpaceId.value ?? "",
        instance_ids: [record.instance_id],
        instance_snapshots: captureDeleteSelection([record.instance_id], snapshot)
      },
      { signal: deleteAbort.signal }
    );
    await loadData();
    Message.success(rsp.processed_count > 0 ? "删除成功" : "实例无需删除");
  } catch (error: any) {
    if (deleteAbort.signal.aborted) return;
    Message.error("删除失败: " + (error?.message || "未知错误"));
  }
};

const onViewInstanceDetail = (record: FunctionInstance) => {
  selectedInstanceDetail.value = record;
  instanceDetailVisible.value = true;
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

// 实例详情
const instanceDetailVisible = ref(false);
const selectedInstanceDetail = ref<FunctionInstance | null>(null);
const selectedTimerMetadata = computed(() => parseMetadata(selectedInstanceDetail.value?.metadata));
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
  return value === "-" ? "-" : formatDateTime(value, displayZone.value);
};

// 代码包详情
const packageDetailVisible = ref(false);
const packageDetailId = ref("");

const onShowPackageDetail = (record: FunctionInstance) => {
  if (!record.package_id) {
    Message.warning("该实例未关联代码包");
    return;
  }
  packageDetailId.value = record.package_id;
  packageDetailVisible.value = true;
};
</script>

<style scoped src="./function-instance.scss"></style>
