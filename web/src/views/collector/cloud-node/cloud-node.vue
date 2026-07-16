<template>
  <div class="moox-page">
    <a-spin :loading="loading">
      <div class="moox-inner">
        <div class="page-head">
          <h2>云节点</h2>
        </div>
        <a-space class="cloud-node-action-bar" wrap>
          <a-button type="primary" status="success" @click="onBatchAdd" :disabled="batchChangeProcessing">
            <template #icon><icon-plus-circle /></template>
            <span>批量新增</span>
          </a-button>
          <a-button type="primary" status="warning" @click="batchDeploy" :disabled="batchChangeProcessing">
            <template #icon><icon-upload /></template>
            <span>批量部署</span>
          </a-button>
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
              <a-tag v-if="region.tag" size="small" :color="region.tag === '国内' ? 'blue' : 'orange'" style="margin-left: 4px;">
                {{ region.tag }}
              </a-tag>
            </a-option>
          </a-select>
          <a-select placeholder="节点类型" v-model="form.nodeType" style="width: 180px" allow-clear>
            <a-option value="scf-event">云函数（事件型）</a-option>
            <a-option value="scf-web">云函数（Web型）</a-option>
            <a-option value="server">服务器</a-option>
          </a-select>
          <a-select placeholder="节点状态" v-model="form.status" style="width: 120px" allow-clear>
            <a-option value="online">在线</a-option>
            <a-option value="offline">离线</a-option>
          </a-select>
          <a-button type="primary" @click="search">
            <template #icon><icon-search /></template>
            <span>查询</span>
          </a-button>
        </a-space>

        <!-- 批量变更进度提示 -->
        <a-alert
          v-if="batchChangeStatuses.length > 0"
          type="info"
          style="margin: 16px 0;"
          closable
          @close="handleCloseBatchChangeAlert"
        >
          <template #title>
            <a-space>
              <icon-loading spin />
              <span>云节点批量变更处理中</span>
            </a-space>
          </template>
          <div v-for="(batchChange, index) in batchChangeStatuses" :key="batchChange.batch_id || index" style="margin-bottom: 12px;">
            <div>批次 {{ index + 1 }}：{{ getBatchChangeTypeText(batchChange.batch_change_type) }}</div>
            <div>处理进度：{{ batchChange.success_count + batchChange.failed_count }} / {{ batchChange.total_count }}</div>
            <div>成功：{{ batchChange.success_count }}，失败：{{ batchChange.failed_count }}</div>
            <a-progress 
              :percent="(Number(batchChange.progress) || 0) / 100" 
              :status="batchChange.failed_count > 0 ? 'warning' : 'normal'"
              :stroke-width="8"
              style="margin-top: 8px"
            />
          </div>
        </a-alert>
        <a-alert
          v-else-if="currentBatchChangeStatus && currentBatchChangeStatus.batch_change_status === 1"
          type="info"
          style="margin: 16px 0;"
          closable
          @close="handleCloseBatchChangeAlert"
        >
          <template #title>
            <a-space>
              <icon-loading spin />
              <span>云节点批量变更处理中</span>
            </a-space>
          </template>
          <div>
            <div>变更类型：{{ getBatchChangeTypeText(currentBatchChangeStatus.batch_change_type) }}</div>
            <div>处理进度：{{ currentBatchChangeStatus.success_count + currentBatchChangeStatus.failed_count }} / {{ currentBatchChangeStatus.total_count }}</div>
            <div>成功：{{ currentBatchChangeStatus.success_count }}，失败：{{ currentBatchChangeStatus.failed_count }}</div>
            <a-progress 
              :percent="(Number(currentBatchChangeStatus.progress) || 0) / 100" 
              :status="currentBatchChangeStatus.failed_count > 0 ? 'warning' : 'normal'"
              :stroke-width="8"
              style="margin-top: 8px"
            />
          </div>
        </a-alert>

        <!-- 选择状态提示 -->
        <a-alert
          v-if="selectedKeys.length > 0 && !batchChangeProcessing"
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
            <a-table-column title="节点ID" data-index="node_id" :width="120">
              <template #cell="{ record }">
                <a-link @click="onViewNodeDetail(record)">{{ record.node_id }}</a-link>
              </template>
            </a-table-column>
            <a-table-column title="命名空间" data-index="namespace" :width="120"></a-table-column>
            <a-table-column title="地区" data-index="region" :width="150">
              <template #cell="{ record }">
                {{ getRegionName(record.region) }}
              </template>
            </a-table-column>
            <a-table-column title="最后心跳时间" data-index="last_heartbeat" :width="170">
              <template #cell="{ record }">
                {{ formatDateTime(record.last_heartbeat) }}
              </template>
            </a-table-column>
            <a-table-column title="CLS 主题 ID" data-index="cls_topic_id" :width="180" :ellipsis="true" :tooltip="true">
              <template #cell="{ record }">
                {{ record.cls_topic_id || '-' }}
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
            <a-table-column title="支持的工作负载" data-index="supported_workloads" :width="150">
              <template #cell="{ record }">
                <div v-if="getSupportedWorkloads(record.supported_workloads).length > 0" style="display: flex; flex-wrap: wrap; gap: 4px;">
                  <a-tag
                    v-for="(workload, index) in getSupportedWorkloads(record.supported_workloads)"
                    :key="index"
                    size="small"
                    :color="getCollectorColor(workload)"
                  >
                    {{ getCollectorName(workload) }}
                  </a-tag>
                </div>
                <span v-else>-</span>
              </template>
            </a-table-column>
            <a-table-column title="代码包版本" data-index="package_version" :width="150">
              <template #cell="{ record }">
                <a-link
                  v-if="record.package_version && record.package_version !== '-'"
                  @click="onShowPackageDetail(record)"
                  style="cursor: pointer;"
                >
                  {{ record.package_version }}
                </a-link>
                <span v-else>-</span>
              </template>
            </a-table-column>
            <a-table-column title="状态" :width="80" align="center">
              <template #cell="{ record }">
                <a-tag bordered size="small"
                  :color="getStatusColor(record.status)">
                  {{ getStatusText(record.status) }}
                </a-tag>
              </template>
            </a-table-column>
            <a-table-column title="操作" :width="170" align="center" fixed="right">
              <template #cell="{ record }">
                <a-space>
                  <a-button type="outline" size="mini" @click="onEdit(record)" :disabled="batchChangeProcessing">
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
                      :disabled="batchChangeProcessing"
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

        <a-form-item field="nodeType" label="节点类型" required>
          <a-select v-model="batchAddForm.nodeType" placeholder="请选择节点类型" style="width: 100%">
            <a-option value="scf-event">云函数（事件型）</a-option>
            <a-option value="scf-web">云函数（Web型）</a-option>
            <a-option value="server">服务器</a-option>
          </a-select>
        </a-form-item>

        <a-form-item field="runtime" label="运行时环境" required>
          <a-select v-model="batchAddForm.runtime" placeholder="请选择运行时环境" style="width: 100%">
            <a-option value="Go1">Go1</a-option>
            <a-option value="Python3.6">Python3.6</a-option>
            <a-option value="Python3.7">Python3.7</a-option>
            <a-option value="Python3.9">Python3.9</a-option>
            <a-option value="Nodejs12.16">Node.js 12.16</a-option>
            <a-option value="Nodejs16.13">Node.js 16.13</a-option>
            <a-option value="CustomRuntime">CustomRuntime</a-option>
          </a-select>
        </a-form-item>

        <a-form-item field="handler" label="函数入口" required>
          <a-input v-model="batchAddForm.handler" placeholder="main" />
        </a-form-item>

        <a-form-item field="region" label="地区" required>
          <a-select v-model="batchAddForm.region" placeholder="请选择地区" style="width: 100%">
            <a-option :value="REGION_UNLIMITED">不限</a-option>
            <a-option v-for="region in regionOptions" :key="region.code" :value="region.code">
              {{ region.name }}
              <a-tag v-if="region.tag" size="small" :color="region.tag === '国内' ? 'blue' : 'orange'" style="margin-left: 4px;">
                {{ region.tag }}
              </a-tag>
            </a-option>
          </a-select>
        </a-form-item>

        <a-form-item field="tag" label="标签" required>
          <a-select v-model="batchAddForm.tag" :disabled="batchAddTagLocked" placeholder="请选择标签" style="width: 100%">
            <a-option v-for="tag in tagOptions" :key="tag" :value="tag">
              {{ tag }}
            </a-option>
          </a-select>
          <template #help>
            <div style="font-size: 12px; color: #86909c;">选择具体地区时标签会自动锁定</div>
          </template>
        </a-form-item>
        
        <a-form-item field="packageId" label="代码包版本" required>
          <a-select v-model="batchAddForm.packageId" placeholder="请选择代码包版本" style="width: 100%">
            <a-option v-for="pkg in availablePackagesForCreation" :key="pkg.package_id" :value="pkg.package_id">
              {{ pkg.package_name }} - {{ pkg.version }} ({{ pkg.runtime }})
            </a-option>
          </a-select>
        </a-form-item>
        
        <a-form-item field="nodeCount" label="节点数量" required>
          <a-input-number 
            v-model="batchAddForm.nodeCount" 
            :min="1" 
            placeholder="请输入要创建的节点数量"
            style="width: 100%"
          />
        </a-form-item>
        
        <!-- 心跳配置 -->
        <a-divider orientation="left">心跳配置</a-divider>
        
        <a-form-item field="timeoutThreshold" label="超时阈值（秒）">
          <a-input-number 
            v-model="batchAddForm.timeoutThreshold" 
            :min="0" 
            :max="3600" 
            placeholder="0表示使用全局默认值"
            style="width: 100%"
          />
          <template #help>
            <div style="font-size: 12px; color: #86909c;">设置为0时将使用全局默认值（通常为30秒）</div>
          </template>
        </a-form-item>
        
        <a-form-item field="heartbeatInterval" label="心跳间隔（秒）">
          <a-input-number 
            v-model="batchAddForm.heartbeatInterval" 
            :min="0" 
            :max="300" 
            placeholder="0表示使用全局默认值"
            style="width: 100%"
          />
          <template #help>
            <div style="font-size: 12px; color: #86909c;">设置为0时将使用全局默认值（通常为10秒）</div>
          </template>
        </a-form-item>
        
        <a-form-item field="probeEnabled" label="启用探测">
          <a-switch v-model="batchAddForm.probeEnabled" />
          <template #help>
            <div style="font-size: 12px; color: #86909c;">是否启用节点健康检查探测</div>
          </template>
        </a-form-item>
      </a-form>
    </a-modal>

    <!-- 批量新增分布计划弹窗 -->
    <a-modal
      v-model:visible="batchPlanVisible"
      title="预计分布计划"
      :width="900"
      :mask-closable="false"
      @cancel="handleBatchPlanCancel"
      @ok="handleBatchPlanOk"
    >
      <a-spin :loading="batchPlanLoading">
        <div style="margin-bottom: 12px;">
          <div>标签：{{ batchPlanTag }}</div>
          <div>请求数量：{{ batchPlanRequested }}</div>
          <div>可用总数：{{ batchPlanTotalAvailable }}</div>
          <div>计划数量：{{ batchPlanPlannedTotal }} / {{ batchPlanTarget }}</div>
        </div>
        <a-alert v-if="batchPlanNotice" type="warning" style="margin-bottom: 12px;">
          {{ batchPlanNotice }}
        </a-alert>
        <a-table
          row-key="regionCode"
          :data="batchPlanItems"
          :bordered="{ cell: true }"
          :pagination="false"
          size="small"
        >
          <template #columns>
            <a-table-column title="地区" data-index="regionName" :width="180" />
            <a-table-column title="最大节点数" data-index="maxNodes" :width="120" />
            <a-table-column title="已占用" data-index="usedNodes" :width="100" />
            <a-table-column title="可用" data-index="availableNodes" :width="100" />
            <a-table-column title="计划数" :width="140">
              <template #cell="{ record }">
                <a-input-number
                  v-model="record.planCount"
                  :min="0"
                  :max="record.availableNodes"
                  style="width: 120px"
                />
              </template>
            </a-table-column>
            <a-table-column title="操作" :width="80">
              <template #cell="{ record }">
                <a-button type="text" status="danger" size="mini" @click="removePlanItem(record)">
                  删除
                </a-button>
              </template>
            </a-table-column>
          </template>
        </a-table>
      </a-spin>
    </a-modal>

    <!-- 批量部署弹窗 -->
    <a-modal
      v-model:visible="batchDeployVisible"
      title="批量部署云函数"
      :width="800"
      :mask-closable="false"
      @cancel="handleBatchDeployCancel"
      @ok="handleBatchDeployOk"
    >
      <a-form :model="batchDeployForm" layout="vertical">
        <a-form-item label="选择代码包版本" required>
          <a-table
            row-key="package_id"
            size="small"
            :bordered="{ cell: true }"
            :data="availablePackages"
            :loading="packagesLoading"
            :pagination="packagesPagination"
            :scroll="{ y: 300 }"
            :row-selection="{ type: 'radio', showCheckedAll: false }"
            :selected-keys="batchDeployForm.selectedPackageId ? [batchDeployForm.selectedPackageId] : []"
            @select="onSelectPackage"
            @page-change="onPackagePageChange"
          >
            <template #columns>
              <a-table-column title="代码包名称" data-index="package_name" :width="140"></a-table-column>
              <a-table-column title="版本" data-index="version" :width="100"></a-table-column>
              <a-table-column title="类型" data-index="package_type" :width="120">
                <template #cell="{ record }">
                  <a-tag :color="getPackageTypeColor(record.package_type)" size="small">
                    {{ getPackageTypeLabel(record.package_type) }}
                  </a-tag>
                </template>
              </a-table-column>
              <a-table-column title="运行时" data-index="runtime" :width="100"></a-table-column>
              <a-table-column title="文件大小" data-index="file_size" :width="100">
                <template #cell="{ record }">
                  {{ formatFileSize(record.file_size) }}
                </template>
              </a-table-column>
              <a-table-column title="创建时间" data-index="created_time" :width="150">
                <template #cell="{ record }">
                  {{ formatTime(record.created_time) }}
                </template>
              </a-table-column>
            </template>
          </a-table>
        </a-form-item>
        
        <a-form-item>
          <a-alert type="info">
            <div>将为以下 {{ selectedKeys.length }} 个节点部署选中的函数版本：</div>
            <div style="margin-top: 8px; max-height: 120px; overflow-y: auto;">
              <a-tag v-for="nodeId in selectedKeys" :key="nodeId" style="margin: 4px;">
                {{ nodeId }}
              </a-tag>
            </div>
          </a-alert>
        </a-form-item>
      </a-form>
    </a-modal>

    <!-- 单节点部署弹窗 -->
    <a-modal
      v-model:visible="singleDeployVisible"
      title="部署云函数代码包"
      :width="800"
      :mask-closable="false"
      @cancel="handleSingleDeployCancel"
      @ok="handleSingleDeployOk"
    >
      <a-form :model="singleDeployForm" layout="vertical">
        <a-form-item label="节点信息">
          <a-alert type="info" style="margin-bottom: 8px;">
            <div><strong>节点ID：</strong>{{ singleDeployForm.nodeId }}</div>
            <div><strong>命名空间：</strong>{{ singleDeployForm.namespace }}</div>
            <div><strong>地区：</strong>{{ singleDeployForm.region }}</div>
          </a-alert>
        </a-form-item>
        
        <a-form-item label="选择代码包版本" required>
          <a-table
            row-key="package_id"
            size="small"
            :bordered="{ cell: true }"
            :data="singleDeployPackages"
            :loading="singleDeployPackagesLoading"
            :pagination="singleDeployPackagesPagination"
            :scroll="{ y: 300 }"
            :row-selection="{ type: 'radio', showCheckedAll: false }"
            :selected-keys="singleDeployForm.selectedPackageId ? [singleDeployForm.selectedPackageId] : []"
            @select="onSelectSingleDeployPackage"
            @page-change="onSingleDeployPackagePageChange"
          >
            <template #columns>
              <a-table-column title="代码包名称" data-index="package_name" :width="140"></a-table-column>
              <a-table-column title="版本" data-index="version" :width="100"></a-table-column>
              <a-table-column title="类型" data-index="package_type" :width="120">
                <template #cell="{ record }">
                  <a-tag :color="getPackageTypeColor(record.package_type)" size="small">
                    {{ getPackageTypeLabel(record.package_type) }}
                  </a-tag>
                </template>
              </a-table-column>
              <a-table-column title="运行时" data-index="runtime" :width="100"></a-table-column>
              <a-table-column title="文件大小" data-index="file_size" :width="100">
                <template #cell="{ record }">
                  {{ formatFileSize(record.file_size) }}
                </template>
              </a-table-column>
              <a-table-column title="创建时间" data-index="created_time" :width="150">
                <template #cell="{ record }">
                  {{ formatTime(record.created_time) }}
                </template>
              </a-table-column>
            </template>
          </a-table>
        </a-form-item>
      </a-form>
    </a-modal>

    <!-- 云账户管理弹窗 -->
    <CloudAccountManage 
      v-model="cloudAccountManageVisible" 
      @refresh="loadCloudAccounts"
    />

    <!-- 代码包版本管理弹窗 -->
    <FunctionPackageManage
      v-model="functionPackageManageVisible"
      mode="modal"
      :package-type="currentPackageType"
      :biz-type="currentBizType"
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
            <a-tag bordered size="small" :color="getNodeTypeColor(selectedNodeDetail.node_type)">
              {{ getNodeTypeLabel(selectedNodeDetail.node_type) }}
            </a-tag>
          </a-descriptions-item>
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
            {{ selectedNodeDetail.package_version || '-' }}
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
          <a-descriptions-item label="超时阈值">
            {{ selectedNodeDetail.timeout_threshold || 0 }}秒
            <span v-if="selectedNodeDetail.timeout_threshold === 0" style="color: #86909c;">（使用全局默认值）</span>
          </a-descriptions-item>
          <a-descriptions-item label="心跳间隔">
            {{ selectedNodeDetail.heartbeat_interval || 0 }}秒
            <span v-if="selectedNodeDetail.heartbeat_interval === 0" style="color: #86909c;">（使用全局默认值）</span>
          </a-descriptions-item>
          <a-descriptions-item label="启用探测">
            <a-tag bordered size="small" :color="selectedNodeDetail.probe_enabled ? 'green' : 'red'">
              {{ selectedNodeDetail.probe_enabled ? '是' : '否' }}
            </a-tag>
          </a-descriptions-item>
          <a-descriptions-item label="CLS 主题 ID">
            {{ selectedNodeDetail.cls_topic_id || '-' }}
          </a-descriptions-item>
          <a-descriptions-item label="元数据" :span="2">
            <div v-if="selectedNodeDetail.metadata" style="max-height: 200px; overflow-y: auto; white-space: pre-wrap; font-family: monospace; background: #f6f8fa; padding: 8px; border-radius: 4px;">{{ formatMetadata(selectedNodeDetail.metadata) }}</div>
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
        <a-descriptions title="基本信息" :column="2" bordered size="medium" style="margin-bottom: 16px;">
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
            <a-typography-text copyable>{{ packageDetail.file_md5 || '-' }}</a-typography-text>
          </a-descriptions-item>
          <a-descriptions-item label="描述" :span="2">
            {{ packageDetail.description || '-' }}
          </a-descriptions-item>
        </a-descriptions>
        
        <!-- 存储信息 -->
        <a-descriptions title="存储信息" :column="2" bordered size="medium" style="margin-bottom: 16px;">
          <a-descriptions-item label="云账户ID">{{ packageDetail.cloud_account_id }}</a-descriptions-item>
          <a-descriptions-item label="COS区域">{{ packageDetail.cos_region }}</a-descriptions-item>
          <a-descriptions-item label="COS Bucket">{{ packageDetail.cos_bucket }}</a-descriptions-item>
          <a-descriptions-item label="COS路径">{{ packageDetail.cos_path }}</a-descriptions-item>
          <a-descriptions-item label="原始文件名" :span="2">{{ packageDetail.original_filename || '-' }}</a-descriptions-item>
        </a-descriptions>
        
        <!-- 审计信息 -->
        <a-descriptions title="审计信息" :column="2" bordered size="medium" style="margin-bottom: 16px;">
          <a-descriptions-item label="创建者">{{ packageDetail.created_by }}</a-descriptions-item>
          <a-descriptions-item label="创建时间">{{ formatDateTime(packageDetail.created_time) }}</a-descriptions-item>
          <a-descriptions-item label="最后部署时间" :span="2">
            {{ packageDetail.last_deploy_time ? formatDateTime(packageDetail.last_deploy_time) : '-' }}
          </a-descriptions-item>
        </a-descriptions>
        
        <div style="margin-top: 16px; text-align: right;">
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
      <div v-else style="text-align: center; padding: 40px;">
        <a-spin :loading="true" />
        <div style="margin-top: 16px;">加载中...</div>
      </div>
    </a-modal>

    <!-- 节点编辑弹窗 -->
    <a-modal
      v-model:visible="editNodeVisible"
      title="编辑云函数节点"
      :width="600"
      :mask-closable="false"
      @cancel="handleEditNodeCancel"
      @ok="handleEditNodeOk"
    >
      <a-form :model="editNodeForm" layout="vertical">
        <a-form-item label="节点信息">
          <a-alert type="info" style="margin-bottom: 8px;">
            <div><strong>节点ID：</strong>{{ editNodeForm.nodeId }}</div>
            <div><strong>命名空间：</strong>{{ editNodeForm.namespace }}</div>
            <div><strong>地区：</strong>{{ editNodeForm.region }}</div>
          </a-alert>
        </a-form-item>

        <!-- 心跳配置 -->
        <a-divider orientation="left">心跳配置</a-divider>
        
        <a-form-item field="timeoutThreshold" label="超时阈值（秒）">
          <a-input-number 
            v-model="editNodeForm.timeoutThreshold" 
            :min="0" 
            :max="3600" 
            placeholder="0表示使用全局默认值"
            style="width: 100%"
          />
          <template #help>
            <div style="font-size: 12px; color: #86909c;">设置为0时将使用全局默认值（通常为30秒）</div>
          </template>
        </a-form-item>
        
        <a-form-item field="heartbeatInterval" label="心跳间隔（秒）">
          <a-input-number 
            v-model="editNodeForm.heartbeatInterval" 
            :min="0" 
            :max="300" 
            placeholder="0表示使用全局默认值"
            style="width: 100%"
          />
          <template #help>
            <div style="font-size: 12px; color: #86909c;">设置为0时将使用全局默认值（通常为10秒）</div>
          </template>
        </a-form-item>
        
        <a-form-item field="probeEnabled" label="启用探测">
          <a-switch v-model="editNodeForm.probeEnabled" />
          <template #help>
            <div style="font-size: 12px; color: #86909c;">是否启用节点健康检查探测</div>
          </template>
        </a-form-item>
      </a-form>
    </a-modal>

  </div>
