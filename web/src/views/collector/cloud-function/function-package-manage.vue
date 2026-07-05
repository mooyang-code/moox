<template>
  <component
    :is="packageManageContainer"
    v-bind="packageManageContainerProps"
    @update:visible="handlePackageManageVisibleChange"
    @cancel="handleCancel"
  >
    <div class="function-package-manage">
      <!-- 搜索区域 -->
      <div class="package-toolbar">
        <a-space class="package-filters" wrap>
          <a-input
            v-model="searchForm.package_name"
            placeholder="搜索代码包名称"
            style="width: 200px"
            allow-clear
          />
          <a-select
            v-model="searchForm.runtime"
            placeholder="运行时环境"
            style="width: 150px"
            allow-clear
          >
            <a-option v-for="runtime in RUNTIME_OPTIONS" :key="runtime.value" :value="runtime.value">
              {{ runtime.label }}
            </a-option>
          </a-select>
          <a-select
            v-model="searchForm.status"
            placeholder="状态"
            style="width: 120px"
            allow-clear
          >
            <a-option v-for="status in STATUS_OPTIONS" :key="status.value" :value="status.value">
              {{ status.label }}
            </a-option>
          </a-select>
          <a-button type="primary" @click="loadPackageList">
            <template #icon><icon-search /></template>
            搜索
          </a-button>
          <a-button @click="resetSearch">
            <template #icon><icon-refresh /></template>
            重置
          </a-button>
        </a-space>
        <a-button class="package-upload-button" type="primary" @click="onAdd">
          <template #icon><icon-plus /></template>
          上传代码包
        </a-button>
      </div>

      <a-table
        row-key="id"
        :data="packageList"
        :bordered="{ cell: true }"
        :loading="loading"
        :pagination="pagination"
        @page-change="handlePageChange"
        @page-size-change="handlePageSizeChange"
      >
        <template #columns>
          <a-table-column title="代码包名称" data-index="package_name" :width="180">
            <template #cell="{ record }">
              <a-button type="text" style="padding: 0; height: auto; color: #165dff;" @click="onShowDetail(record)">
                {{ record.package_name }}
              </a-button>
            </template>
          </a-table-column>
          <a-table-column title="版本" data-index="version" :width="120"></a-table-column>
          <a-table-column title="类型" data-index="package_type_label" :width="150">
            <template #cell="{ record }">
              <a-tag :color="getPackageTypeColor(record.package_type)">
                {{ getPackageTypeLabel(record.package_type) }}
              </a-tag>
            </template>
          </a-table-column>
          <a-table-column title="运行时" data-index="runtime" :width="120"></a-table-column>
          <a-table-column title="文件大小" data-index="file_size" :width="120">
            <template #cell="{ record }">
              {{ formatFileSize(record.file_size) }}
            </template>
          </a-table-column>
          <a-table-column title="状态" data-index="status" :width="100">
            <template #cell="{ record }">
              <a-tag :color="getStatusColor(record.status)">
                {{ getPackageStatusLabel(record.status) }}
              </a-tag>
            </template>
          </a-table-column>
          <a-table-column title="创建时间" data-index="created_time" :width="180">
            <template #cell="{ record }">
              {{ formatTime(record.created_time) }}
            </template>
          </a-table-column>
          <a-table-column title="描述" data-index="description" :width="200">
            <template #cell="{ record }">
              <a-tooltip :content="record.description" position="top">
                <span>{{ record.description ? (record.description.length > 20 ? record.description.substring(0, 20) + '...' : record.description) : '-' }}</span>
              </a-tooltip>
            </template>
          </a-table-column>
          <a-table-column title="云账户" data-index="cloud_account_id" :width="120">
            <template #cell="{ record }">
              {{ record.cloud_account_id || '-' }}
            </template>
          </a-table-column>
          <a-table-column title="COS地区" data-index="cos_region" :width="120">
            <template #cell="{ record }">
              {{ record.cos_region || '-' }}
            </template>
          </a-table-column>
          <a-table-column title="文件MD5" data-index="file_md5" :width="180">
            <template #cell="{ record }">
              <a-tooltip :content="record.file_md5" position="top">
                <span>{{ record.file_md5 ? (record.file_md5.length > 16 ? record.file_md5.substring(0, 16) + '...' : record.file_md5) : '-' }}</span>
              </a-tooltip>
            </template>
          </a-table-column>
          <a-table-column title="操作" :width="180" align="center" fixed="right">
            <template #cell="{ record }">
              <a-space>
                <a-button 
                  type="primary"
                  size="mini"
                  status="success"
                  @click="onDownload(record)"
                  :disabled="record.status !== 1"
                  :loading="downloadProgress[record.package_id] !== undefined && downloadProgress[record.package_id] < 100"
                >
                  <template #icon>
                    <icon-download />
                  </template>
                  <span v-if="downloadProgress[record.package_id] !== undefined && downloadProgress[record.package_id] < 100">
                    下载中...
                  </span>
                  <span v-else>下载</span>
                </a-button>
                <a-popconfirm
                  content="确定要删除该代码包吗？删除后无法恢复。"
                  ok-text="确定"
                  cancel-text="取消"
                  @ok="() => onDelete(record)"
                  position="tr"
                >
                  <a-button type="primary" size="mini" status="danger">
                    <template #icon><icon-delete /></template>
                    删除
                  </a-button>
                </a-popconfirm>
              </a-space>
            </template>
          </a-table-column>
        </template>
      </a-table>
    </div>
    
    <!-- 上传弹窗 -->
    <a-modal
      v-model:visible="uploadVisible"
      title="上传云函数代码包"
      :width="600"
      :mask-closable="false"
      @cancel="handleUploadCancel"
    >
      <a-form :model="uploadForm" layout="vertical" ref="uploadFormRef">
        <a-form-item field="package_type" label="函数包类型" required>
          <a-select v-model="uploadForm.package_type" placeholder="请选择函数包类型" @change="onPackageTypeChange" :disabled="!!props.packageType">
            <a-option v-for="type in PACKAGE_TYPE_OPTIONS" :key="type.value" :value="type.value">
              {{ type.label }}
            </a-option>
          </a-select>
        </a-form-item>
        
        
        <a-form-item
          field="version"
          label="版本号"
          required
          :status="versionValidationStatus"
          :feedback="versionFeedback"
        >
          <a-input
            v-model="uploadForm.version"
            placeholder="将从上传的文件名中自动解析"
            readonly
            disabled
          />
          <template #extra>
            <span style="color: #86909c; font-size: 12px;">
              版本号将从上传的文件名中自动解析（格式：xxx-v1.0.45.zip）
            </span>
          </template>
        </a-form-item>
        
        <a-form-item field="runtime" label="运行时环境" required>
          <a-select v-model="uploadForm.runtime" placeholder="请选择运行时环境">
            <a-option v-for="runtime in RUNTIME_OPTIONS" :key="runtime.value" :value="runtime.value">
              {{ runtime.label }}
            </a-option>
          </a-select>
        </a-form-item>
        
        <a-form-item field="cloud_account_id" label="云账户" required>
          <a-select v-model="uploadForm.cloud_account_id" placeholder="请选择云账户（COS存储）">
            <a-option v-for="account in cloudAccountOptions" :key="account.account_id" :value="account.account_id">
              {{ account.account_name }} ({{ getProviderName(account.provider) }})
            </a-option>
          </a-select>
          <template #extra>
            <span style="color: #86909c; font-size: 12px;">
              仅支持COS方式上传，请选择云账户。上传由独立 cloudnode 服务处理。
            </span>
          </template>
        </a-form-item>
        
        <a-form-item field="description" label="描述（可选）">
          <a-textarea 
            v-model="uploadForm.description" 
            placeholder="请输入代码包描述" 
            :rows="3"
            :max-length="500"
            show-word-limit
          />
          <template #extra>
            <span style="color: #86909c; font-size: 12px;">
              最多500个字符 ({{ uploadForm.description?.length || 0 }}/500)
            </span>
          </template>
        </a-form-item>
        
        <a-form-item field="file" label="代码包文件">
          <a-upload
            ref="uploadRef"
            :file-list="fileList"
            :auto-upload="false"
            :show-file-list="true"
            :limit="1"
            accept=".zip"
            @change="onFileChange"
          >
            <template #upload-button>
              <div class="upload-area">
                <div>
                  <icon-upload style="font-size: 48px; color: #c9cdd4;" />
                </div>
                <div style="margin-top: 8px;">
                  点击或拖拽上传ZIP文件
                </div>
                <div style="color: #86909c; font-size: 12px; margin-top: 4px;">
                  支持ZIP格式，文件大小不超过100MB
                </div>
              </div>
            </template>
          </a-upload>
        </a-form-item>
      </a-form>
      
      <!-- 自定义footer -->
      <template #footer>
        <a-space>
          <a-button @click="handleUploadCancel">取消</a-button>
          <a-button type="primary" :loading="uploading" @click="handleUploadOk">确定</a-button>
        </a-space>
      </template>
    </a-modal>
    
    <!-- 代码包详情弹窗 -->
    <a-modal
      v-model:visible="detailVisible"
      title="代码包详情"
      :width="800"
      :mask-closable="false"
      :footer="false"
      @cancel="handleDetailCancel"
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
            <a-tag :color="getStatusColor(packageDetail.status)">
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
          <a-descriptions-item label="云账户">
            {{ packageDetail.cloud_account_id || '本地存储' }}
          </a-descriptions-item>
          <a-descriptions-item label="存储类型">
            <a-tag :color="packageDetail.cos_bucket === 'local' ? 'orange' : 'blue'">
              {{ packageDetail.cos_bucket === 'local' ? '本地存储' : 'COS存储' }}
            </a-tag>
          </a-descriptions-item>
          <a-descriptions-item label="COS地区">
            {{ packageDetail.cos_region || '-' }}
          </a-descriptions-item>
          <a-descriptions-item label="COS桶">
            {{ packageDetail.cos_bucket === 'local' ? '-' : (packageDetail.cos_bucket || '-') }}
          </a-descriptions-item>
          <a-descriptions-item label="存储路径" :span="2">
            <a-typography-text copyable v-if="packageDetail.cos_path">
              {{ packageDetail.cos_path }}
            </a-typography-text>
            <span v-else>-</span>
          </a-descriptions-item>
        </a-descriptions>
        
        <!-- 时间信息 -->
        <a-descriptions title="时间信息" :column="2" bordered size="medium">
          <a-descriptions-item label="创建者">{{ packageDetail.created_by }}</a-descriptions-item>
          <a-descriptions-item label="创建时间">{{ formatTime(packageDetail.created_time) }}</a-descriptions-item>
          <a-descriptions-item label="最后部署时间" :span="2">
            {{ packageDetail.last_deploy_time ? formatTime(packageDetail.last_deploy_time) : '-' }}
          </a-descriptions-item>
        </a-descriptions>
        
        <div style="margin-top: 16px; text-align: right;">
          <a-space>
            <a-button @click="handleDetailCancel">关闭</a-button>
            <a-button 
              type="primary"
              status="success"
              @click="onDownload(packageDetail)"
              :disabled="packageDetail.status !== 1"
              :loading="downloadProgress[packageDetail.package_id] !== undefined && downloadProgress[packageDetail.package_id] < 100"
            >
              <template #icon>
                <icon-download />
              </template>
              <span v-if="downloadProgress[packageDetail.package_id] !== undefined && downloadProgress[packageDetail.package_id] < 100">
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
  </component>
