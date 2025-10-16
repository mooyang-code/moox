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
          <a-table-column title="代码包名称" data-index="package_name" :width="180"></a-table-column>
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
          <a-table-column title="创建者" data-index="created_by" :width="120"></a-table-column>
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
              选择云账户将使用COS存储，不选择将存储到本地/tmp目录
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
  </a-modal>
</template>

<script setup lang="ts">
import { ref, watch, reactive, computed } from 'vue';
import { Message } from '@arco-design/web-vue';
import { 
  getFunctionPackageList, 
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
    if (response?.code === 200 && response?.data) {
      packageList.value = response.data.items || [];
      total.value = response.data.total || 0;
    } else {
      packageList.value = [];
      total.value = 0;
    }
  } catch (error) {
    console.error('加载代码包列表失败:', error);
    Message.error('加载代码包列表失败');
  } finally {
    loading.value = false;
  }
};

// 加载云账户列表
const loadCloudAccounts = async () => {
  try {
    const response = await getCloudAccountList();
    if (response?.code === 200 && response?.data) {
      let data = response.data;
      if (Array.isArray(data)) {
        cloudAccountOptions.value = data;
      } else {
        cloudAccountOptions.value = [data].filter(Boolean);
      }
    } else if (response?.ret_info?.code === 0) {
      let data = response.ret_info.data;
      if (Array.isArray(data)) {
        cloudAccountOptions.value = data;
      } else {
        cloudAccountOptions.value = [data].filter(Boolean);
      }
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
      Message.error('只支持ZIP格式的文件');
      fileList.value = [];
      uploadForm.file_content = '';
      return;
    }
    
    // 检查文件大小（100MB限制）
    const maxSize = 100 * 1024 * 1024;
    if (fileItem.file.size > maxSize) {
      Message.error('文件大小不能超过100MB');
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
      Message.error('文件读取失败');
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
    Message.error(validationErrors[0]); // 显示第一个错误
    return; // 有验证错误，不继续执行
  }

  uploading.value = true;
  try {
    const response = await uploadFunctionPackage(uploadForm);
    if (response?.data?.code === 200) {
      Message.success('上传成功');
      uploadVisible.value = false; // 只有成功时才关闭弹窗
      await loadPackageList();
      emit('refresh');
    } else {
      throw new Error(response?.data?.message || '上传失败');
    }
  } catch (error: any) {
    console.error('上传代码包失败:', error);
    Message.error(error?.message || '上传代码包失败');
    // 出错时不关闭弹窗，让用户可以修改后重新提交
  } finally {
    uploading.value = false;
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
    Message.error('下载失败');
  }
};

// 删除
const onDelete = async (record: FunctionPackage) => {
  try {
    const response = await deleteFunctionPackage(record.id);
    if (response?.data?.code === 200) {
      Message.success('删除成功');
      await loadPackageList();
      emit('refresh');
    } else {
      throw new Error('删除失败');
    }
  } catch (error) {
    console.error('删除代码包失败:', error);
    Message.error('删除代码包失败');
  }
};

// 关闭弹窗
const handleCancel = () => {
  visible.value = false;
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
    0: 'processing', // 上传中
    1: 'success',    // 可用
    2: 'default',    // 已删除
    3: 'danger'      // 上传失败
  };
  return colorMap[status] || 'default';
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
</style>