</template>

<script setup lang="ts">
import { ref, reactive, computed, onMounted, onBeforeUnmount, h, watch } from 'vue';
import { storeToRefs } from 'pinia';
import { Message, Modal } from '@arco-design/web-vue';
import { getFunctionPackageList, getFunctionPackageDetail, downloadPackageByURL, PACKAGE_STATUS_LABEL, PACKAGE_TYPE_LABEL, type FunctionPackage } from '@/api/function-package';
import { getCloudAccountList, type CloudAccount } from '@/api/cloud-account';
import { batchCreateNodes, batchDeleteNodes, batchDeployNodes, getNodeList, listCloudRegions, updateNode, NODE_STATUS_LABEL } from '@/api/cloud-node';
import { useSpaceStore } from '@/store/modules/space';
import { BatchChangeStatus } from '@/utils/cloud-node-batch-change';
import type { BatchChangeStatusResponse, BatchChangeDetailItem } from '@/utils/cloud-node-batch-change';
import CloudAccountManage from '../cloud-account/cloud-account-manage.vue';
import FunctionPackageManage from './function-package-manage.vue';

// 获取当前路由

// 接口定义
interface CloudNode {
  node_id: string;
  cloud_account_id: string;
  namespace: string;
  node_type: string;
  region: string;
  tag: string; // 标签（国内/海外）
  ip_address: string;
  version: string;
  package_id?: string;
  package_version?: string; // 代码包版本（包名-版本号）
  running_version?: string; // 当前运行版本（来自心跳上报）
  supported_workloads: string[] | string;
  capacity: string;
  current_load: string;
  metadata: string | Record<string, unknown>;
  status: string | number;
  enabled: number;
  // 新增心跳配置字段
  timeout_threshold: number;    // 超时阈值（秒），0表示使用全局默认值
  heartbeat_interval: number;   // 心跳间隔（秒），0表示使用全局默认值
  probe_enabled: boolean;       // 是否启用探测
  probe_url?: string;           // 探测URL
  cls_topic_id?: string;
  last_heartbeat?: string;
  created_at: string;
  create_time?: string;
  modify_time?: string;
  updated_at: string;
}

