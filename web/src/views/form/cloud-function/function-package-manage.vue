<template>
  <a-modal
    v-model:visible="visible"
    title="云函数版本管理"
    :width="1200"
    :mask-closable="false"
    :footer="false"
    @cancel="handleCancel"
  >
    <div class="function-package-manage">
      <!-- 搜索区域 -->
      <a-row style="margin-bottom: 16px;">
        <a-space wrap>
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
            v-model="searchForm.package_type"
            placeholder="函数包类型"
            style="width: 150px"
            allow-clear
          >
            <a-option v-for="type in PACKAGE_TYPE_OPTIONS" :key="type.value" :value="type.value">
              {{ type.label }}
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
      </a-row>

      <a-row style="margin-bottom: 16px;">
        <a-button type="primary" @click="onAdd">
          <template #icon><icon-plus /></template>
          上传代码包
        </a-button>
      </a-row>

      <!-- 上传状态提示 -->
      <a-row v-if="isPolling" style="margin-bottom: 16px;">
        <a-alert type="info" :closable="false" style="width: 100%;">
          <template #icon><icon-loading class="spinning-icon" /></template>
          <div style="display: flex; align-items: center; justify-content: space-between;">
            <div>
              <div style="font-weight: 500; margin-bottom: 4px;">{{ uploadMessage }}</div>
              <div style="color: #86909c; font-size: 12px;">
                任务ID: {{ currentTaskId }}
              </div>
            </div>
            <div style="margin-left: 16px;">
              <a-spin :size="20" :loading="true" />
            </div>
          </div>
        </a-alert>
      </a-row>
      
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
                {{ record.package_type_label }}
              </a-tag>
            </template>
          </a-table-column>
          <a-table-column title="运行时" data-index="runtime" :width="120"></a-table-column>
          <a-table-column title="文件大小" data-index="file_size" :width="120">
            <template #cell="{ record }">
              {{ formatFileSize(record.file_size) }}
            </template>
          </a-table-column>
          <a-table-column title="状态" data-index="status_label" :width="100">
            <template #cell="{ record }">
              <a-tag :color="getStatusColor(record.status)">
                {{ record.status_label }}
              </a-tag>
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
          <a-table-column title="创建时间" data-index="created_at" :width="180">
            <template #cell="{ record }">
              {{ formatTime(record.created_at) }}
            </template>
          </a-table-column>
          <a-table-column title="操作" :width="180" align="center" fixed="right">
            <template #cell="{ record }">
              <a-space>
                <a-button type="primary" size="mini" status="success" @click="onDownload(record)" :disabled="record.status !== 1">
                  <template #icon><icon-download /></template>
                  下载
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
          <a-select v-model="uploadForm.package_type" placeholder="请选择函数包类型" @change="onPackageTypeChange">
            <a-option v-for="type in PACKAGE_TYPE_OPTIONS" :key="type.value" :value="type.value">
              {{ type.label }}
            </a-option>
          </a-select>
        </a-form-item>
        
        
        <a-form-item field="version" label="版本号" required>
          <a-input v-model="uploadForm.version" placeholder="请输入版本号，如：v1.0.0" />
        </a-form-item>
        
        <a-form-item field="runtime" label="运行时环境" required>
          <a-select v-model="uploadForm.runtime" placeholder="请选择运行时环境">
            <a-option v-for="runtime in RUNTIME_OPTIONS" :key="runtime.value" :value="runtime.value">
              {{ runtime.label }}
            </a-option>
          </a-select>
        </a-form-item>
        
        <a-form-item field="cloud_account_id" label="云账户（可选）">
          <a-select v-model="uploadForm.cloud_account_id" placeholder="选择云账户（用于COS存储）" allow-clear>
            <a-option v-for="account in cloudAccountOptions" :key="account.account_id" :value="account.account_id">
              {{ account.account_name }} ({{ getProviderName(account.provider) }})
            </a-option>
          </a-select>
          <template #extra>
            <span style="color: #86909c; font-size: 12px;">
              选择云账户将使用COS存储，不选择将存储到本地/tmp目录。所有上传都为异步处理。
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
              {{ packageDetail.package_type_label }}
            </a-tag>
          </a-descriptions-item>
          <a-descriptions-item label="运行时环境">{{ packageDetail.runtime }}</a-descriptions-item>
          <a-descriptions-item label="文件大小">{{ formatFileSize(packageDetail.file_size) }}</a-descriptions-item>
          <a-descriptions-item label="状态">
            <a-tag :color="getStatusColor(packageDetail.status)">
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
          <a-descriptions-item label="创建时间">{{ formatTime(packageDetail.created_at) }}</a-descriptions-item>
          <a-descriptions-item label="最后部署时间" :span="2">
            {{ packageDetail.last_deploy_time ? formatTime(packageDetail.last_deploy_time) : '-' }}
          </a-descriptions-item>
        </a-descriptions>
        
        <div style="margin-top: 16px; text-align: right;">
          <a-space>
            <a-button @click="handleDetailCancel">关闭</a-button>
            <a-button type="primary" @click="onDownload(packageDetail)" :disabled="packageDetail.status !== 1">
              <template #icon><icon-download /></template>
              下载
            </a-button>
          </a-space>
        </div>
      </div>
      <div v-else style="text-align: center; padding: 40px;">
        <a-spin :loading="true" />
        <div style="margin-top: 16px;">加载中...</div>
      </div>
    </a-modal>
  </a-modal>