</template>

<script setup lang="ts">
import { ref, watch, reactive, computed, onMounted } from 'vue';
import { Message, Modal } from '@arco-design/web-vue';
import { 
  getFunctionPackageList, 
  getFunctionPackageDetail,
  uploadFunctionPackage,
  deleteFunctionPackage,
  downloadPackageByURL,
  RUNTIME_OPTIONS,
  PACKAGE_TYPE_OPTIONS,
  resolvePackageType,
  STATUS_OPTIONS,
  PACKAGE_STATUS_LABEL,
  PACKAGE_TYPE_LABEL,
  type FunctionPackage,
  type PackageListRequest
} from '@/api/function-package';
import { getCloudAccountList, type CloudAccount } from '@/api/cloud-account';

// Props
const props = defineProps<{
  modelValue?: boolean;
  mode?: 'page' | 'modal';
  packageType?: string | number; // 用于过滤代码包类型
  bizType?: string; // 用于过滤业务类型
}>();

// Emits
const emit = defineEmits<{
  'update:modelValue': [value: boolean];
  'refresh': [];
}>();


// 响应式数据
const packageManageMode = computed(() => props.mode || 'page');
const standalone = computed(() => packageManageMode.value === 'page');
const visible = ref(standalone.value ? true : props.modelValue);
const packageManageContainer = computed(() => standalone.value ? 'div' : Modal);
const packageManageContainerProps = computed(() => {
  if (standalone.value) {
    return {
      class: 'function-package-page'
    };
  }

  return {
    visible: visible.value,
    title: '代码包版本管理',
    width: 1200,
    maskClosable: false,
    footer: false
  };
});
const loading = ref(false);
const packageList = ref<FunctionPackage[]>([]);
const cloudAccountOptions = ref<CloudAccount[]>([]);
const uploadVisible = ref(false);
const uploading = ref(false);
const uploadFormRef = ref();
const uploadRef = ref();
const fileList = ref<any[]>([]);

