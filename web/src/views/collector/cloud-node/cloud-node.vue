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
          @close="selectedKeys = []"
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

        <a-form-item v-if="batchAddForm.nodeType === 'scf-event'" field="triggerType" label="触发方式" required>
          <a-select v-model="batchAddForm.triggerType" style="width: 100%">
            <a-option value="timer">定时器</a-option>
            <a-option value="invoke">手动调用</a-option>
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
        </a-form-item>

        <a-form-item field="tag" label="标签" required>
          <a-select v-model="batchAddForm.tag" :disabled="batchAddTagLocked" placeholder="请选择标签" style="width: 100%">
            <a-option v-for="tag in tagOptions" :key="tag" :value="tag">
              {{ tag }}
            </a-option>
          </a-select>
          <template #help>
            <div style="font-size: 12px; color: #86909c">选择具体地区时标签会自动锁定</div>
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
            :max="100"
            placeholder="请输入要创建的节点数量"
            style="width: 100%"
          />
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
        <div style="margin-bottom: var(--moox-space-3)">
          <div>标签：{{ batchPlanTag }}</div>
          <div>请求数量：{{ batchPlanRequested }}</div>
          <div>可用总数：{{ batchPlanTotalAvailable }}</div>
          <div>计划数量：{{ batchPlanPlannedTotal }} / {{ batchPlanTarget }}</div>
        </div>
        <a-alert v-if="batchPlanNotice" type="warning" style="margin-bottom: var(--moox-space-3)">
          {{ batchPlanNotice }}
        </a-alert>
        <a-table row-key="regionCode" :data="batchPlanItems" :bordered="{ cell: true }" :pagination="false" size="small">
          <template #columns>
            <a-table-column title="地区" data-index="regionName" :width="180" />
            <a-table-column title="最大节点数" data-index="maxNodes" :width="120" />
            <a-table-column title="已占用" data-index="usedNodes" :width="100" />
            <a-table-column title="可用" data-index="availableNodes" :width="100" />
            <a-table-column title="计划数" :width="140">
              <template #cell="{ record }">
                <a-input-number v-model="record.planCount" :min="0" :max="record.availableNodes" style="width: 120px" />
              </template>
            </a-table-column>
            <a-table-column title="操作" :width="80">
              <template #cell="{ record }">
                <a-button type="text" status="danger" size="mini" @click="removePlanItem(record)"> 删除 </a-button>
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
            <div style="margin-top: var(--moox-space-2); max-height: 120px; overflow-y: auto">
              <a-tag v-for="nodeId in selectedKeys" :key="nodeId" style="margin: var(--moox-space-1)">
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
          <a-alert type="info" style="margin-bottom: var(--moox-space-2)">
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
import {
  getFunctionPackageList,
  getFunctionPackageDetail,
  downloadPackageByURL,
  type FunctionPackage
} from "@/api/function-package";
import { getCloudAccountList, type CloudAccount } from "@/api/cloud-account";
import {
  batchDeleteNodes,
  getNodeBatchChange,
  getNodeList,
  listCloudRegions,
  submitDeployNodes,
  type GetNodeBatchChangeResponse,
  type SubmitNodeBatchResponse
} from "@/api/cloud-node";
import { useSpaceStore } from "@/store/modules/space";
import { getNodeBatchJobId, setNodeBatchJobId } from "@/utils/cloud-node-batch-change";
import CloudAccountManage from "../cloud-account/cloud-account-manage.vue";
import FunctionPackageManage from "./function-package-manage.vue";
import SCFSyncDialog from "./scf-sync-dialog.vue";
import CloudNodeTable from "./components/cloud-node-table.vue";
import { submitCloudNodeBatchChange } from "./cloud-node-batch-service";
import { createCloudNodeBatchPoller } from "./cloud-node-batch-poller";
import {
  formatDateTime,
  formatFileSize,
	formatMetadata,
	formatTime,
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
  type BatchPlanItem,
  type CloudNode,
  type RegionInfo
} from "./cloud-node-model";

// 状态管理
const spaceStore = useSpaceStore();
const { selectedSpaceId } = storeToRefs(spaceStore);
const route = useRoute();
const router = useRouter();
const loading = ref(false);
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
const cloudAccountOptions = ref<CloudAccount[]>([]);
const regionOptions = ref<RegionInfo[]>([]); // 地区选项
const scfSyncVisible = ref(false);
const REGION_UNLIMITED = "all";
const tagOptions = ["国内", "海外"];