const normalizeCloudNodes = (items: Array<Partial<CloudNode> & Record<string, any>>): CloudNode[] => {
  return items.map((item) => ({
    node_id: item.node_id || '',
    cloud_account_id: item.cloud_account_id || '',
    namespace: item.namespace || '',
    node_type: item.node_type || '',
    region: item.region || '',
    tag: item.tag || '',
    ip_address: item.ip_address || '',
    version: item.version || item.package_version || item.running_version || '',
    package_id: item.package_id || '',
    package_version: item.package_version || '',
    running_version: item.running_version || '',
    supported_workloads: normalizeSupportedWorkloads(item.supported_workloads),
    capacity: item.capacity || '',
    current_load: item.current_load || '',
    metadata: item.metadata || '',
    status: item.status || 'offline',
    enabled: item.enabled ?? 1,
    timeout_threshold: item.timeout_threshold || 0,
    heartbeat_interval: item.heartbeat_interval || 0,
    probe_enabled: item.probe_enabled ?? false,
    probe_url: item.probe_url || '',
    cls_topic_id: item.cls_topic_id || '',
    last_heartbeat: item.last_heartbeat || '',
    created_at: item.created_at || item.create_time || '',
    create_time: item.create_time || '',
    modify_time: item.modify_time || '',
    updated_at: item.updated_at || item.modify_time || ''
  }));
};