// 详情弹窗
const detailVisible = ref(false);
const packageDetail = ref<FunctionPackage | null>(null);

// 版本验证状态
const versionValidationStatus = ref<'error' | 'warning' | 'success' | undefined>(undefined);
const versionFeedback = ref<string>('');
const isCheckingVersion = ref(false);

// 搜索表单
const searchForm = reactive<PackageListRequest>({
  page: 1,
  page_size: 10,
  biz_type: props.bizType // 根据传入的 bizType 初始化过滤条件
});

// 分页信息
const pagination = computed(() => ({
  current: searchForm.page || 1,
  pageSize: searchForm.page_size || 10,
  total: total.value,
  showSizeChanger: true,
  showJumper: true,
  showTotal: true,
  pageSizeOptions: ['10', '20', '50', '100']
}));

const total = ref(0);

// 上传表单数据
const defaultUploadForm = {
  package_name: 'moox-collector',
  version: 'dev',
  description: '',
  runtime: 'Python3.9',
  package_type: 1,
  biz_type: '',
  cloud_account_id: ''
};

const uploadForm = reactive({ ...defaultUploadForm });

// 监听属性变化
watch(() => props.modelValue, async (newVal) => {
  if (standalone.value) return;
  visible.value = newVal;
  if (newVal) {
    await initializePackageManage();
  }
});