// 批量新增相关
const batchAddVisible = ref(false);
const batchAddForm = reactive({
  cloudAccountId: "",
  nodeType: "scf-event", // 节点类型，默认为事件型云函数
  triggerType: "timer",
  runtime: "Go1", // 运行时环境，默认Go1
  handler: "main",
  region: "ap-guangzhou",
  tag: "",
  packageId: "", // 代码包版本ID
  nodeCount: 5,
  namespace: "",
  config: {} as Record<string, string>,
  environment: {} as Record<string, string>
});

const batchPlanVisible = ref(false);
const batchPlanLoading = ref(false);
const batchPlanItems = ref<BatchPlanItem[]>([]);
const batchPlanNotice = ref("");
const batchPlanRequested = ref(0);
const batchPlanTag = ref("");

// 批量部署相关
const batchDeployVisible = ref(false);
const packagesLoading = ref(false);
const availablePackages = ref<any[]>([]);
const availablePackagesForCreation = ref<any[]>([]); // 批量创建时的代码包选项
const batchDeployForm = reactive({
  selectedPackageId: "", // 选中的代码包ID
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
  nodeId: "",
  namespace: "",
  region: "",
  selectedPackageId: ""
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
const batchPlanTotalAvailable = computed(() =>
  batchPlanItems.value.reduce((sum, item) => sum + (Number(item.availableNodes) || 0), 0)
);
const batchPlanTarget = computed(() => Math.min(batchPlanRequested.value, batchPlanTotalAvailable.value));

// 当前页面是采集云节点管理页，代码包固定按采集器类型过滤。
const currentPackageType = computed(() => 1);

// 根据路由路径判断默认的节点类型
const defaultNodeType = computed(() => {
  // Collector cloud-node operations create event SCFs by default. The list
  // filter remains unselected so it can still show every node type.
  return "scf-event";
});

// 采集 SCF runtime 默认使用 Go1。
const defaultRuntime = computed(() => "Go1");

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
  batchPoller.stop();
  currentBatchChangeStatus.value = null;
  batchChangeProcessing.value = false;
  batchPollingTimedOut.value = false;
  if (getNodeBatchJobId(route.query)) {
    await replaceRouteJobId();
  }
  pagination.value.current = 1;
  selectedKeys.value = [];
  if (!spaceId) {
    cloudNodeList.value = [];
    pagination.value.total = 0;
    return;
  }
  await loadData();
});

watch(
  () => batchAddForm.nodeType,
  nodeType => {
    batchAddForm.triggerType = nodeType === "scf-event" ? batchAddForm.triggerType || "timer" : "";
  }
);