// 状态管理
const spaceStore = useSpaceStore();
const { selectedSpaceId } = storeToRefs(spaceStore);
const loading = ref(false);
const batchChangeProcessing = ref(false);
const currentBatchChangeStatus = ref<BatchChangeStatusResponse | null>(null);
const batchChangeCompleteHandled = ref(false); // 防止重复处理提交完成

const form = reactive({
  cloudAccountId: '',
  nodeId: '',
  region: '',
  nodeType: '',
  status: ''
});

// 接口定义 - 地区信息
interface RegionInfo {
  code: string;
  name: string;
  tag: string; // 标签（国内/海外）
  max_nodes?: number; // 地区最大节点数
}

interface BatchPlanItem {
  regionCode: string;
  regionName: string;
  tag: string;
  maxNodes: number;
  usedNodes: number;
  availableNodes: number;
  planCount: number;
}

type BatchChangeViewStatus = BatchChangeStatusResponse & {
  batchIndex: number;
};

// 数据列表
const cloudNodeList = ref<CloudNode[]>([]);
const selectedKeys = ref<string[]>([]);
const cloudAccountOptions = ref<CloudAccount[]>([]);
const regionOptions = ref<RegionInfo[]>([]); // 地区选项
const REGION_UNLIMITED = 'all';
const tagOptions = ['国内', '海外'];

// 批量新增相关
const batchAddVisible = ref(false);
const batchAddForm = reactive({
  cloudAccountId: '',
  nodeType: 'scf-event', // 节点类型，默认为事件型云函数
  runtime: 'Go1', // 运行时环境，默认Go1
  handler: 'main',
  region: 'ap-guangzhou',
  tag: '',
  packageId: '', // 代码包版本ID
  nodeCount: 5,
  namespace: '',
  // 新增心跳配置字段
  timeoutThreshold: 0,    // 超时阈值（秒），0表示使用全局默认值
  heartbeatInterval: 0,   // 心跳间隔（秒），0表示使用全局默认值
  probeEnabled: true,     // 是否启用探测，默认启用
  config: {} as Record<string, string>,
  environment: {} as Record<string, string>
});

const batchPlanVisible = ref(false);
const batchPlanLoading = ref(false);
const batchPlanItems = ref<BatchPlanItem[]>([]);
const batchPlanNotice = ref('');
const batchPlanRequested = ref(0);
const batchPlanTag = ref('');
const batchChangeStatuses = ref<BatchChangeViewStatus[]>([]);

// 批量部署相关
const batchDeployVisible = ref(false);
const packagesLoading = ref(false);
const availablePackages = ref<any[]>([]);
const availablePackagesForCreation = ref<any[]>([]); // 批量创建时的代码包选项
const batchDeployForm = reactive({
  selectedPackageId: '', // 选中的代码包ID
  deployConfig: {} // 可选的部署配置
});

// 代码包分页配置
const packagesPagination = ref({
  current: 1,
  pageSize: 10,
  total: 0,
  showTotal: true,
  showJumper: true,
  showPageSize: false
});

// 单节点部署相关
const singleDeployVisible = ref(false);
const singleDeployPackagesLoading = ref(false);
const singleDeployPackages = ref<any[]>([]);
const singleDeployForm = reactive({
  nodeId: '',
  namespace: '',
  region: '',
  selectedPackageId: ''
});

const singleDeployPackagesPagination = ref({
  current: 1,
  pageSize: 10,
  total: 0,
  showTotal: true,
  showJumper: true,
  showPageSize: false
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

const batchAddTagLocked = computed(() => batchAddForm.region !== REGION_UNLIMITED);
const batchPlanPlannedTotal = computed(() => batchPlanItems.value.reduce((sum, item) => sum + (Number(item.planCount) || 0), 0));
const batchPlanTotalAvailable = computed(() => batchPlanItems.value.reduce((sum, item) => sum + (Number(item.availableNodes) || 0), 0));
const batchPlanTarget = computed(() => Math.min(batchPlanRequested.value, batchPlanTotalAvailable.value));

// 当前页面是采集云节点管理页，代码包固定按采集器类型过滤。
const currentPackageType = computed(() => 1);

// 根据路由路径判断默认的节点类型
const defaultNodeType = computed(() => {
  return ''; // 搜索框节点类型不设默认值
});

// 采集 SCF runtime 默认使用 Go1。
const defaultRuntime = computed(() => 'Go1');

// 当前页面的业务类型固定为数据采集。
const currentBizType = computed(() => 'data_collector');

// 生命周期钩子
onMounted(async () => {
  // 根据路由设置默认的节点类型筛选条件
  form.nodeType = defaultNodeType.value;

  await spaceStore.loadSpaces();
  if (selectedSpaceId.value) {
    await loadData();
  } else {
    Message.warning('请先选择空间');
  }
  await loadCloudAccounts();
  await loadRegions(); // 加载地区列表
});

watch(selectedSpaceId, async (spaceId, oldSpaceId) => {
  if (spaceId === oldSpaceId) return;
  pagination.value.current = 1;
  selectedKeys.value = [];
  if (!spaceId) {
    cloudNodeList.value = [];
    pagination.value.total = 0;
    return;
  }
  await loadData();
});

onBeforeUnmount(() => {
  batchChangeStatuses.value = [];
  currentBatchChangeStatus.value = null;
});

// 批量变更完成处理
const handleBatchChangeComplete = async (data: BatchChangeStatusResponse) => {
  batchChangeStatuses.value = [];
  
  // 防止重复处理
  if (batchChangeCompleteHandled.value) {
    return;
  }
  batchChangeCompleteHandled.value = true;
  
  // 先更新状态为完成状态，让用户看到100%的进度
  currentBatchChangeStatus.value = data;
  
  // 延迟1秒后再清理
  setTimeout(async () => {
    batchChangeProcessing.value = false;
    currentBatchChangeStatus.value = null;
    
    // 清空选中项
    selectedKeys.value = [];    
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
        h('div', `变更类型：${getBatchChangeTypeText(data.batch_change_type)}`),
        h('div', `总数量：${data.total_count}`),
        h('div', `成功数量：${data.success_count}`),
        h('div', { style: { color: '#ff4d4f' } }, `失败数量：${data.failed_count}`)
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
      title: '批量变更失败',
      content,
      width: 700,
      maskClosable: false
    });
    } else {
      // 全部成功，显示成功提示
      Message.success(`${getBatchChangeTypeText(data.batch_change_type)}成功！共处理 ${data.total_count} 个节点`);
    }
  }, 1200); // 稍微延迟比进度条消失时间长一点，避免冲突
};

// 关闭批量变更提示
const handleCloseBatchChangeAlert = () => {
  batchChangeStatuses.value = [];
  currentBatchChangeStatus.value = null;
  batchChangeProcessing.value = false;
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
  batchAddForm.nodeType = defaultNodeType.value; // 根据路由设置默认节点类型
  batchAddForm.runtime = defaultRuntime.value; // 根据路由设置默认运行时
  batchAddForm.region = 'ap-hongkong';
  batchAddForm.tag = getRegionTag(batchAddForm.region) || '海外';
  batchAddForm.packageId = '';
  batchAddForm.nodeCount = 5;
  batchAddForm.namespace = '';
  batchAddForm.timeoutThreshold = 0;
  batchAddForm.heartbeatInterval = 0;
  batchAddForm.probeEnabled = true;
  
  // 加载可用的代码包列表
  await loadAvailablePackagesForCreation();
  
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
  if (!batchAddForm.nodeType) {
    Message.warning('请选择节点类型');
    return;
  }
  if (!batchAddForm.runtime) {
    Message.warning('请选择运行时环境');
    return;
  }
  if (!batchAddForm.region) {
    Message.warning('请选择地区');
    return;
  }
  if (!batchAddForm.tag) {
    Message.warning('请选择标签');
    return;
  }
  if (!batchAddForm.packageId) {
    Message.warning('请选择代码包版本');
    return;
  }
  if (!batchAddForm.nodeCount || batchAddForm.nodeCount < 1) {
    Message.warning('请输入有效的节点数量');
    return;
  }

  // 关闭弹窗
  batchAddVisible.value = false;

  if (batchAddForm.region === REGION_UNLIMITED) {
    await prepareBatchPlan();
    return;
  }

  // 执行批量新增
  await executeBatchAddDirect();
};

