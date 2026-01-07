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
          <a-input v-model="form.nodeId" placeholder="请输入节点ID" allow-clear />
          <a-select placeholder="地区" v-model="form.region" style="width: 200px" allow-clear>
            <a-option v-for="region in regionOptions" :key="region.code" :value="region.code">
              {{ region.name }}
              <a-tag v-if="region.tag" size="small" :color="region.tag === '国内' ? 'blue' : 'orange'" style="margin-left: 4px;">
                {{ region.tag }}
              </a-tag>
            </a-option>
          </a-select>
          <a-select placeholder="节点类型" v-model="form.nodeType" style="width: 120px" allow-clear>
            <a-option value="scf">云函数</a-option>
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
              <span>代码包版本</span>
            </a-button>
          </a-space>
        </a-row>

        <!-- 任务进度提示 -->
        <a-alert
          v-if="batchJobStatuses.length > 0"
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
          <div v-for="(job, index) in batchJobStatuses" :key="job.task_id || index" style="margin-bottom: 12px;">
            <div>批次 {{ index + 1 }}：{{ getTaskTypeText(job.task_type) }}</div>
            <div>处理进度：{{ job.success_count + job.failed_count }} / {{ job.total_count }}</div>
            <div>成功：{{ job.success_count }}，失败：{{ job.failed_count }}</div>
            <a-progress 
              :percent="(Number(job.progress) || 0) / 100" 
              :status="job.failed_count > 0 ? 'warning' : 'normal'"
              :stroke-width="8"
              style="margin-top: 8px"
            />
          </div>
        </a-alert>
        <a-alert
          v-else-if="currentTaskStatus && currentTaskStatus.task_status === 1"
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
            <a-table-column title="最后心跳时间" data-index="last_heartbeat" :width="170">
              <template #cell="{ record }">
                {{ formatDateTime(record.last_heartbeat) }}
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
            <a-table-column title="支持的采集器" data-index="supported_collectors" :width="200">
              <template #cell="{ record }">
                <div v-if="getSupportedCollectors(record.supported_collectors).length > 0" style="display: flex; flex-wrap: wrap; gap: 4px;">
                  <a-tag
                    v-for="(collector, index) in getSupportedCollectors(record.supported_collectors)"
                    :key="index"
                    size="small"
                    :color="getCollectorColor(collector)"
                  >
                    {{ getCollectorName(collector) }}
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
            <a-table-column title="状态" :width="100" align="center">
              <template #cell="{ record }">
                <a-tag bordered size="small" 
                  :color="getStatusColor(record.status)">
                  {{ getStatusText(record.status) }}
                </a-tag>
              </template>
            </a-table-column>
            <a-table-column title="操作" :width="250" align="center" fixed="right">
              <template #cell="{ record }">
                <a-space>
                  <a-button type="outline" size="mini" @click="onEdit(record)" :disabled="taskPolling">
                    <template #icon><icon-edit /></template>
                    <span>编辑</span>
                  </a-button>
                  <a-button v-if="record.node_type === 'scf'" type="primary" size="mini" @click="onDeploy(record)" :disabled="taskPolling">
                    <template #icon><icon-upload /></template>
                    <span>部署</span>
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
            :data="availablePackages"
            :loading="packagesLoading"
            :pagination="packagesPagination"
            :scroll="{ y: 300 }"
            :row-selection="{ type: 'radio', showCheckedAll: false }"
            :selected-keys="batchDeployForm.selectedPackageId ? [batchDeployForm.selectedPackageId] : []"
            @select="onSelectPackage"
            @page-change="onPackagePageChange"
            size="small"
          >
            <template #columns>
              <a-table-column title="代码包名称" data-index="package_name" :width="140"></a-table-column>
              <a-table-column title="版本" data-index="version" :width="100"></a-table-column>
              <a-table-column title="类型" data-index="package_type_label" :width="120">
                <template #cell="{ record }">
                  <a-tag :color="getPackageTypeColor(record.package_type)" size="small">
                    {{ record.package_type_label }}
                  </a-tag>
                </template>
              </a-table-column>
              <a-table-column title="运行时" data-index="runtime" :width="100"></a-table-column>
              <a-table-column title="文件大小" data-index="file_size" :width="100">
                <template #cell="{ record }">
                  {{ formatFileSize(record.file_size) }}
                </template>
              </a-table-column>
              <a-table-column title="创建时间" data-index="created_at" :width="150">
                <template #cell="{ record }">
                  {{ formatTime(record.created_at) }}
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
            :data="singleDeployPackages"
            :loading="singleDeployPackagesLoading"
            :pagination="singleDeployPackagesPagination"
            :scroll="{ y: 300 }"
            :row-selection="{ type: 'radio', showCheckedAll: false }"
            :selected-keys="singleDeployForm.selectedPackageId ? [singleDeployForm.selectedPackageId] : []"
            @select="onSelectSingleDeployPackage"
            @page-change="onSingleDeployPackagePageChange"
            size="small"
          >
            <template #columns>
              <a-table-column title="代码包名称" data-index="package_name" :width="140"></a-table-column>
              <a-table-column title="版本" data-index="version" :width="100"></a-table-column>
              <a-table-column title="类型" data-index="package_type_label" :width="120">
                <template #cell="{ record }">
                  <a-tag :color="getPackageTypeColor(record.package_type)" size="small">
                    {{ record.package_type_label }}
                  </a-tag>
                </template>
              </a-table-column>
              <a-table-column title="运行时" data-index="runtime" :width="100"></a-table-column>
              <a-table-column title="文件大小" data-index="file_size" :width="100">
                <template #cell="{ record }">
                  {{ formatFileSize(record.file_size) }}
                </template>
              </a-table-column>
              <a-table-column title="创建时间" data-index="created_at" :width="150">
                <template #cell="{ record }">
                  {{ formatTime(record.created_at) }}
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
          <a-descriptions-item label="标签">
            <a-tag v-if="selectedNodeDetail.tag" size="small" :color="selectedNodeDetail.tag === '国内' ? 'blue' : 'orange'">
              {{ selectedNodeDetail.tag }}
            </a-tag>
            <span v-else>-</span>
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
              {{ packageDetail.package_type_label }}
            </a-tag>
          </a-descriptions-item>
          <a-descriptions-item label="运行时环境">{{ packageDetail.runtime }}</a-descriptions-item>
          <a-descriptions-item label="文件大小">{{ formatFileSize(packageDetail.file_size) }}</a-descriptions-item>
          <a-descriptions-item label="状态">
            <a-tag :color="getPackageStatusColor(packageDetail.status)">
              {{ packageDetail.status_label }}
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
          <a-descriptions-item label="创建时间">{{ formatDateTime(packageDetail.created_at) }}</a-descriptions-item>
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
              :disabled="packageDetail.status !== 1"
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
import { Message, Modal } from '@arco-design/web-vue';
import { api } from '@/api/config';
import { getFunctionPackageList, getFunctionPackageDetail, downloadPackageByURL, type FunctionPackage } from '@/api/function-package';
import { AsyncTaskManager, asyncTaskManager, TaskStatus } from '@/utils/async-task';
import type { TaskStatusResponse, TaskDetailItem } from '@/utils/async-task';
import CloudAccountManage from '../cloud-account/cloud-account-manage.vue';
import FunctionPackageManage from './function-package-manage.vue';