</template>

<script setup lang="ts">
import { ref, watch, reactive, computed, onUnmounted } from 'vue';
import { Message } from '@arco-design/web-vue';
import { 
  getFunctionPackageList, 
  getFunctionPackageDetail,
  uploadFunctionPackage,
  deleteFunctionPackage,
  getFunctionPackageDownloadURL,
  downloadLocalPackage,
  RUNTIME_OPTIONS,
  PACKAGE_TYPE_OPTIONS,
  STATUS_OPTIONS,
  type FunctionPackage,
  type PackageListRequest
} from '@/api/function-package';
import { getCloudAccountList, type CloudAccount } from '@/api/cloud-account';
import { asyncTaskManager } from '@/utils/async-task';

// Props
const props = defineProps<{
  modelValue: boolean;
}>();

// Emits
const emit = defineEmits<{
  'update:modelValue': [value: boolean];
  'refresh': [];
}>();


// 响应式数据
const visible = ref(props.modelValue);
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

// 异步任务状态
const currentTaskId = ref<string>('');
const isPolling = ref(false);
const uploadMessage = ref('');
const pollingTimer = ref<number | null>(null);

// 搜索表单
const searchForm = reactive<PackageListRequest>({
  page: 1,
  page_size: 20
});

// 分页信息
const pagination = computed(() => ({
  current: searchForm.page || 1,
  pageSize: searchForm.page_size || 20,
  total: total.value,
  showSizeChanger: true,
  showJumper: true,
  showTotal: true,
  pageSizeOptions: ['10', '20', '50', '100']
}));

const total = ref(0);

// 上传表单数据
const defaultUploadForm = {
  package_name: 'data_collector', // 根据默认包类型设置
  version: '',
  description: '',
  runtime: 'Go1',
  package_type: 'data_collector',
  cloud_account_id: '',
  file_content: ''
};

const uploadForm = reactive({ ...defaultUploadForm });

// 监听属性变化
watch(() => props.modelValue, (newVal) => {
  visible.value = newVal;
  if (newVal) {
    loadPackageList();
    loadCloudAccounts();
  }
});

watch(visible, (newVal) => {
  emit('update:modelValue', newVal);
});