watch(visible, (newVal) => {
  if (!standalone.value) {
    emit('update:modelValue', newVal);
  }
});

const handlePackageManageVisibleChange = (newVal: boolean) => {
  visible.value = newVal;
};

onMounted(async () => {
  if (standalone.value) {
    await initializePackageManage();
  }
});

const initializePackageManage = async () => {
  if (props.bizType) {
    searchForm.biz_type = props.bizType;
  }
  await Promise.all([loadPackageList(), loadCloudAccounts()]);
};

// 加载代码包列表
const loadPackageList = async () => {
  loading.value = true;
  try {
    // 构建查询参数，排除 package_type
    const { package_type, ...queryParams } = searchForm;
    const response = await getFunctionPackageList(queryParams);

    if (response?.items) {
      packageList.value = response.items || [];
      total.value = response.total || 0;
    } else {
      packageList.value = [];
      total.value = 0;
    }
  } catch (error) {
    console.error('加载代码包列表失败:', error);
    Message.error({
      content: '加载代码包列表失败',
      duration: 5000
    });
  } finally {
    loading.value = false;
  }
};

// 加载云账户列表
const loadCloudAccounts = async () => {
  try {
    const accounts = await getCloudAccountList();
    cloudAccountOptions.value = accounts || [];
  } catch (error) {
    console.error('加载云账户列表失败:', error);
    cloudAccountOptions.value = [];
    Message.error(error instanceof Error ? error.message : '加载云账户失败：请确认已登录且 moox-cloudnode 服务已部署');
  }
};

// 重置搜索
const resetSearch = () => {
  Object.assign(searchForm, {
    page: 1,
    page_size: 10,
    package_name: '',
    runtime: '',
    biz_type: props.bizType || '', // 保持传入的 bizType
    status: undefined
  });
  loadPackageList();
};

// 分页处理
const handlePageChange = (page: number) => {
  searchForm.page = page;
  loadPackageList();
};

const handlePageSizeChange = (pageSize: number) => {
  searchForm.page_size = pageSize;
  searchForm.page = 1;
  loadPackageList();
};