const optionalRecord = (value: Record<string, string>) => (
  Object.keys(value).length > 0 ? value : undefined
);

const buildCreateNodeBatchChange = (region: string, index: number) => ({
  batchChangeType: 'CREATE_NODE',
  requestPayload: {
    cloud_account_id: batchAddForm.cloudAccountId,
    namespace: batchAddForm.namespace || undefined,
    node_type: batchAddForm.nodeType, // 使用表单中选择的节点类型
    runtime: batchAddForm.runtime, // 运行时环境
    handler: batchAddForm.handler || 'main',
    biz_type: currentBizType.value, // 根据路由设置业务类型
    region,
    tag: getRegionTag(region) || batchAddForm.tag,
    package_id: batchAddForm.packageId,
    config: optionalRecord(batchAddForm.config),
    environment: optionalRecord(batchAddForm.environment),
    version: '1.0.0',
    capacity: '100',
    metadata: JSON.stringify({ env: 'prod', index }),
    // 新增心跳配置字段
    timeout_threshold: batchAddForm.timeoutThreshold,
    heartbeat_interval: batchAddForm.heartbeatInterval,
    probe_enabled: batchAddForm.probeEnabled
  }
});

const buildBatchChangesForRegion = (region: string, count: number, startIndex: number) => (
  Array(count).fill(null).map((_, index) => buildCreateNodeBatchChange(region, startIndex + index))
);

const buildBatchChangesFromPlan = (items: BatchPlanItem[]) => {
  const batchChanges: Array<{ batchChangeType: string; requestPayload: any }> = [];
  let index = 0;
  items.forEach(item => {
    const count = Number(item.planCount) || 0;
    for (let i = 0; i < count; i += 1) {
      batchChanges.push(buildCreateNodeBatchChange(item.regionCode, index));
      index += 1;
    }
  });
  return batchChanges;
};

const chunkTasks = <T,>(items: T[], size: number): T[][] => {
  const chunks: T[][] = [];
  for (let i = 0; i < items.length; i += size) {
    chunks.push(items.slice(i, i + size));
  }
  return chunks;
};

const executeBatchAddChanges = async (batchChanges: Array<{ batchChangeType: string; requestPayload: any }>) => {
  if (batchChanges.length === 0) {
    Message.warning('没有可执行的批量变更');
    return;
  }

  const chunks = chunkTasks(batchChanges, 100);

  batchChangeProcessing.value = true;
  batchChangeCompleteHandled.value = false;
  currentBatchChangeStatus.value = null;

  const initialStatuses: BatchChangeViewStatus[] = chunks.map((chunk, index) => ({
    batchIndex: index,
    batch_id: '',
    batch_change_type: 'CREATE_NODE',
    batch_change_status: BatchChangeStatus.PROCESSING,
    total_count: chunk.length,
    success_count: 0,
    failed_count: 0,
    progress: 0,
    created_at: new Date().toISOString()
  }));
  batchChangeStatuses.value = initialStatuses;

  const batchIds: string[] = [];

  for (let i = 0; i < chunks.length; i += 1) {
    const chunk = chunks[i];
    try {
      const batchId = await submitCloudNodeBatchChange(chunk);
      batchIds.push(batchId);
      batchChangeStatuses.value = batchChangeStatuses.value.map(item => (
        item.batchIndex === i ? { ...item, batch_id: batchId } : item
      ));
    } catch (error: any) {
      batchChangeStatuses.value = batchChangeStatuses.value.map(item => (
        item.batchIndex === i ? {
          ...item,
          batch_change_status: BatchChangeStatus.FAILED,
          failed_count: item.total_count,
          progress: 100,
          error_message: error?.message || '创建批量变更失败'
        } : item
      ));
    }
  }

  if (batchIds.length === 0) {
    const finalStatus = computeAggregateStatus(batchChangeStatuses.value);
    batchChangeStatuses.value = [];
    handleBatchChangeComplete(finalStatus);
    return;
  }

  batchChangeStatuses.value = batchChangeStatuses.value.map(item => (
    item.batch_id ? {
      ...item,
      batch_change_status: BatchChangeStatus.SUCCESS,
      success_count: item.total_count,
      failed_count: 0,
      progress: 100,
      completed_time: new Date().toISOString()
    } : item
  ));
  const finalStatus = computeAggregateStatus(batchChangeStatuses.value);
  batchChangeStatuses.value = [];
  handleBatchChangeComplete(finalStatus);
};

const executeBatchAddDirect = async () => {
  const batchChanges = buildBatchChangesForRegion(batchAddForm.region, batchAddForm.nodeCount, 0);
  await executeBatchAddChanges(batchChanges);
};

const fetchRegionUsage = async (regionCode: string, tag: string) => {
  const response = await getNodeList({
    region: regionCode,
    tag,
    node_type: batchAddForm.nodeType,
    page: 1,
    page_size: 1
  });
  return Number(response.total || 0);
};

const prepareBatchPlan = async () => {
  const selectedTag = batchAddForm.tag;
  const requestedCount = batchAddForm.nodeCount;

  const taggedRegions = regionOptions.value.filter(region => region.tag === selectedTag);
  if (taggedRegions.length === 0) {
    Message.warning('未找到对应标签的地区');
    return;
  }

  batchPlanLoading.value = true;
  batchPlanNotice.value = '';
  batchPlanRequested.value = requestedCount;
  batchPlanTag.value = selectedTag;

  try {
    const usageList = await Promise.all(taggedRegions.map(async (region) => {
      const usedNodes = await fetchRegionUsage(region.code, selectedTag);
      const maxNodes = Number(region.max_nodes || 0);
      const availableNodes = Math.max(0, maxNodes - usedNodes);
      return {
        regionCode: region.code,
        regionName: region.name,
        tag: region.tag,
        maxNodes,
        usedNodes,
        availableNodes,
        planCount: 0
      };
    }));

    const availableItems = usageList.filter(item => item.availableNodes > 0);
    if (availableItems.length === 0) {
      Message.warning('当前标签没有可用节点');
      return;
    }

    availableItems.sort((a, b) => a.availableNodes - b.availableNodes);

    let remaining = requestedCount;
    availableItems.forEach(item => {
      if (remaining <= 0) {
        item.planCount = 0;
        return;
      }
      const assign = Math.min(item.availableNodes, remaining);
      item.planCount = assign;
      remaining -= assign;
    });

    batchPlanItems.value = availableItems;
    if (batchPlanTotalAvailable.value < requestedCount) {
      batchPlanNotice.value = `当前可用节点总数为 ${batchPlanTotalAvailable.value}，小于需求 ${requestedCount}，将按可用数量创建。`;
    } else {
      batchPlanNotice.value = '';
    }

    batchPlanVisible.value = true;
  } catch (error) {
    console.error('生成分布计划失败:', error);
    Message.error('生成分布计划失败，请稍后重试');
  } finally {
    batchPlanLoading.value = false;
  }
};

const handleBatchPlanCancel = () => {
  batchPlanVisible.value = false;
  batchPlanItems.value = [];
  batchPlanNotice.value = '';
};

const removePlanItem = (record: BatchPlanItem) => {
  batchPlanItems.value = batchPlanItems.value.filter(item => item.regionCode !== record.regionCode);
  if (batchPlanTotalAvailable.value < batchPlanRequested.value) {
    batchPlanNotice.value = `当前可用节点总数为 ${batchPlanTotalAvailable.value}，小于需求 ${batchPlanRequested.value}，将按可用数量创建。`;
  } else {
    batchPlanNotice.value = '';
  }
};

const handleBatchPlanOk = async () => {
  if (batchPlanItems.value.length === 0) {
    Message.warning('没有可用地区');
    return;
  }
  if (batchPlanPlannedTotal.value <= 0) {
    Message.warning('计划数量不能为空');
    return;
  }
  if (batchPlanPlannedTotal.value > batchAddForm.nodeCount) {
    Message.warning('计划数量不能超过请求数量');
    return;
  }

  const overLimit = batchPlanItems.value.find(item => item.planCount > item.availableNodes);
  if (overLimit) {
    Message.warning(`地区 ${overLimit.regionName} 的计划数超过可用数量`);
    return;
  }

  if (batchPlanPlannedTotal.value < batchPlanTarget.value) {
    Message.warning('计划数量少于可创建数量，将按当前计划创建');
  }

  const batchChanges = buildBatchChangesFromPlan(batchPlanItems.value);
  batchPlanVisible.value = false;
  await executeBatchAddChanges(batchChanges);
};