onBeforeUnmount(() => {
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
      selectedKeys.value = [];
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

const beginBatchPolling = async (response: SubmitNodeBatchResponse) => {
  if (!response.job_id) throw new Error("cloudnode 未返回 job_id");
  currentBatchChangeStatus.value = null;
  await replaceRouteJobId(response.job_id);
  await startBatchPolling(response.job_id);
};

// 关闭批量变更提示
const handleCloseBatchChangeAlert = () => {
  batchPoller.stop();
  currentBatchChangeStatus.value = null;
  batchChangeProcessing.value = false;
  batchPollingTimedOut.value = false;
  void replaceRouteJobId();
};

// 批量新增
const onBatchAdd = async () => {
  // 如果云账户列表为空，尝试重新加载
  if (cloudAccountOptions.value.length === 0) {
    await loadCloudAccounts();

    // 重新检查
    if (cloudAccountOptions.value.length === 0) {
      Message.warning("请先创建云账户");
      return;
    }
  }

  // 重置表单
  batchAddForm.cloudAccountId = cloudAccountOptions.value[0]?.account_id || "";
  batchAddForm.nodeType = defaultNodeType.value; // 根据路由设置默认节点类型
  batchAddForm.triggerType = batchAddForm.nodeType === "scf-event" ? "timer" : "";
  batchAddForm.runtime = defaultRuntime.value; // 根据路由设置默认运行时
  batchAddForm.region = "ap-hongkong";
  batchAddForm.tag = getRegionTag(batchAddForm.region) || "海外";
  batchAddForm.packageId = "";
  batchAddForm.nodeCount = 5;
  batchAddForm.namespace = "";

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
    Message.warning("请选择云账户");
    return;
  }
  if (!batchAddForm.nodeType) {
    Message.warning("请选择节点类型");
    return;
  }
  if (batchAddForm.nodeType === "scf-event" && !["timer", "invoke"].includes(batchAddForm.triggerType)) {
    Message.warning("请选择云函数触发方式");
    return;
  }
  if (!batchAddForm.runtime) {
    Message.warning("请选择运行时环境");
    return;
  }
  if (!batchAddForm.region) {
    Message.warning("请选择地区");
    return;
  }
  if (!batchAddForm.tag) {
    Message.warning("请选择标签");
    return;
  }
  if (!batchAddForm.packageId) {
    Message.warning("请选择代码包版本");
    return;
  }
  if (!batchAddForm.nodeCount || batchAddForm.nodeCount < 1) {
    Message.warning("请输入有效的节点数量");
    return;
  }
  if (batchAddForm.nodeCount > 100) {
    Message.warning("一次最多创建 100 个云节点");
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

const optionalRecord = (value: Record<string, string>) => (Object.keys(value).length > 0 ? value : undefined);

const buildCreateNodeBatchChange = (region: string, index: number) => ({
  batchChangeType: "CREATE_NODE",
  requestPayload: {
    cloud_account_id: batchAddForm.cloudAccountId,
    namespace: batchAddForm.namespace || undefined,
    node_type: batchAddForm.nodeType, // 使用表单中选择的节点类型
    trigger_type: batchAddForm.nodeType === "scf-event" ? batchAddForm.triggerType : undefined,
    runtime: batchAddForm.runtime, // 运行时环境
    handler: batchAddForm.handler || "main",
    biz_type: currentBizType.value, // 根据路由设置业务类型
    region,
    tag: getRegionTag(region) || batchAddForm.tag,
    package_id: batchAddForm.packageId,
    config: optionalRecord(batchAddForm.config),
    environment: optionalRecord(batchAddForm.environment),
    metadata: JSON.stringify({ env: "prod", index })
  }
});

const buildBatchChangesForRegion = (region: string, count: number, startIndex: number) =>
  Array(count)
    .fill(null)
    .map((_, index) => buildCreateNodeBatchChange(region, startIndex + index));

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

const executeBatchAddChanges = async (batchChanges: Array<{ batchChangeType: string; requestPayload: any }>) => {
  if (batchChanges.length === 0) {
    Message.warning("没有可执行的批量变更");
    return;
  }
  if (batchChanges.length > 100) {
    Message.warning("一次最多创建 100 个云节点");
    return;
  }

  try {
    batchChangeProcessing.value = true;
    const response = await submitCloudNodeBatchChange(batchChanges);
    await beginBatchPolling(response);
  } catch (error: any) {
    batchChangeProcessing.value = false;
    Message.error(error?.message || "创建批量变更失败");
  }
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
    Message.warning("未找到对应标签的地区");
    return;
  }

  batchPlanLoading.value = true;
  batchPlanNotice.value = "";
  batchPlanRequested.value = requestedCount;
  batchPlanTag.value = selectedTag;

  try {
    const usageList = await Promise.all(
      taggedRegions.map(async region => {
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
      })
    );

    const availableItems = usageList.filter(item => item.availableNodes > 0);
    if (availableItems.length === 0) {
      Message.warning("当前标签没有可用节点");
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
      batchPlanNotice.value = "";
    }

    batchPlanVisible.value = true;
  } catch (error) {
    console.error("生成分布计划失败:", error);
    Message.error("生成分布计划失败，请稍后重试");
  } finally {
    batchPlanLoading.value = false;
  }
};

const handleBatchPlanCancel = () => {
  batchPlanVisible.value = false;
  batchPlanItems.value = [];
  batchPlanNotice.value = "";
};

const removePlanItem = (record: BatchPlanItem) => {
  batchPlanItems.value = batchPlanItems.value.filter(item => item.regionCode !== record.regionCode);
  if (batchPlanTotalAvailable.value < batchPlanRequested.value) {
    batchPlanNotice.value = `当前可用节点总数为 ${batchPlanTotalAvailable.value}，小于需求 ${batchPlanRequested.value}，将按可用数量创建。`;
  } else {
    batchPlanNotice.value = "";
  }
};

const handleBatchPlanOk = async () => {
  if (batchPlanItems.value.length === 0) {
    Message.warning("没有可用地区");
    return;
  }
  if (batchPlanPlannedTotal.value <= 0) {
    Message.warning("计划数量不能为空");
    return;
  }
  if (batchPlanPlannedTotal.value > batchAddForm.nodeCount) {
    Message.warning("计划数量不能超过请求数量");
    return;
  }

  const overLimit = batchPlanItems.value.find(item => item.planCount > item.availableNodes);
  if (overLimit) {
    Message.warning(`地区 ${overLimit.regionName} 的计划数超过可用数量`);
    return;
  }

  if (batchPlanPlannedTotal.value < batchPlanTarget.value) {
    Message.warning("计划数量少于可创建数量，将按当前计划创建");
  }

  const batchChanges = buildBatchChangesFromPlan(batchPlanItems.value);
  batchPlanVisible.value = false;
  await executeBatchAddChanges(batchChanges);
};

// 批量部署
const batchDeploy = async () => {
  if (selectedKeys.value.length === 0) {
    Message.warning("请选择要部署的节点");
    return;
  }

  // 重置表单和分页
  batchDeployForm.selectedPackageId = "";
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
    console.error("加载代码包列表失败:", error);
    Message.error("加载代码包列表失败");
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
    console.error("加载代码包列表失败:", error);
    Message.error("加载代码包列表失败");
    availablePackagesForCreation.value = [];
  }
};

// 选择代码包
const onSelectPackage = (rowKeys: string[]) => {
  if (rowKeys.length > 0) {
    batchDeployForm.selectedPackageId = rowKeys[0];
  } else {
    batchDeployForm.selectedPackageId = "";
  }
};

// 代码包分页处理
const onPackagePageChange = (page: number) => {
  loadAvailablePackages(page);
};

// 批量删除
const batchDelete = () => {
  if (selectedKeys.value.length === 0) {
    Message.warning("请选择要删除的节点");
    return;
  }

  Modal.warning({
    title: "批量删除确认",
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
    selectedKeys.value = [];
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

    const data = response.items || [];
    cloudNodeList.value = normalizeCloudNodes(Array.isArray(data) ? data : [data].filter(Boolean));
    pagination.value.total = response.total || cloudNodeList.value.length;
    if (showEmptyTip && cloudNodeList.value.length === 0) {
      Message.info("查询结果为空");
    }
  } catch (error) {
    console.error("加载数据失败:", error);
    Message.error("加载数据失败");
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

// 根据地区代码获取标签
const getRegionTag = (region: string) => {
  if (region === REGION_UNLIMITED) {
    return "";
  }
  const regionInfo = regionOptions.value.find(r => r.code === region);
  return regionInfo ? regionInfo.tag : "";
};

watch(
  () => batchAddForm.region,
  region => {
    if (region && region !== REGION_UNLIMITED) {
      const tag = getRegionTag(region);
      if (tag) {
        batchAddForm.tag = tag;
      }
      return;
    }

    if (!batchAddForm.tag) {
      batchAddForm.tag = "海外";
    }
  }
);

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

// 批量部署弹窗取消
const handleBatchDeployCancel = () => {
  batchDeployVisible.value = false;
  // 清理表单和分页
  batchDeployForm.selectedPackageId = "";
  batchDeployForm.deployConfig = {};
  availablePackages.value = [];
  packagesPagination.value.current = 1;
  packagesPagination.value.total = 0;
};

// 批量部署弹窗确认
const handleBatchDeployOk = async () => {
  // 表单验证
  if (!batchDeployForm.selectedPackageId) {
    Message.warning("请选择要部署的代码包版本");
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
    batchChangeProcessing.value = true;
    const response = await submitDeployNodes({
      node_ids: selectedKeys.value,
      package_id: batchDeployForm.selectedPackageId
    });
    await beginBatchPolling(response);

    // 清理表单
    batchDeployForm.selectedPackageId = "";
    batchDeployForm.deployConfig = {};
  } catch (error: any) {
    batchChangeProcessing.value = false;
    console.error("创建批量部署变更失败:", error);
    Message.error("创建批量部署变更失败: " + (error?.message || "未知错误"));
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
    console.error("获取代码包列表失败:", error);
    Message.error("获取代码包列表失败");
  } finally {
    singleDeployPackagesLoading.value = false;
  }
};

const onSelectSingleDeployPackage = (rowKeys: string[]) => {
  if (rowKeys.length > 0) {
    singleDeployForm.selectedPackageId = rowKeys[0];
  } else {
    singleDeployForm.selectedPackageId = "";
  }
};

const onSingleDeployPackagePageChange = (page: number) => {
  singleDeployPackagesPagination.value.current = page;
  loadSingleDeployPackages();
};

const handleSingleDeployCancel = () => {
  singleDeployVisible.value = false;
  singleDeployForm.selectedPackageId = "";
  singleDeployPackages.value = [];
  singleDeployPackagesPagination.value.current = 1;
  singleDeployPackagesPagination.value.total = 0;
};

const handleSingleDeployOk = async () => {
  // 表单验证
  if (!singleDeployForm.selectedPackageId) {
    Message.warning("请选择要部署的代码包版本");
    return;
  }

  // 关闭弹窗
  singleDeployVisible.value = false;

  try {
    batchChangeProcessing.value = true;
    const response = await submitDeployNodes({
      node_ids: [singleDeployForm.nodeId],
      package_id: singleDeployForm.selectedPackageId
    });
    await beginBatchPolling(response);

    // 清理表单
    singleDeployForm.selectedPackageId = "";
  } catch (error: any) {
    batchChangeProcessing.value = false;
    console.error("创建部署变更失败:", error);
    Message.error("创建部署变更失败: " + (error?.message || "未知错误"));
  }
};

</script>

<style scoped src="./cloud-node.scss"></style>