// 新增
const onAdd = () => {
  // 根据传入的 packageType 和 bizType 设置默认值
  const defaultPackageType = resolvePackageType(props.packageType);
  const defaultBizType = props.bizType || '';
  Object.assign(uploadForm, {
    ...defaultUploadForm,
    package_type: defaultPackageType,
    biz_type: defaultBizType,
    package_name: packageNameForType(defaultPackageType)
  });
  fileList.value = [];
  // 清除版本验证状态
  versionValidationStatus.value = undefined;
  versionFeedback.value = '';
  uploadVisible.value = true;
};

// 包类型变化处理
const packageNameForType = (packageType: number) => {
  const map: Record<number, string> = {
    1: 'moox-collector',
    2: 'factor_calculator',
    3: 'custom_package'
  };
  return map[packageType] || 'moox-collector';
};

const onPackageTypeChange = (value: number) => {
  uploadForm.package_name = packageNameForType(value);
  
  // 包类型变化时重新检查版本
  if (uploadForm.version) {
    checkVersionExists();
  }
};

// 检查版本是否已存在
const checkVersionExists = async () => {
  if (!uploadForm.version || !uploadForm.package_name || isCheckingVersion.value) {
    return;
  }

  // 去除首尾空格
  uploadForm.version = uploadForm.version.trim();
  
  if (!uploadForm.version) {
    versionValidationStatus.value = undefined;
    versionFeedback.value = '';
    return;
  }

  isCheckingVersion.value = true;
  versionValidationStatus.value = undefined;
  versionFeedback.value = '正在检查版本...';

  try {
    // 使用现有的列表查询API检查版本是否存在
    const response = await getFunctionPackageList({
      package_name: uploadForm.package_name,
      page: 1,
      page_size: 1000 // 获取所有匹配的包
    });

    if (response?.items) {
      const existingPackages = response.items || [];
      
      // 检查是否有相同版本的包（排除已删除的）
      const duplicatePackage = existingPackages.find((pkg: FunctionPackage) => 
        pkg.version === uploadForm.version && 
        pkg.package_name === uploadForm.package_name &&
        pkg.status !== 4 // 排除已删除状态
      );

      if (duplicatePackage) {
        versionValidationStatus.value = 'error';
        versionFeedback.value = `版本 ${uploadForm.version} 已存在，请使用其他版本号`;
      } else {
        versionValidationStatus.value = 'success';
        versionFeedback.value = '版本号可用';
      }
    } else {
      // 查询失败，不显示错误，允许用户继续
      versionValidationStatus.value = undefined;
      versionFeedback.value = '';
    }
  } catch (error) {
    console.error('检查版本失败:', error);
    // 查询失败，不显示错误，允许用户继续
    versionValidationStatus.value = undefined;
    versionFeedback.value = '';
  } finally {
    isCheckingVersion.value = false;
  }
};

// 文件上传处理
const onFileChange = (fileItemList: any[], fileItem: any) => {
  fileList.value = fileItemList;
  if (fileItem && fileItem.file) {
    const fileName = fileItem.file.name;

    // 检查文件类型
    if (!fileName.toLowerCase().endsWith('.zip')) {
      Message.error({
        content: '只支持ZIP格式的文件',
        duration: 5000
      });
      fileList.value = [];
      uploadForm.version = '';
      versionValidationStatus.value = undefined;
      versionFeedback.value = '';
      return;
    }

    // 从文件名解析版本号（格式：xxx-v1.0.45.zip）
    const versionMatch = fileName.match(/-v(\d+\.\d+\.\d+)\.zip$/i);
    if (!versionMatch) {
      Message.error({
        content: '文件名格式不正确，应为：xxx-v1.0.45.zip（例如：collector-scf-v1.0.45.zip）',
        duration: 5000
      });
      fileList.value = [];
      uploadForm.version = '';
      versionValidationStatus.value = 'error';
      versionFeedback.value = '文件名格式不符合要求';
      return;
    }

    // 提取版本号（包含 v 前缀）
    const version = 'v' + versionMatch[1];
    uploadForm.version = version;

    // 解析成功后自动检查版本是否存在
    checkVersionExists();

    // 检查文件大小（100MB限制）
    const maxSize = 100 * 1024 * 1024;
    if (fileItem.file.size > maxSize) {
      Message.error({
        content: '文件大小不能超过100MB',
        duration: 5000
      });
      fileList.value = [];
      uploadForm.version = '';
      versionValidationStatus.value = undefined;
      versionFeedback.value = '';
      return;
    }
  } else {
    uploadForm.version = '';
    versionValidationStatus.value = undefined;
    versionFeedback.value = '';
  }
};