// 批量部署
const batchDeploy = async () => {
  if (selectedKeys.value.length === 0) {
    Message.warning('请选择要部署的节点');
    return;
  }
  
  // 重置表单和分页
  batchDeployForm.selectedPackageId = '';
  batchDeployForm.deployConfig = {};
  packagesPagination.value.current = 1;
  
  // 显示批量部署弹窗
  batchDeployVisible.value = true;
  
  // 加载可用的代码包列表
  await loadAvailablePackages(1);
};

// 加载可用的代码包列表
const loadAvailablePackages = async (page: number = 1) => {
  packagesLoading.value = true;
  try {
    const response = await getFunctionPackageList({
      page: page,
      page_size: packagesPagination.value.pageSize,
      status: 2, // PACKAGE_STATUS_AVAILABLE
      package_type: currentPackageType.value // 根据当前路由过滤代码包类型
    });

    if (response?.items) {
      // 按时间倒序排列
      availablePackages.value = (response.items || []).sort((a: any, b: any) => {
        return new Date(b.created_time).getTime() - new Date(a.created_time).getTime();
      });

      // 更新分页信息
      packagesPagination.value.current = page;
      packagesPagination.value.total = response.total || response.items.length;
    } else {
      availablePackages.value = [];
      packagesPagination.value.total = 0;
    }
  } catch (error) {
    console.error('加载代码包列表失败:', error);
    Message.error('加载代码包列表失败');
    availablePackages.value = [];
    packagesPagination.value.total = 0;
  } finally {
    packagesLoading.value = false;
  }
};

// 加载批量创建时的代码包列表
const loadAvailablePackagesForCreation = async () => {
  try {
    const response = await getFunctionPackageList({
      page: 1,
      page_size: 100, // 获取较多数据
      status: 2, // PACKAGE_STATUS_AVAILABLE
      package_type: currentPackageType.value // 根据当前路由过滤代码包类型
    });

    if (response?.items) {
      // 按时间倒序排列
      availablePackagesForCreation.value = (response.items || []).sort((a: any, b: any) => {
        return new Date(b.created_time).getTime() - new Date(a.created_time).getTime();
      });
    } else {
      availablePackagesForCreation.value = [];
    }
  } catch (error) {
    console.error('加载代码包列表失败:', error);
    Message.error('加载代码包列表失败');
    availablePackagesForCreation.value = [];
  }
};

// 选择代码包
const onSelectPackage = (rowKeys: string[]) => {
  if (rowKeys.length > 0) {
    batchDeployForm.selectedPackageId = rowKeys[0];
  } else {
    batchDeployForm.selectedPackageId = '';
  }
};

// 代码包分页处理
const onPackagePageChange = (page: number) => {
  loadAvailablePackages(page);
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
  try {
    const total = selectedKeys.value.length;
    const rsp = await batchDeleteNodes({ node_ids: selectedKeys.value });
    if (!rsp.batch_id) {
      throw new Error('cloudnode 未返回 batch_id');
    }

    batchChangeProcessing.value = true;
    batchChangeCompleteHandled.value = false; // 重置批量变更完成处理标志
    completeCloudNodeBatchChange(rsp.batch_id, 'DELETE_NODE', total);
    
  } catch (error) {
    console.error('创建批量删除变更失败:', error);
  }
};