// 接口定义
interface CloudFunction {
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
  supported_collectors: string; // 支持的采集器类型（JSON数组格式）
  capacity: string;
  current_load: string;
  metadata: string;
  status: string;
  enabled: number;
  // 新增心跳配置字段
  timeout_threshold: number;    // 超时阈值（秒），0表示使用全局默认值
  heartbeat_interval: number;   // 心跳间隔（秒），0表示使用全局默认值
  probe_enabled: boolean;       // 是否启用探测
  probe_url?: string;           // 探测URL
  last_heartbeat?: string;
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
const taskCompleteHandled = ref(false); // 防止重复处理任务完成

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

type BatchJobStatus = TaskStatusResponse & {
  batchIndex: number;
};

// 数据列表
const functionList = ref<CloudFunction[]>([]);
const selectedKeys = ref<string[]>([]);
const cloudAccountOptions = ref<CloudAccount[]>([]);
const regionOptions = ref<RegionInfo[]>([]); // 地区选项
const REGION_UNLIMITED = 'all';
const tagOptions = ['国内', '海外'];

// 批量新增相关
const batchAddVisible = ref(false);
const batchAddForm = reactive({
  cloudAccountId: '',
  region: 'ap-guangzhou',
  tag: '',
  packageId: '', // 代码包版本ID
  nodeCount: 5,
  namespace: '',
  // 新增心跳配置字段
  timeoutThreshold: 0,    // 超时阈值（秒），0表示使用全局默认值
  heartbeatInterval: 0,   // 心跳间隔（秒），0表示使用全局默认值
  probeEnabled: true      // 是否启用探测，默认启用
});

const batchPlanVisible = ref(false);
const batchPlanLoading = ref(false);
const batchPlanItems = ref<BatchPlanItem[]>([]);
const batchPlanNotice = ref('');
const batchPlanRequested = ref(0);
const batchPlanTag = ref('');
const batchJobStatuses = ref<BatchJobStatus[]>([]);
let batchJobTimer: number | null = null;

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

// 生命周期钩子
onMounted(async () => {
  await loadData();
  await loadCloudAccounts();
  await loadRegions(); // 加载地区列表
  
  // 检查并恢复任务状态
  await asyncTaskManager.checkAndRestoreTask(handleTaskRestore);
});

onBeforeUnmount(() => {
  // 清理轮询
  asyncTaskManager.stopPolling();
  stopBatchJobPolling();
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
    taskCompleteHandled.value = false; // 重置任务完成处理标志
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
  stopBatchJobPolling();
  batchJobStatuses.value = [];
  console.log('handleTaskComplete called with data:', data);
  console.log('Task status:', data.task_status, 'Failed count:', data.failed_count);
  
  // 防止重复处理
  if (taskCompleteHandled.value) {
    console.log('Task completion already handled, skipping...');
    return;
  }
  taskCompleteHandled.value = true;
  
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
    console.log('Showing result modal, failed_count:', data.failed_count);
    // 检查是否有失败项（通过failed_count判断）
    if (data.failed_count > 0) {
    // 有失败项，使用 Modal.error 显示失败详情
    const failedItems = data.failed_items || [];
    console.log('Failed items:', failedItems);
    
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
  stopBatchJobPolling();
  batchJobStatuses.value = [];
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

const buildCreateNodeTask = (region: string, index: number) => ({
  taskType: 'CREATE_NODE',
  requestParams: {
    cloud_account_id: batchAddForm.cloudAccountId,
    namespace: batchAddForm.namespace || undefined,
    node_type: 'scf',
    region,
    tag: getRegionTag(region) || batchAddForm.tag,
    package_id: batchAddForm.packageId,
    version: '1.0.0',
    capacity: '100',
    metadata: JSON.stringify({ env: 'prod', index }),
    // 新增心跳配置字段
    timeout_threshold: batchAddForm.timeoutThreshold,
    heartbeat_interval: batchAddForm.heartbeatInterval,
    probe_enabled: batchAddForm.probeEnabled
  }
});

const buildTasksForRegion = (region: string, count: number, startIndex: number) => (
  Array(count).fill(null).map((_, index) => buildCreateNodeTask(region, startIndex + index))
);

const buildTasksFromPlan = (items: BatchPlanItem[]) => {
  const tasks: Array<{ taskType: string; requestParams: any }> = [];
  let index = 0;
  items.forEach(item => {
    const count = Number(item.planCount) || 0;
    for (let i = 0; i < count; i += 1) {
      tasks.push(buildCreateNodeTask(item.regionCode, index));
      index += 1;
    }
  });
  return tasks;
};

const chunkTasks = <T,>(items: T[], size: number): T[][] => {
  const chunks: T[][] = [];
  for (let i = 0; i < items.length; i += size) {
    chunks.push(items.slice(i, i + size));
  }
  return chunks;
};

const executeBatchAddTasks = async (tasks: Array<{ taskType: string; requestParams: any }>) => {
  if (tasks.length === 0) {
    Message.warning('没有可执行的任务');
    return;
  }

  const chunks = chunkTasks(tasks, 100);

  taskPolling.value = true;
  taskCompleteHandled.value = false;
  currentTaskStatus.value = null;

  const initialStatuses: BatchJobStatus[] = chunks.map((chunk, index) => ({
    batchIndex: index,
    task_id: '',
    task_type: 'CREATE_NODE',
    task_status: TaskStatus.PROCESSING,
    total_count: chunk.length,
    success_count: 0,
    failed_count: 0,
    progress: 0,
    created_at: new Date().toISOString()
  }));
  batchJobStatuses.value = initialStatuses;

  const jobIds: string[] = [];

  for (let i = 0; i < chunks.length; i += 1) {
    const chunk = chunks[i];
    try {
      const jobId = await createAsyncJob(chunk);
      jobIds.push(jobId);
      batchJobStatuses.value = batchJobStatuses.value.map(item => (
        item.batchIndex === i ? { ...item, task_id: jobId } : item
      ));
    } catch (error: any) {
      batchJobStatuses.value = batchJobStatuses.value.map(item => (
        item.batchIndex === i ? {
          ...item,
          task_status: TaskStatus.FAILED,
          failed_count: item.total_count,
          progress: 100,
          error_message: error?.message || '创建任务失败'
        } : item
      ));
    }
  }

  if (jobIds.length === 0) {
    const finalStatus = computeAggregateStatus(batchJobStatuses.value);
    batchJobStatuses.value = [];
    handleTaskComplete(finalStatus);
    return;
  }

  startBatchJobPolling(jobIds);
};

const executeBatchAddDirect = async () => {
  const tasks = buildTasksForRegion(batchAddForm.region, batchAddForm.nodeCount, 0);
  await executeBatchAddTasks(tasks);
};

const fetchRegionUsage = async (regionCode: string, tag: string) => {
  const response = await api.post('/cloudnode/GetNodeList', {
    region: regionCode,
    tag,
    node_type: 'scf',
    page: 1,
    page_size: 1
  });

  if (response.data?.code === 200) {
    return Number(response.data.total || 0);
  }

  if (response.data?.ret_info?.code === 0) {
    return Number(response.data.ret_info.total || 0);
  }

  throw new Error('获取地区占用失败');
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

  const tasks = buildTasksFromPlan(batchPlanItems.value);
  batchPlanVisible.value = false;
  await executeBatchAddTasks(tasks);
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
      status: 1 // 只获取可用状态的代码包
    });
    
    if (response?.code === 200 && response?.data) {
      // 按时间倒序排列
      availablePackages.value = (response.data || []).sort((a: any, b: any) => {
        return new Date(b.created_at).getTime() - new Date(a.created_at).getTime();
      });
      
      // 更新分页信息
      packagesPagination.value.current = page;
      packagesPagination.value.total = response.total || response.data.length;
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
      status: 1 // 只获取可用状态的代码包
    });
    
    if (response?.code === 200 && response?.data) {
      // 按时间倒序排列
      availablePackagesForCreation.value = (response.data || []).sort((a: any, b: any) => {
        return new Date(b.created_at).getTime() - new Date(a.created_at).getTime();
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
  // 准备多个独立任务的数据
  const tasks = selectedKeys.value.map(nodeId => ({
    taskType: 'DELETE_NODE',
    requestParams: {
      node_id: nodeId
    }
  }));

  try {
    // 创建多个独立任务的异步任务
    const taskId = await asyncTaskManager.createMultipleAsyncTasks(tasks);

    taskPolling.value = true;
    taskCompleteHandled.value = false; // 重置任务完成处理标志
    
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

// 加载数据（使用后端分页）
const loadData = async (showEmptyTip = false) => {
  loading.value = true;
  try {
    const response = await api.post('/cloudnode/GetNodeList', {
      node_id: form.nodeId,
      cloud_account_id: form.cloudAccountId,
      region: form.region,
      node_type: form.nodeType,
      status: form.status,
      page: pagination.value.current,
      page_size: pagination.value.pageSize
    });

    // 兼容两种响应格式
    if (response.data?.code === 200) {
      // 新格式：处理数组格式的响应
      let data = response.data.data;
      if (Array.isArray(data)) {
        functionList.value = data;
      } else {
        functionList.value = [data].filter(Boolean);
      }
      // 使用后端返回的 total
      pagination.value.total = response.data.total || functionList.value.length;
      if (showEmptyTip && functionList.value.length === 0) {
        Message.info('查询结果为空');
      }
    } else if (response.data?.ret_info?.code === 0) {
      // 旧格式
      let data = response.data.ret_info.data;
      if (Array.isArray(data)) {
        functionList.value = data;
      } else {
        functionList.value = [data].filter(Boolean);
      }
      pagination.value.total = response.data.ret_info.total || functionList.value.length;
      if (showEmptyTip && functionList.value.length === 0) {
        Message.info('查询结果为空');
      }
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
    const response = await api.post('/cloudnode/ListCloudAccounts', {});
    
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
      Message.error('加载云账户失败，请点击"云账户管理按钮"，新增云账户');
    }
  } catch (error) {
    console.error('加载云账户失败:', error);
    Message.error('加载云账户失败，请检查网络连接');
  }
};

// 加载地区列表
const loadRegions = async () => {
  try {
    const response = await api.post('/cloudnode/ListCloudRegions', {
      provider: 'tencent' // 目前只支持腾讯云
    });
    
    if (response.data?.code === 200 && response.data?.data) {
      let data = response.data.data;
      if (Array.isArray(data)) {
        regionOptions.value = data;
      } else {
        regionOptions.value = [data].filter(Boolean);
      }
    } else {
      console.error('加载地区列表失败:', response);
      // 失败时使用空数组
      regionOptions.value = [];
    }
  } catch (error) {
    console.error('加载地区列表失败:', error);
    // 失败时使用空数组
    regionOptions.value = [];
  }
};

// 工具函数
const getTaskTypeText = (taskType: string) => {
  const typeMap: Record<string, string> = {
    'CREATE_NODE': '批量创建节点',
    'BATCH_UPDATE_NODE': '批量更新节点',
    'DELETE_NODE': '批量删除节点',
    'DEPLOY_NODE': '批量部署节点'
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
  if (status === 'online') {
    return 'green';
  }
  if (status === 'offline') {
    return 'red';
  }
  if (status === 1) {
    return 'green';
  }
  if (status === 0) {
    return 'red';
  }
  return 'gray';
};

const getStatusText = (status: string | number) => {
  if (typeof status === 'string' && status) {
    if (status === 'online') {
      return '在线';
    }
    if (status === 'offline') {
      return '离线';
    }
    return status;
  }
  if (status === 1) {
    return '在线';
  }
  if (status === 0) {
    return '离线';
  }
  return '未知';
};

const mapJobStatusToTaskStatus = (jobStatus: number): TaskStatus => {
  switch (jobStatus) {
    case 0:
      return TaskStatus.PROCESSING;
    case 1:
      return TaskStatus.PROCESSING;
    case 2:
      return TaskStatus.SUCCESS;
    case 3:
      return TaskStatus.FAILED;
    case 4:
      return TaskStatus.PARTIAL;
    default:
      return TaskStatus.PROCESSING;
  }
};

const extractFailedItems = (tasks: any[]): TaskDetailItem[] => {
  if (!Array.isArray(tasks)) {
    return [];
  }
  return tasks
    .filter(task => task.task_status === 3)
    .map(task => ({
      item_id: task.task_id,
      item_name: task.task_type,
      status: task.task_status,
      error_message: task.error_message
    }));
};

const createAsyncJob = async (tasks: Array<{ taskType: string; requestParams: any }>) => {
  const response = await api.post('/asynctask/CreateAsyncJob', {
    tasks: tasks.map(task => ({
      task_type: task.taskType,
      request_params: task.requestParams
    }))
  }, {
    timeout: 20000
  });

  if (response.data?.code !== 200) {
    throw new Error(response.data?.message || '创建任务失败');
  }

  let jobData = response.data?.data;
  if (Array.isArray(jobData) && jobData.length > 0) {
    jobData = jobData[0];
  }

  const jobId = jobData?.job_id;
  if (!jobId) {
    throw new Error('服务器未返回job_id');
  }

  return jobId as string;
};

const queryAsyncJob = async (jobId: string): Promise<TaskStatusResponse | null> => {
  try {
    const response = await api.post('/asynctask/QueryAsyncJob', {
      job_id: jobId
    });

    if (response.data?.code !== 200) {
      // 请求成功但业务失败，返回null表示查询失败，继续重试
      console.warn('queryAsyncJob 业务失败:', response.data?.message);
      return null;
    }

    let jobData = response.data?.data;
    if (Array.isArray(jobData) && jobData.length > 0) {
      jobData = jobData[0];
    }

    return {
      task_id: jobData?.job_id || jobId,
      task_type: jobData?.tasks?.[0]?.task_type || 'UNKNOWN',
      task_status: mapJobStatusToTaskStatus(jobData?.job_status),
      total_count: jobData?.total_task_cnt || 0,
      success_count: jobData?.success_task_cnt || 0,
      failed_count: jobData?.failed_task_cnt || 0,
      progress: jobData?.progress || 0,
      error_message: jobData?.tasks?.[0]?.error_message,
      created_at: jobData?.created_at || new Date().toISOString(),
      completed_time: jobData?.updated_at,
      failed_items: extractFailedItems(jobData?.tasks)
    };
  } catch (error: any) {
    // 网络超时或请求失败，返回null继续重试，不弹窗
    console.warn('queryAsyncJob 请求失败，将继续重试:', error?.message);
    return null;
  }
};

const stopBatchJobPolling = () => {
  if (batchJobTimer) {
    clearInterval(batchJobTimer);
    batchJobTimer = null;
  }
};

const computeAggregateStatus = (statuses: BatchJobStatus[]): TaskStatusResponse => {
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
    failedItems: [] as TaskDetailItem[]
  });

  let status = TaskStatus.SUCCESS;
  if (totals.failed > 0) {
    status = totals.success > 0 ? TaskStatus.PARTIAL : TaskStatus.FAILED;
  }

  return {
    task_id: '',
    task_type: 'CREATE_NODE',
    task_status: status,
    total_count: totals.total,
    success_count: totals.success,
    failed_count: totals.failed,
    progress: 100,
    created_at: new Date().toISOString(),
    completed_time: new Date().toISOString(),
    failed_items: totals.failedItems
  };
};

const startBatchJobPolling = (_jobIds: string[]) => {
  stopBatchJobPolling();

  const pollOnce = async () => {
    const pendingJobs = batchJobStatuses.value.filter(job => job.task_status === TaskStatus.PROCESSING && job.task_id);
    if (pendingJobs.length === 0) {
      stopBatchJobPolling();
      const finalStatus = computeAggregateStatus(batchJobStatuses.value);
      batchJobStatuses.value = [];
      handleTaskComplete(finalStatus);
      return;
    }

    const updates = await Promise.all(pendingJobs.map(async (job) => {
      const status = await queryAsyncJob(job.task_id);
      // 如果查询失败（返回null），保持原状态继续轮询
      if (status === null) {
        return { batchIndex: job.batchIndex, status: null };
      }
      return { batchIndex: job.batchIndex, status };
    }));

    const nextStatuses = batchJobStatuses.value.map(job => {
      const update = updates.find(item => item.batchIndex === job.batchIndex);
      // 如果没有更新或状态为null，保持原状态
      if (!update || update.status === null) {
        return job;
      }
      return {
        ...job,
        ...update.status,
        batchIndex: job.batchIndex
      };
    });

    batchJobStatuses.value = nextStatuses;
  };

  pollOnce();
  batchJobTimer = window.setInterval(pollOnce, 2000);
};

// 解析支持的采集器列表
const getSupportedCollectors = (supportedCollectorsStr: string): string[] => {
  if (!supportedCollectorsStr || supportedCollectorsStr === '[]') {
    return [];
  }
  try {
    const collectors = JSON.parse(supportedCollectorsStr);
    return Array.isArray(collectors) ? collectors : [];
  } catch (error) {
    console.error('解析 supported_collectors 失败:', error);
    return [];
  }
};

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

const getPackageTypeColor = (packageType: string) => {
  const colorMap: Record<string, string> = {
    'data_collector': 'blue',
    'factor_calculator': 'green'
  };
  return colorMap[packageType] || 'gray';
};

const getPackageStatusColor = (status: number) => {
  const colorMap: Record<number, string> = {
    0: 'blue',       // 上传中 - 蓝色
    1: 'green',      // 可用 - 绿色
    2: 'gray',       // 已删除 - 灰色
    3: 'red'         // 上传失败 - 红色
  };
  return colorMap[status] || 'gray';
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

// 查询和重置
const search = () => {
  pagination.value.current = 1;
  loadData(true);
};

const reset = () => {
  form.cloudAccountId = '';
  form.nodeId = '';
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

const onDelete = async (record: CloudFunction) => {
  try {
    // 创建单个删除的异步任务
    const taskId = await asyncTaskManager.createAsyncTask('DELETE_NODE', {
      node_id: record.node_id
    });

    taskPolling.value = true;
    taskCompleteHandled.value = false; // 重置任务完成处理标志
    
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

const onDeploy = async (record: CloudFunction) => {
  // 填充表单
  singleDeployForm.nodeId = record.node_id;
  singleDeployForm.namespace = record.namespace;
  singleDeployForm.region = getRegionName(record.region);
  singleDeployForm.selectedPackageId = '';
  
  // 打开弹窗
  singleDeployVisible.value = true;
  
  // 加载代码包列表
  await loadSingleDeployPackages();
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

// 代码包版本管理
const functionPackageManageVisible = ref(false);

const onFunctionPackageManage = () => {
  functionPackageManageVisible.value = true;
};

// 节点详情
const nodeDetailVisible = ref(false);
const selectedNodeDetail = ref<CloudFunction | null>(null);

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
const onShowPackageDetail = async (record: CloudFunction) => {
  // 如果没有package_id，则不显示
  if (!record.package_id) {
    Message.warning('该节点未关联代码包');
    return;
  }
  
  packageDetail.value = null;
  packageDetailVisible.value = true;
  
  try {
    const response = await getFunctionPackageDetail(record.package_id);
    console.log('代码包详情API响应:', response);
    
    if (response?.code === 200 && response?.data && response.data.length > 0) {
      packageDetail.value = response.data[0]; // 取数组第一个元素
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
  if (pkg.status !== 1) {
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
    // 准备多个独立任务的数据
    const tasks = selectedKeys.value.map(nodeId => ({
      taskType: 'DEPLOY_NODE',
      requestParams: {
        node_id: nodeId,
        package_id: batchDeployForm.selectedPackageId
      }
    }));
    
    // 创建多个独立任务的异步任务
    const taskId = await asyncTaskManager.createMultipleAsyncTasks(tasks);
    
    taskPolling.value = true;
    taskCompleteHandled.value = false; // 重置任务完成处理标志
    
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
    
    // 清理表单
    batchDeployForm.selectedPackageId = '';
    batchDeployForm.deployConfig = {};
    
  } catch (error: any) {
    console.error('创建批量部署任务失败:', error);
    Message.error('创建批量部署任务失败: ' + (error?.message || '未知错误'));
  }
};

// 单节点部署相关函数
const loadSingleDeployPackages = async () => {
  try {
    singleDeployPackagesLoading.value = true;
    
    const response = await getFunctionPackageList({
      page: singleDeployPackagesPagination.value.current,
      page_size: singleDeployPackagesPagination.value.pageSize,
      status: 1 // 只获取可用状态的包
    });
    
    if (response?.code === 200 && response?.data) {
      singleDeployPackages.value = response.data || [];
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
    // 创建单个部署的异步任务
    const taskId = await asyncTaskManager.createAsyncTask('DEPLOY_NODE', {
      node_id: singleDeployForm.nodeId,
      package_id: singleDeployForm.selectedPackageId
    });
    
    taskPolling.value = true;
    taskCompleteHandled.value = false; // 重置任务完成处理标志
    
    // 开始轮询任务状态
    asyncTaskManager.startPolling(taskId, {
      onProgress: (data) => {
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
    
    // 清理表单
    singleDeployForm.selectedPackageId = '';
    
  } catch (error: any) {
    console.error('创建部署任务失败:', error);
    Message.error('创建部署任务失败: ' + (error?.message || '未知错误'));
  }
};

// 编辑节点
const onEdit = (record: CloudFunction) => {
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
    // 调用更新API
    const response = await api.post('/cloudnode/UpdateNode', {
      node_id: editNodeForm.nodeId,
      timeout_threshold: editNodeForm.timeoutThreshold,
      heartbeat_interval: editNodeForm.heartbeatInterval,
      probe_enabled: editNodeForm.probeEnabled
    });
    
    if (response.data?.code === 200 || response.data?.ret_info?.code === 0) {
      Message.success('节点配置更新成功');
      editNodeVisible.value = false;
      handleEditNodeCancel();
      // 刷新数据
      await loadData();
    } else {
      throw new Error(response.data?.message || response.data?.ret_info?.message || '更新失败');
    }
  } catch (error: any) {
    console.error('更新节点配置失败:', error);
    Message.error('更新节点配置失败: ' + (error?.message || '未知错误'));
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