// 加载代码包列表
const loadPackageList = async () => {
  loading.value = true;
  try {
    const response = await getFunctionPackageList(searchForm);
    console.log('代码包列表API响应:', response);
    
    if (response?.code === 200 && response?.data) {
      console.log('解析后的列表数据:', response);
      packageList.value = response.data || [];
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
    const response = await getCloudAccountList();
    console.log('云账户选项API响应:', response);
    
    if (response?.code === 200 && response?.data) {
      cloudAccountOptions.value = response.data || [];
    } else {
      cloudAccountOptions.value = [];
    }
  } catch (error) {
    console.error('加载云账户列表失败:', error);
    cloudAccountOptions.value = [];
  }
};

// 重置搜索
const resetSearch = () => {
  Object.assign(searchForm, {
    page: 1,
    page_size: 20,
    package_name: '',
    runtime: '',
    package_type: '',
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
  Object.assign(uploadForm, { ...defaultUploadForm });
  fileList.value = [];
  uploadVisible.value = true;
};

// 包类型变化处理
const onPackageTypeChange = (value: string) => {
  // 根据包类型自动生成包名
  const packageTypeNameMap: Record<string, string> = {
    'data_collector': 'data_collector',
    'factor_calculator': 'factor_calculator'
  };
  uploadForm.package_name = packageTypeNameMap[value] || value;
};

// 文件上传处理
const onFileChange = (fileItemList: any[], fileItem: any) => {
  fileList.value = fileItemList;
  if (fileItem && fileItem.file) {
    // 检查文件类型
    if (!fileItem.file.name.toLowerCase().endsWith('.zip')) {
      Message.error({
        content: '只支持ZIP格式的文件',
        duration: 5000
      });
      fileList.value = [];
      uploadForm.file_content = '';
      return;
    }
    
    // 检查文件大小（100MB限制）
    const maxSize = 100 * 1024 * 1024;
    if (fileItem.file.size > maxSize) {
      Message.error({
        content: '文件大小不能超过100MB',
        duration: 5000
      });
      fileList.value = [];
      uploadForm.file_content = '';
      return;
    }
    
    // 将文件转换为base64
    const reader = new FileReader();
    reader.onload = (e) => {
      if (e.target?.result) {
        const base64 = (e.target.result as string).split(',')[1]; // 移除data:xxx;base64,前缀
        uploadForm.file_content = base64;
      }
    };
    reader.onerror = () => {
      Message.error({
        content: '文件读取失败',
        duration: 5000
      });
      fileList.value = [];
      uploadForm.file_content = '';
    };
    reader.readAsDataURL(fileItem.file);
  } else {
    // 文件被移除
    uploadForm.file_content = '';
  }
};

// 上传取消
const handleUploadCancel = () => {
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
  
  // 验证文件
  if (!uploadForm.file_content) {
    errors.push('请选择要上传的ZIP文件');
  }
  
  // 验证文件列表
  if (fileList.value.length === 0) {
    errors.push('请选择要上传的文件');
  } else {
    const file = fileList.value[0];
    // 验证文件存在
    if (!file.file) {
      errors.push('文件读取失败，请重新选择文件');
    } else {
      // 验证文件类型
      if (!file.file.name.toLowerCase().endsWith('.zip')) {
        errors.push('只支持ZIP格式的文件');
      }
      
      // 验证文件大小（100MB限制）
      if (file.file.size > 100 * 1024 * 1024) {
        errors.push('文件大小不能超过100MB');
      }
      
      // 验证文件大小（不能为空）
      if (file.file.size === 0) {
        errors.push('文件不能为空');
      }
      
      // 验证文件名
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

// 统一上传处理（异步）
const handleUpload = async () => {
  try {
    const response = await uploadFunctionPackage(uploadForm);
    console.log('上传API响应:', response);
    
    if (response?.data?.code === 200) {
      // 后端返回的data是数组格式，取第一个元素
      const responseData = response.data.data;
      const uploadResult = Array.isArray(responseData) ? responseData[0] : responseData;
      console.log('解析后的上传结果:', uploadResult);
      console.log('is_async:', uploadResult.is_async, 'task_id:', uploadResult.task_id);
      
      if (uploadResult.is_async && uploadResult.task_id) {
        // 异步上传，开始轮询
        console.log('收到异步上传响应:', uploadResult);
        currentTaskId.value = uploadResult.task_id;
        uploadMessage.value = '上传任务已创建，正在后台处理...';
        
        
        // 不要立即显示成功消息，等待轮询结果
        uploadVisible.value = false; // 关闭上传弹窗
        
        // 开始轮询任务状态
        startTaskPolling(uploadResult.task_id);
      } else {
        // 本地存储直接完成
        Message.success('上传成功');
        uploadVisible.value = false;
        // 刷新代码包列表
        await loadPackageList();
        emit('refresh');
      }
    } else {
      // 处理业务错误
      const errorMessage = response?.data?.message || '上传失败';
      throw new Error(errorMessage);
    }
  } catch (error: any) {
    console.error('上传请求失败:', error);
    console.error('错误对象详情:', {
      message: error?.message,
      response: error?.response,
      responseData: error?.response?.data
    });
    
    // 从不同的错误结构中提取错误消息
    let errorMessage = '上传失败';
    
    // 1. 先检查axios错误消息中是否包含JSON响应
    if (error?.message && typeof error.message === 'string' && error.message.includes('{"code":')) {
      try {
        // 从axios错误消息中提取JSON部分
        const jsonMatch = error.message.match(/\{[^}]+\}/);
        if (jsonMatch) {
          const errorData = JSON.parse(jsonMatch[0]);
          if (errorData.message) {
            errorMessage = errorData.message;
          }
        }
      } catch (parseError) {
        console.warn('解析错误消息JSON失败:', parseError);
      }
    }
    
    // 2. 检查response.data中的错误信息
    if (error?.response?.data) {
      const errorData = error.response.data;
      if (errorData.message) {
        errorMessage = errorData.message;
      } else if (errorData.ret_info?.msg) {
        errorMessage = errorData.ret_info.msg;
      } else if (errorData.error) {
        errorMessage = errorData.error;
      }
    }
    
    // 3. 如果还是默认消息，尝试从其他地方提取
    if (errorMessage === '上传失败' && error?.message) {
      // 检查错误消息是否包含有用信息
      if (error.message.includes('版本冲突') || 
          error.message.includes('conflict') ||
          error.message.includes('exists')) {
        errorMessage = error.message;
      }
    }
    
    console.log('提取的错误消息:', errorMessage);
    throw new Error(errorMessage);
  }
};

// 开始任务轮询 - 使用统一的AsyncTaskManager
const startTaskPolling = (taskId: string) => {
  isPolling.value = true;
  currentTaskId.value = taskId;
  
  console.log('开始轮询上传任务:', taskId);
  console.log('AsyncTaskManager实例:', asyncTaskManager);
  
  try {
    // 使用 AsyncTaskManager 进行轮询，与云函数创建保持一致
    asyncTaskManager.startPolling(taskId, {
      onProgress: (data: any) => {
        console.log('Package upload progress:', data);
        uploadMessage.value = data.message || '正在处理上传任务，请勿关闭本页面...';
      },
      onSuccess: (data: any) => {
        console.log('Upload task success:', data);
        handleTaskComplete(data, 'success');
      },
      onFailed: (data: any) => {
        console.log('Upload task failed:', data);
        handleTaskComplete(data, 'failed');
      },
      onPartialSuccess: (data: any) => {
        console.log('Upload task partial success:', data);
        handleTaskComplete(data, 'partial_success');
      },
      showLoading: false,
      interval: 2000
    });
    console.log('AsyncTaskManager.startPolling 调用成功');
  } catch (error) {
    console.error('AsyncTaskManager.startPolling 调用失败:', error);
  }
};

// 任务完成处理
const handleTaskComplete = async (data: any, result: string) => {
  console.log('任务完成:', { data, result });
  
  stopTaskPolling();
  
  if (result === 'success') {
    Message.success('上传成功！');
    // 刷新代码包列表
    await loadPackageList();
    emit('refresh');
  } else if (result === 'failed') {
    // 提取错误消息
    let errorMessage = '上传失败';
    if (data?.message) {
      errorMessage = data.message;
    } else if (data?.error_message) {
      errorMessage = data.error_message;
    }
    Message.error({
      content: errorMessage,
      duration: 5000 // 错误消息显示5秒
    });
    // 失败后也刷新列表，显示失败状态
    await loadPackageList();
  } else if (result === 'partial_success') {
    Message.warning('部分上传成功，请查看详情');
    // 刷新代码包列表
    await loadPackageList();
    emit('refresh');
  }
};

// 停止任务轮询
const stopTaskPolling = () => {
  isPolling.value = false;
  
  // 停止AsyncTaskManager轮询
  asyncTaskManager.stopPolling();
  
  // 清除任务ID
  currentTaskId.value = '';
  
  
  // 清除备用定时器（如果有的话）
  if (pollingTimer.value) {
    clearInterval(pollingTimer.value);
    pollingTimer.value = null;
  }
};

// 下载
const onDownload = async (record: FunctionPackage) => {
  try {
    const response = await getFunctionPackageDownloadURL(record.id);
    if (response?.code === 200 && response?.data?.download_url) {
      const url = response.data.download_url;
      
      // 检查是否为本地存储
      if (url.includes('/download-local')) {
        downloadLocalPackage(record.id);
      } else {
        // COS存储，直接打开链接
        window.open(url, '_blank');
      }
    } else {
      throw new Error('获取下载链接失败');
    }
  } catch (error) {
    console.error('下载失败:', error);
    Message.error({
      content: '下载失败',
      duration: 5000
    });
  }
};

// 删除
const onDelete = async (record: FunctionPackage) => {
  try {
    const response = await deleteFunctionPackage(record.id);
    if (response?.data?.code === 200) {
      Message.success('删除成功');
      // 刷新代码包列表
      await loadPackageList();
      emit('refresh');
    } else {
      throw new Error('删除失败');
    }
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
    const response = await getFunctionPackageDetail(record.id);
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

const getPackageTypeColor = (packageType: string) => {
  const colorMap: Record<string, string> = {
    'data_collector': 'blue',
    'factor_calculator': 'green'
  };
  return colorMap[packageType] || 'gray';
};

const getStatusColor = (status: number) => {
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


// 组件卸载时清理轮询
onUnmounted(() => {
  stopTaskPolling();
});
</script>

<style scoped>
.function-package-manage {
  min-height: 600px;
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