// 加载数据（使用后端分页）
const loadData = async (showEmptyTip = false) => {
  if (!selectedSpaceId.value) {
    cloudNodeList.value = [];
    pagination.value.total = 0;
    return;
  }
  loading.value = true;
  try {
    const response = await getNodeList({
      node_id: form.nodeId,
      cloud_account_id: form.cloudAccountId,
      region: form.region,
      node_type: form.nodeType,
      biz_type: currentBizType.value, // 根据路由添加业务类型过滤
      status: form.status,
      page: pagination.value.current,
      page_size: pagination.value.pageSize
    });

    const data = response.items || [];
    cloudNodeList.value = normalizeCloudNodes(Array.isArray(data) ? data : [data].filter(Boolean));
    pagination.value.total = response.total || cloudNodeList.value.length;
    if (showEmptyTip && cloudNodeList.value.length === 0) {
      Message.info('查询结果为空');
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
    const accounts = await getCloudAccountList();
    cloudAccountOptions.value = Array.isArray(accounts) ? accounts : [accounts].filter(Boolean);
  } catch (error) {
    console.error('加载云账户失败:', error);
    Message.error(error instanceof Error ? error.message : '加载云账户失败：请确认已登录且 moox-cloudnode 服务已部署');
  }
};

// 加载地区列表
const loadRegions = async () => {
  try {
    const data = await listCloudRegions('tencent');
    regionOptions.value = Array.isArray(data) ? data : [data].filter(Boolean);
  } catch (error) {
    console.error('加载地区列表失败:', error);
    // 失败时使用空数组
    regionOptions.value = [];
  }
};

// 工具函数
const getBatchChangeTypeText = (batchChangeType: string) => {
  const typeMap: Record<string, string> = {
    'CREATE_NODE': '批量创建节点',
    'BATCH_UPDATE_NODE': '批量更新节点',
    'DELETE_NODE': '批量删除节点',
    'DEPLOY_NODE': '批量部署节点'
  };
  return typeMap[batchChangeType] || batchChangeType;
};

const getProviderName = (provider: string) => {
  const providerMap: Record<string, string> = {
    'tencent': '腾讯云',
    'aliyun': '阿里云',
    'aws': 'AWS'
  };
  return providerMap[provider] || provider;
};

const getNodeTypeLabel = (nodeType: string) => {
  const labelMap: Record<string, string> = {
    'scf-event': '云函数（事件型）',
    'scf-web': '云函数（Web型）',
    'server': '服务器'
  };
  return labelMap[nodeType] || nodeType;
};

const getNodeTypeColor = (nodeType: string) => {
  const colorMap: Record<string, string> = {
    'scf-event': 'blue',
    'scf-web': 'cyan',
    'server': 'orange'
  };
  return colorMap[nodeType] || 'gray';
};

const getRegionName = (region: string) => {
  if (region === REGION_UNLIMITED) {
    return '不限';
  }
  // 从动态加载的地区列表中查找
  const regionInfo = regionOptions.value.find(r => r.code === region);
  return regionInfo ? regionInfo.name : region;
};

// 根据地区代码获取标签
const getRegionTag = (region: string) => {
  if (region === REGION_UNLIMITED) {
    return '';
  }
  const regionInfo = regionOptions.value.find(r => r.code === region);
  return regionInfo ? regionInfo.tag : '';
};

watch(
  () => batchAddForm.region,
  (region) => {
    if (region && region !== REGION_UNLIMITED) {
      const tag = getRegionTag(region);
      if (tag) {
        batchAddForm.tag = tag;
      }
      return;
    }

    if (!batchAddForm.tag) {
      batchAddForm.tag = '海外';
    }
  }
);

const getStatusColor = (status: string | number) => {
  const numeric = typeof status === 'number' ? status : Number(status);
  const colorMap: Record<number, string> = {
    2: 'green',
    1: 'red',
    3: 'orange',
    4: 'orangered'
  };
  if (status === 'online') return 'green';
  if (status === 'offline') return 'red';
  if (status === 'timeout') return 'orange';
  if (status === 'abnormal') return 'orangered';
  return colorMap[numeric] || 'gray';
};

const getStatusText = (status: string | number) => {
  if (typeof status === 'string' && status) {
    const legacyMap: Record<string, string> = {
      online: '在线',
      offline: '离线',
      timeout: '超时',
      abnormal: '异常',
    };
    if (legacyMap[status]) return legacyMap[status];
    if (NODE_STATUS_LABEL[status]) return NODE_STATUS_LABEL[status];
    return status;
  }
  const enumMap: Record<number, string> = {
    1: 'NODE_STATUS_OFFLINE',
    2: 'NODE_STATUS_ONLINE',
    3: 'NODE_STATUS_TIMEOUT',
    4: 'NODE_STATUS_ABNORMAL'
  };
  const key = enumMap[Number(status)] || 'NODE_STATUS_UNSPECIFIED';
  return NODE_STATUS_LABEL[key] || '未知';
};

const parseMetadata = (value: unknown): Record<string, unknown> => {
  if (!value) {
    return {};
  }
  if (typeof value === 'string') {
    try {
      const parsed = JSON.parse(value);
      return parsed && typeof parsed === 'object' ? parsed : {};
    } catch {
      return {};
    }
  }
  return typeof value === 'object' ? value as Record<string, unknown> : {};
};

const makeCompletedBatchChangeStatus = (batchId: string, batchChangeType: string, total: number): BatchChangeStatusResponse => ({
  batch_id: batchId,
  batch_change_type: batchChangeType,
  batch_change_status: BatchChangeStatus.SUCCESS,
  total_count: total,
  success_count: total,
  failed_count: 0,
  progress: 100,
  created_at: new Date().toISOString(),
  completed_time: new Date().toISOString(),
  failed_items: []
});

const completeCloudNodeBatchChange = (batchId: string, batchChangeType: string, total: number) => {
  handleBatchChangeComplete(makeCompletedBatchChangeStatus(batchId, batchChangeType, total));
};

const submitCloudNodeBatchChange = async (batchChanges: Array<{ batchChangeType: string; requestPayload: any }>) => {
  if (batchChanges.length === 0) {
    throw new Error('没有可提交的云节点批量变更');
  }
  const batchChangeType = batchChanges[0].batchChangeType;
  const first = batchChanges[0].requestPayload || {};

  if (batchChangeType === 'CREATE_NODE') {
    const nodes = batchChanges.map((batchChange, index) => {
      const params = batchChange.requestPayload || {};
      const functionNamePrefix = params.function_name_prefix || params.function_name || first.function_name_prefix || first.function_name || 'moox-cloudnode';
      return {
        cloud_account_id: params.cloud_account_id,
        node_type: params.node_type,
        region: params.region,
        namespace: params.namespace || first.namespace || 'default',
        runtime: params.runtime || first.runtime || 'Go1',
        handler: params.handler || first.handler || 'main',
        package_id: params.package_id || first.package_id,
        config: params.config,
        environment: params.environment,
        metadata: {
          ...parseMetadata(params.metadata),
          biz_type: params.biz_type,
          tag: params.tag,
          function_name_prefix: functionNamePrefix,
          timeout_threshold: params.timeout_threshold,
          heartbeat_interval: params.heartbeat_interval,
          probe_enabled: params.probe_enabled,
          index
        }
      };
    });
    const rsp = await batchCreateNodes({
      nodes,
      cloud_account_id: first.cloud_account_id,
      region: first.region,
      namespace: first.namespace || 'default',
      node_type: first.node_type,
      function_name_prefix: first.function_name_prefix || first.function_name || 'moox-cloudnode',
      runtime: first.runtime || 'Go1',
      handler: first.handler || 'main',
      package_id: first.package_id,
      count: batchChanges.length,
      config: first.config,
      environment: first.environment
    });
    if (!rsp.batch_id) {
      throw new Error('cloudnode 未返回 batch_id');
    }
    return rsp.batch_id;
  }

  if (batchChangeType === 'DELETE_NODE') {
    const rsp = await batchDeleteNodes({
      node_ids: batchChanges.map(batchChange => batchChange.requestPayload?.node_id).filter(Boolean)
    });
    if (!rsp.batch_id) {
      throw new Error('cloudnode 未返回 batch_id');
    }
    return rsp.batch_id;
  }

  if (batchChangeType === 'DEPLOY_NODE') {
    const rsp = await batchDeployNodes({
      node_ids: batchChanges.map(batchChange => batchChange.requestPayload?.node_id).filter(Boolean),
      package_id: first.package_id
    });
    if (!rsp.batch_id) {
      throw new Error('cloudnode 未返回 batch_id');
    }
    return rsp.batch_id;
  }

  throw new Error(`unsupported cloud node batch change type: ${batchChangeType}`);
};


const computeAggregateStatus = (statuses: BatchChangeViewStatus[]): BatchChangeStatusResponse => {
  const totals = statuses.reduce((acc, item) => {
    acc.total += item.total_count || 0;
    acc.success += item.success_count || 0;
    acc.failed += item.failed_count || 0;
    if (item.failed_items?.length) {
      acc.failedItems.push(...item.failed_items);
    }
    return acc;
  }, {
    total: 0,
    success: 0,
    failed: 0,
    failedItems: [] as BatchChangeDetailItem[]
  });

  let status = BatchChangeStatus.SUCCESS;
  if (totals.failed > 0) {
    status = totals.success > 0 ? BatchChangeStatus.PARTIAL : BatchChangeStatus.FAILED;
  }

  return {
    batch_id: '',
    batch_change_type: 'CREATE_NODE',
    batch_change_status: status,
    total_count: totals.total,
    success_count: totals.success,
    failed_count: totals.failed,
    progress: 100,
    created_at: new Date().toISOString(),
    completed_time: new Date().toISOString(),
    failed_items: totals.failedItems
  };
};


// 解析支持的工作负载列表
const normalizeSupportedWorkloads = (value: string[] | string | undefined): string[] => {
  if (!value) return [];
  if (Array.isArray(value)) return value;
  if (value === '[]') return [];
  try {
    const parsed = JSON.parse(value);
    return Array.isArray(parsed) ? parsed : [];
  } catch {
    return [];
  }
};

const getSupportedWorkloads = (value: string[] | string | undefined): string[] => normalizeSupportedWorkloads(value);

// 获取采集器名称
const getCollectorName = (collector: string) => {
  const nameMap: Record<string, string> = {
    'kline': 'K线',
    'ticker': '行情',
    'orderbook': '订单簿',
    'trade': '逐笔',
    'news': '资讯',
    'symbol': '标的'
  };
  return nameMap[collector] || collector;
};

// 获取采集器颜色
const getCollectorColor = (collector: string) => {
  const colorMap: Record<string, string> = {
    'kline': 'blue',
    'ticker': 'green',
    'orderbook': 'orange',
    'trade': 'purple',
    'news': 'red',
    'symbol': 'cyan'
  };
  return colorMap[collector] || 'gray';
};

const getPackageTypeColor = (packageType: number | string) => {
  const normalized = String(packageType);
  const colorMap: Record<string, string> = {
    '1': 'blue',
    '2': 'green',
    '3': 'gray',
    'PACKAGE_TYPE_COLLECTOR': 'blue',
    'PACKAGE_TYPE_FACTOR': 'green',
    'PACKAGE_TYPE_CUSTOM': 'gray',
    'data_collector': 'blue',
    'factor_calculator': 'green'
  };
  return colorMap[normalized] || 'gray';
};

const getPackageStatusColor = (status: number | string) => {
  const numeric = typeof status === 'number' ? status : Number(status);
  const colorMap: Record<number, string> = {
    1: 'blue',
    2: 'green',
    3: 'red',
    4: 'gray'
  };
  return colorMap[numeric] || 'gray';
};

const getPackageStatusLabel = (status: number | string) => {
  const numeric = typeof status === 'number' ? status : Number(status);
  const enumMap: Record<number, string> = {
    1: 'PACKAGE_STATUS_PENDING',
    2: 'PACKAGE_STATUS_AVAILABLE',
    3: 'PACKAGE_STATUS_FAILED',
    4: 'PACKAGE_STATUS_DELETED'
  };
  const key = enumMap[numeric] || 'PACKAGE_STATUS_UNSPECIFIED';
  return PACKAGE_STATUS_LABEL[key] || '未知';
};

const getPackageTypeLabel = (packageType: number | string) => {
  if (typeof packageType === 'string' && PACKAGE_TYPE_LABEL[packageType]) {
    return PACKAGE_TYPE_LABEL[packageType];
  }
  const numeric = typeof packageType === 'number' ? packageType : Number(packageType);
  const enumMap: Record<number, string> = {
    1: 'PACKAGE_TYPE_COLLECTOR',
    2: 'PACKAGE_TYPE_FACTOR',
    3: 'PACKAGE_TYPE_CUSTOM'
  };
  const key = enumMap[numeric] || 'PACKAGE_TYPE_UNSPECIFIED';
  return PACKAGE_TYPE_LABEL[key] || String(packageType);
};

const formatFileSize = (size: number) => {
  if (size < 1024) return size + 'B';
  if (size < 1024 * 1024) return (size / 1024).toFixed(1) + 'KB';
  if (size < 1024 * 1024 * 1024) return (size / (1024 * 1024)).toFixed(1) + 'MB';
  return (size / (1024 * 1024 * 1024)).toFixed(1) + 'GB';
};

const formatTime = (time: string | undefined) => {
  if (!time) return '-';
  return new Date(time).toLocaleString('zh-CN', {
    year: 'numeric',
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit'
  });
};

const formatDateTime = (dateTime: string | undefined) => {
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

const formatMetadata = (metadata: string | Record<string, unknown>) => {
  if (!metadata) return '-';
  if (typeof metadata === 'object') {
    return JSON.stringify(metadata, null, 2);
  }
  try {
    const parsed = JSON.parse(metadata);
    return JSON.stringify(parsed, null, 2);
  } catch {
    return metadata;
  }
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
  } else {
    selectedKeys.value.push(rowKey);
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
  } else {
    const currentPageKeys = cloudNodeList.value.map(item => item.node_id);
    selectedKeys.value = selectedKeys.value.filter(key => !currentPageKeys.includes(key));
  }
};

// 单个操作（保留原有实现）

const onDelete = async (record: CloudNode) => {
  try {
    const rsp = await batchDeleteNodes({ node_ids: [record.node_id] });
    if (!rsp.batch_id) {
      throw new Error('cloudnode 未返回 batch_id');
    }

    batchChangeProcessing.value = true;
    batchChangeCompleteHandled.value = false; // 重置批量变更完成处理标志
    completeCloudNodeBatchChange(rsp.batch_id, 'DELETE_NODE', 1);
  } catch (error: any) {
    Message.error('删除失败: ' + (error?.message || '未知错误'));
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

// 代码包详情
const packageDetailVisible = ref(false);
const packageDetail = ref<FunctionPackage | null>(null);
const downloadProgress = ref<Record<string, number>>({});

// 节点编辑
const editNodeVisible = ref(false);
const editNodeForm = reactive({
  nodeId: '',
  namespace: '',
  region: '',
  timeoutThreshold: 0,
  heartbeatInterval: 0,
  probeEnabled: true
});

// 显示代码包详情
const onShowPackageDetail = async (record: CloudNode) => {
  // 如果没有package_id，则不显示
  if (!record.package_id) {
    Message.warning('该节点未关联代码包');
    return;
  }
  
  packageDetail.value = null;
  packageDetailVisible.value = true;
  
  try {
    const detail = await getFunctionPackageDetail(record.package_id);
    if (detail) {
      packageDetail.value = detail;
    } else {
      throw new Error('获取详情失败');
    }
  } catch (error) {
    console.error('获取代码包详情失败:', error);
    Message.error({
      content: '获取代码包详情失败',
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
    Message.warning('只能下载可用状态的代码包');
    return;
  }
  
  try {
    downloadProgress.value[pkg.package_id] = 0;

    Message.info({
      content: '开始下载代码包...',
      duration: 2000
    });

    await downloadPackageByURL(pkg.package_id);

    downloadProgress.value[pkg.package_id] = 100;

    Message.success({
      content: '代码包下载成功',
      duration: 3000
    });

    // 3秒后清除进度
    setTimeout(() => {
      delete downloadProgress.value[pkg.package_id];
    }, 3000);
    
  } catch (error) {
    console.error('下载代码包失败:', error);
    delete downloadProgress.value[pkg.package_id];
    Message.error({
      content: '代码包下载失败',
      duration: 5000
    });
  }
};

// 批量部署弹窗取消
const handleBatchDeployCancel = () => {
  batchDeployVisible.value = false;
  // 清理表单和分页
  batchDeployForm.selectedPackageId = '';
  batchDeployForm.deployConfig = {};
  availablePackages.value = [];
  packagesPagination.value.current = 1;
  packagesPagination.value.total = 0;
};

// 批量部署弹窗确认
const handleBatchDeployOk = async () => {
  // 表单验证
  if (!batchDeployForm.selectedPackageId) {
    Message.warning('请选择要部署的代码包版本');
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
    const total = selectedKeys.value.length;
    const rsp = await batchDeployNodes({
      node_ids: selectedKeys.value,
      package_id: batchDeployForm.selectedPackageId
    });
    if (!rsp.batch_id) {
      throw new Error('cloudnode 未返回 batch_id');
    }
    
    batchChangeProcessing.value = true;
    batchChangeCompleteHandled.value = false; // 重置批量变更完成处理标志
    completeCloudNodeBatchChange(rsp.batch_id, 'DEPLOY_NODE', total);
    
    // 清理表单
    batchDeployForm.selectedPackageId = '';
    batchDeployForm.deployConfig = {};
    
  } catch (error: any) {
    console.error('创建批量部署变更失败:', error);
    Message.error('创建批量部署变更失败: ' + (error?.message || '未知错误'));
  }
};

// 单节点部署相关函数
const loadSingleDeployPackages = async () => {
  try {
    singleDeployPackagesLoading.value = true;

    const response = await getFunctionPackageList({
      page: singleDeployPackagesPagination.value.current,
      page_size: singleDeployPackagesPagination.value.pageSize,
      status: 2, // PACKAGE_STATUS_AVAILABLE
      package_type: currentPackageType.value // 根据当前路由过滤代码包类型
    });

    if (response?.items) {
      singleDeployPackages.value = response.items || [];
      singleDeployPackagesPagination.value.total = response.total || 0;
    }
  } catch (error) {
    console.error('获取代码包列表失败:', error);
    Message.error('获取代码包列表失败');
  } finally {
    singleDeployPackagesLoading.value = false;
  }
};

const onSelectSingleDeployPackage = (rowKeys: string[]) => {
  if (rowKeys.length > 0) {
    singleDeployForm.selectedPackageId = rowKeys[0];
  } else {
    singleDeployForm.selectedPackageId = '';
  }
};

const onSingleDeployPackagePageChange = (page: number) => {
  singleDeployPackagesPagination.value.current = page;
  loadSingleDeployPackages();
};

const handleSingleDeployCancel = () => {
  singleDeployVisible.value = false;
  singleDeployForm.selectedPackageId = '';
  singleDeployPackages.value = [];
  singleDeployPackagesPagination.value.current = 1;
  singleDeployPackagesPagination.value.total = 0;
};

const handleSingleDeployOk = async () => {
  // 表单验证
  if (!singleDeployForm.selectedPackageId) {
    Message.warning('请选择要部署的代码包版本');
    return;
  }
  
  // 关闭弹窗
  singleDeployVisible.value = false;
  
  try {
    const rsp = await batchDeployNodes({
      node_ids: [singleDeployForm.nodeId],
      package_id: singleDeployForm.selectedPackageId
    });
    if (!rsp.batch_id) {
      throw new Error('cloudnode 未返回 batch_id');
    }
    
    batchChangeProcessing.value = true;
    batchChangeCompleteHandled.value = false; // 重置批量变更完成处理标志
    completeCloudNodeBatchChange(rsp.batch_id, 'DEPLOY_NODE', 1);
    
    // 清理表单
    singleDeployForm.selectedPackageId = '';
    
  } catch (error: any) {
    console.error('创建部署变更失败:', error);
    Message.error('创建部署变更失败: ' + (error?.message || '未知错误'));
  }
};

// 编辑节点
const onEdit = (record: CloudNode) => {
  // 填充表单
  editNodeForm.nodeId = record.node_id;
  editNodeForm.namespace = record.namespace;
  editNodeForm.region = getRegionName(record.region);
  editNodeForm.timeoutThreshold = record.timeout_threshold || 0;
  editNodeForm.heartbeatInterval = record.heartbeat_interval || 0;
  editNodeForm.probeEnabled = record.probe_enabled;

  // 打开弹窗
  editNodeVisible.value = true;
};

// 取消编辑
const handleEditNodeCancel = () => {
  editNodeVisible.value = false;
  // 重置表单
  editNodeForm.nodeId = '';
  editNodeForm.namespace = '';
  editNodeForm.region = '';
  editNodeForm.timeoutThreshold = 0;
  editNodeForm.heartbeatInterval = 0;
  editNodeForm.probeEnabled = true;
};

// 确认编辑
const handleEditNodeOk = async () => {
  try {
    await updateNode({
      node_id: editNodeForm.nodeId,
      timeout_threshold: editNodeForm.timeoutThreshold,
      heartbeat_interval: editNodeForm.heartbeatInterval,
      probe_enabled: editNodeForm.probeEnabled,
      metadata: {
        timeout_threshold: editNodeForm.timeoutThreshold,
        heartbeat_interval: editNodeForm.heartbeatInterval,
        probe_enabled: editNodeForm.probeEnabled
      }
    });

    Message.success('节点配置更新成功');
    editNodeVisible.value = false;
    handleEditNodeCancel();
    await loadData();
  } catch (error: any) {
    console.error('更新节点配置失败:', error);
    Message.error('更新节点配置失败: ' + (error?.message || '未知错误'));
  }
};

</script>

<style scoped>
.moox-page {
  width: 100%;
  min-width: 0;
  contain: inline-size;
  overflow-x: hidden;
  overflow-y: auto;
}

.moox-page :deep(.arco-spin),
.moox-page :deep(.arco-spin-children) {
  display: block;
  width: 100%;
  min-width: 0;
  overflow-x: hidden;
}

.moox-inner {
  width: 100%;
  max-width: 100%;
  min-height: 100%;
  min-width: 0;
  box-sizing: border-box;
  overflow-x: hidden;
}

.page-head {
  margin-bottom: var(--moox-space-2);
}
.page-head h2 { margin: 0; font-size: 20px; font-weight: 600; }
.cloud-node-action-bar,
.cloud-node-filter-bar {
  display: flex;
  width: 100%;
  max-width: 100%;
  min-width: 0;
  justify-content: flex-start;
  margin-bottom: var(--moox-space-2);
}
</style>