// 上传取消
const handleUploadCancel = () => {
  // 清除版本验证状态
  versionValidationStatus.value = undefined;
  versionFeedback.value = '';
  uploadVisible.value = false;
};

// 输入验证函数
const validateUploadForm = () => {
  const errors: string[] = [];

  // 验证函数包类型
  if (!uploadForm.package_type) {
    errors.push('请选择函数包类型');
  }

  // 验证版本号
  if (!uploadForm.version) {
    errors.push('请输入版本号');
  } else {
    // 去除首尾空格
    uploadForm.version = uploadForm.version.trim();

    // 版本号格式验证 (支持 v1.0.0, 1.0.0, v1.0, 1.0 等格式)
    const versionRegex = /^v?\d+(\.\d+){0,2}(-[a-zA-Z0-9]+)?$/;
    if (!versionRegex.test(uploadForm.version)) {
      errors.push('版本号格式不正确，请使用如 v1.0.0 或 1.0.0 的格式');
    }

    // 检查版本号长度
    if (uploadForm.version.length > 20) {
      errors.push('版本号长度不能超过20个字符');
    }
  }

  // 验证运行时环境
  if (!uploadForm.runtime) {
    errors.push('请选择运行时环境');
  }

  // 验证云账户（必填）
  if (!uploadForm.cloud_account_id) {
    errors.push('请选择云账户，仅支持COS方式上传');
  }

  if (fileList.value.length === 0) {
    errors.push('请选择要上传的ZIP文件');
  } else {
    const file = fileList.value[0];
    if (!file.file) {
      errors.push('文件读取失败，请重新选择文件');
    } else {
      if (!file.file.name.toLowerCase().endsWith('.zip')) {
        errors.push('只支持ZIP格式的文件');
      }
      if (file.file.size > 100 * 1024 * 1024) {
        errors.push('文件大小不能超过100MB');
      }
      if (file.file.size === 0) {
        errors.push('文件不能为空');
      }
      if (file.file.name.length > 255) {
        errors.push('文件名长度不能超过255个字符');
      }
    }
  }
  
  // 如果有描述，验证长度和内容
  if (uploadForm.description) {
    uploadForm.description = uploadForm.description.trim();
    if (uploadForm.description.length > 500) {
      errors.push('描述长度不能超过500个字符');
    }
  }
  
  return errors;
};

// 上传确认
const handleUploadOk = async () => {
  // 检查版本是否重复
  if (versionValidationStatus.value === 'error') {
    Message.error({
      content: versionFeedback.value || '版本已存在，请修改版本号',
      duration: 5000
    });
    return;
  }

  // 如果还没有检查过版本，先检查一下
  if (uploadForm.version && !versionValidationStatus.value) {
    await checkVersionExists();
    // 检查后如果发现重复，阻止提交
    if (versionValidationStatus.value === 'error') {
      Message.error({
        content: versionFeedback.value || '版本已存在，请修改版本号',
        duration: 5000
      });
      return;
    }
  }

  // 表单验证
  const errors = await uploadFormRef.value?.validate();
  if (errors) {
    return; // 有验证错误，不继续执行
  }

  // 自定义验证
  const validationErrors = validateUploadForm();
  if (validationErrors.length > 0) {
    Message.error({
      content: validationErrors[0],
      duration: 5000 // 显示第一个错误，停留5秒
    });
    return; // 有验证错误，不继续执行
  }

  uploading.value = true;
  
  try {
    await handleUpload();
  } catch (error: any) {
    console.error('上传代码包失败:', error);
    Message.error({
      content: error?.message || '上传代码包失败',
      duration: 5000 // 错误消息显示5秒
    });
    // 出错时不关闭弹窗，让用户可以修改后重新提交
  } finally {
    uploading.value = false;
  }
};

// 统一上传处理（独立 cloudnode 服务）
const handleUpload = async () => {
  try {
    const originalFilename = fileList.value[0]?.file?.name || '';
    await uploadFunctionPackage({
      ...uploadForm,
      original_filename: originalFilename
    }, fileList.value[0].file);

    Message.success('上传成功！');
    uploadVisible.value = false;
    await loadPackageList();
    emit('refresh');
  } catch (error: any) {
    console.error('上传代码包失败:', error);
    
    const errorMessage = error?.message || '上传代码包失败';
    
    Message.error(errorMessage);
    uploading.value = false;
  }
};

// 下载进度状态（简化版）
const downloadProgress = ref<{[key: string]: number}>({});


// 下载（新的URL下载方式）
const onDownload = async (record: FunctionPackage) => {
  try {
    // 如果已经在下载中，则忽略
    if (downloadProgress.value[record.package_id] !== undefined) {
      return;
    }

    // 显示下载状态
    downloadProgress.value[record.package_id] = 0;

    // 使用新的URL下载方式
    await downloadPackageByURL(record.package_id);

    // 清理进度状态
    delete downloadProgress.value[record.package_id];

  } catch (error) {
    console.error('下载失败:', error);
    // 清除状态
    delete downloadProgress.value[record.package_id];

    const errorMessage = error instanceof Error ? error.message : '未知错误';
    Message.error({
      content: `下载失败: ${errorMessage}`,
      duration: 5000
    });
  }
};

// 删除
const onDelete = async (record: FunctionPackage) => {
  try {
    await deleteFunctionPackage(record.package_id);
    Message.success('删除成功');
    // 刷新代码包列表
    await loadPackageList();
    emit('refresh');
  } catch (error) {
    console.error('删除代码包失败:', error);
    Message.error({
      content: '删除代码包失败',
      duration: 5000
    });
  }
};

// 关闭弹窗
const handleCancel = () => {
  visible.value = false;
};

// 显示代码包详情
const onShowDetail = async (record: FunctionPackage) => {
  packageDetail.value = null;
  detailVisible.value = true;

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
    detailVisible.value = false;
  }
};

// 关闭详情弹窗
const handleDetailCancel = () => {
  detailVisible.value = false;
  packageDetail.value = null;
};

// 工具函数
const getProviderName = (provider: string) => {
  const providerMap: Record<string, string> = {
    'tencent': '腾讯云',
    'aliyun': '阿里云',
    'aws': 'AWS'
  };
  return providerMap[provider] || provider;
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

const getStatusColor = (status: number | string) => {
  const colorMap: Record<number, string> = {
    1: 'blue',
    2: 'green',
    3: 'red',
    4: 'gray'
  };
  const numeric = typeof status === 'number' ? status : Number(status);
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

</script>

<style scoped>
.function-package-page {
  padding: 16px;
  min-height: 100%;
  background: #fff;
}

.function-package-manage {
  min-height: 600px;
}

.package-toolbar {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 12px;
}

.package-filters {
  flex: 1 1 auto;
}

.package-upload-button {
  flex: 0 0 auto;
}

.upload-area {
  border: 2px dashed #d0d0d0;
  border-radius: 6px;
  padding: 40px;
  text-align: center;
  background: #fafafa;
  cursor: pointer;
  transition: border-color 0.3s;
}

.upload-area:hover {
  border-color: #165dff;
}

/* 版本验证样式 */
:deep(.arco-form-item-status-error .arco-input-wrapper) {
  border-color: #f53f3f;
  box-shadow: 0 0 0 2px rgba(245, 63, 63, 0.1);
}

:deep(.arco-form-item-status-success .arco-input-wrapper) {
  border-color: #00b42a;
  box-shadow: 0 0 0 2px rgba(0, 180, 42, 0.1);
}

:deep(.arco-form-item-feedback) {
  font-size: 12px;
  margin-top: 4px;
}

:deep(.arco-form-item-status-error .arco-form-item-feedback) {
  color: #f53f3f;
}

:deep(.arco-form-item-status-success .arco-form-item-feedback) {
  color: #00b42a;
}

/* 加载动画 */
@keyframes spin {
  0% { transform: rotate(0deg); }
  100% { transform: rotate(360deg); }
}

.spinning-icon {
  animation: spin 1s linear infinite;
  display: inline-block;
}
</style>
