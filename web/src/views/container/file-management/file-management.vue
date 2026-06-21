<template>
  <div class="file-management-page">
    <SpaceContextBar />
    <div class="page-header">
      <h2>文件管理</h2>
      <p>查看和管理容器中的文件</p>
    </div>
    
    <div class="page-content">
      <a-row :gutter="20">
        <!-- 容器选择和路径导航 -->
        <a-col :span="24">
          <a-card :bordered="false" class="navigation-card">
            <a-row :gutter="16" align="middle">
              <a-col :span="6">
                <a-form-item label="选择容器">
                  <a-select 
                    v-model="selectedContainer" 
                    placeholder="请选择容器"
                    @change="onContainerChange"
                  >
                    <a-option 
                      v-for="container in containers" 
                      :key="container.id" 
                      :value="container.id"
                    >
                      {{ container.name }}
                    </a-option>
                  </a-select>
                </a-form-item>
              </a-col>
              <a-col :span="12">
                <a-form-item label="当前路径">
                  <a-breadcrumb>
                    <a-breadcrumb-item 
                      v-for="(path, index) in pathBreadcrumb" 
                      :key="index"
                      @click="navigateToPath(index)"
                      style="cursor: pointer;"
                    >
                      {{ path }}
                    </a-breadcrumb-item>
                  </a-breadcrumb>
                </a-form-item>
              </a-col>
              <a-col :span="6">
                <a-space>
                  <a-button @click="refreshFiles" :loading="loading">
                    <template #icon>
                      <icon-refresh />
                    </template>
                    刷新
                  </a-button>
                  <a-upload
                    action="/api/upload"
                    :show-file-list="false"
                    @before-upload="beforeUpload"
                  >
                    <a-button type="primary">
                      <template #icon>
                        <icon-upload />
                      </template>
                      上传文件
                    </a-button>
                  </a-upload>
                </a-space>
              </a-col>
            </a-row>
          </a-card>
        </a-col>
        
        <!-- 文件列表 -->
        <a-col :span="24">
          <a-card title="文件列表" :bordered="false">
            <template #extra>
              <a-space>
                <a-input-search
                  v-model="searchKeyword"
                  placeholder="搜索文件..."
                  style="width: 200px;"
                  @search="searchFiles"
                />
                <a-button @click="createFolder">
                  <template #icon>
                    <icon-folder-add />
                  </template>
                  新建文件夹
                </a-button>
              </a-space>
            </template>
            
            <a-table 
              :columns="columns" 
              :data="filteredFiles" 
              :loading="loading"
              :pagination="false"
              @row-click="onRowClick"
            >
              <template #icon="{ record }">
                <icon-folder v-if="record.type === 'directory'" style="color: #ffb84d;" />
                <icon-file v-else style="color: #4080ff;" />
              </template>
              
              <template #size="{ record }">
                {{ record.type === 'directory' ? '-' : formatFileSize(record.size) }}
              </template>
              
              <template #permissions="{ record }">
                <a-tag size="small">{{ record.permissions }}</a-tag>
              </template>
              
              <template #actions="{ record }">
                <a-space>
                  <a-button 
                    type="text" 
                    size="small"
                    @click.stop="downloadFile(record)"
                    v-if="record.type === 'file'"
                  >
                    下载
                  </a-button>
                  <a-button 
                    type="text" 
                    size="small"
                    @click.stop="editFile(record)"
                    v-if="record.type === 'file' && isTextFile(record.name)"
                  >
                    编辑
                  </a-button>
                  <a-button 
                    type="text" 
                    size="small"
                    status="danger"
                    @click.stop="deleteFile(record)"
                  >
                    删除
                  </a-button>
                </a-space>
              </template>
            </a-table>
          </a-card>
        </a-col>
      </a-row>
    </div>
    
    <!-- 文件编辑弹窗 -->
    <a-modal
      v-model:visible="editModalVisible"
      title="编辑文件"
      width="80%"
      @ok="saveFile"
      @cancel="editModalVisible = false"
    >
      <div class="file-editor">
        <div class="editor-header">
          <span>{{ editingFile?.name }}</span>
        </div>
        <a-textarea
          v-model="fileContent"
          :rows="20"
          placeholder="文件内容..."
          class="editor-textarea"
        />
      </div>
    </a-modal>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, onMounted } from 'vue';
import { useRoute } from 'vue-router';
import { Message, Modal } from '@arco-design/web-vue';
import SpaceContextBar from '@/components/SpaceContextBar/index.vue';

const route = useRoute();

// 状态管理
const selectedContainer = ref('');
const currentPath = ref('/');
const loading = ref(false);
const searchKeyword = ref('');
const editModalVisible = ref(false);
const editingFile = ref<any>(null);
const fileContent = ref('');

// 容器列表
const containers = ref([
  { id: 'container-001', name: 'moox-backend-1' },
  { id: 'container-002', name: 'moox-database-1' },
  { id: 'container-003', name: 'moox-redis-1' }
]);

// 文件列表
const files = ref([
  {
    name: 'app',
    type: 'directory',
    size: 0,
    permissions: 'drwxr-xr-x',
    owner: 'root',
    modified: '2024-01-15 10:30:00'
  },
  {
    name: 'config.json',
    type: 'file',
    size: 1024,
    permissions: '-rw-r--r--',
    owner: 'root',
    modified: '2024-01-15 09:15:00'
  },
  {
    name: 'logs',
    type: 'directory',
    size: 0,
    permissions: 'drwxr-xr-x',
    owner: 'root',
    modified: '2024-01-15 08:45:00'
  },
  {
    name: 'startup.sh',
    type: 'file',
    size: 512,
    permissions: '-rwxr-xr-x',
    owner: 'root',
    modified: '2024-01-15 08:30:00'
  }
]);

// 路径面包屑
const pathBreadcrumb = computed(() => {
  return currentPath.value.split('/').filter(p => p);
});

// 过滤后的文件列表
const filteredFiles = computed(() => {
  if (!searchKeyword.value) return files.value;
  return files.value.filter(file => 
    file.name.toLowerCase().includes(searchKeyword.value.toLowerCase())
  );
});

// 表格列配置
const columns = [
  {
    title: '',
    dataIndex: 'icon',
    key: 'icon',
    width: 40,
    slotName: 'icon'
  },
  {
    title: '名称',
    dataIndex: 'name',
    key: 'name'
  },
  {
    title: '大小',
    dataIndex: 'size',
    key: 'size',
    slotName: 'size'
  },
  {
    title: '权限',
    dataIndex: 'permissions',
    key: 'permissions',
    slotName: 'permissions'
  },
  {
    title: '所有者',
    dataIndex: 'owner',
    key: 'owner'
  },
  {
    title: '修改时间',
    dataIndex: 'modified',
    key: 'modified'
  },
  {
    title: '操作',
    key: 'actions',
    slotName: 'actions'
  }
];

// 容器变更
const onContainerChange = () => {
  currentPath.value = '/';
  refreshFiles();
};

// 刷新文件列表
const refreshFiles = async () => {
  if (!selectedContainer.value) {
    Message.warning('请先选择容器');
    return;
  }
  
  loading.value = true;
  try {
    // 模拟API调用
    await new Promise(resolve => setTimeout(resolve, 1000));
    Message.success('文件列表已刷新');
  } catch (error) {
    Message.error('刷新失败');
  } finally {
    loading.value = false;
  }
};

// 导航到指定路径
const navigateToPath = (index: number) => {
  const pathParts = pathBreadcrumb.value.slice(0, index + 1);
  currentPath.value = '/' + pathParts.join('/');
  refreshFiles();
};

// 行点击事件
const onRowClick = (record: any) => {
  if (record.type === 'directory') {
    currentPath.value = currentPath.value === '/' 
      ? `/${record.name}` 
      : `${currentPath.value}/${record.name}`;
    refreshFiles();
  }
};

// 格式化文件大小
const formatFileSize = (bytes: number) => {
  if (bytes === 0) return '0 B';
  const k = 1024;
  const sizes = ['B', 'KB', 'MB', 'GB'];
  const i = Math.floor(Math.log(bytes) / Math.log(k));
  return parseFloat((bytes / Math.pow(k, i)).toFixed(2)) + ' ' + sizes[i];
};

// 判断是否为文本文件
const isTextFile = (filename: string) => {
  const textExtensions = ['.txt', '.json', '.js', '.ts', '.vue', '.html', '.css', '.scss', '.md', '.yml', '.yaml', '.xml', '.sh'];
  return textExtensions.some(ext => filename.toLowerCase().endsWith(ext));
};

// 搜索文件
const searchFiles = () => {
  // 搜索逻辑已在computed中实现
};

// 创建文件夹
const createFolder = () => {
  Modal.confirm({
    title: '新建文件夹',
    content: '请输入文件夹名称',
    onOk: () => {
      Message.success('文件夹创建成功');
      refreshFiles();
    }
  });
};

// 上传前处理
const beforeUpload = (file: File) => {
  Message.info(`正在上传文件: ${file.name}`);
  return true;
};

// 下载文件
const downloadFile = (file: any) => {
  Message.info(`正在下载文件: ${file.name}`);
  // 实际实现中应该调用下载API
};

// 编辑文件
const editFile = async (file: any) => {
  editingFile.value = file;
  editModalVisible.value = true;
  
  // 模拟加载文件内容
  fileContent.value = `// 这是文件 ${file.name} 的内容\n// 实际使用时会从容器中读取真实内容\n\nconsole.log('Hello from ${file.name}');`;
};

// 保存文件
const saveFile = () => {
  Message.success(`文件 ${editingFile.value?.name} 保存成功`);
  editModalVisible.value = false;
};

// 删除文件
const deleteFile = (file: any) => {
  Modal.confirm({
    title: '确认删除',
    content: `确定要删除 ${file.name} 吗？`,
    onOk: () => {
      Message.success(`${file.name} 删除成功`);
      refreshFiles();
    }
  });
};

onMounted(() => {
  // 如果URL中有容器ID参数，自动选择
  const containerId = route.query.containerId as string;
  if (containerId) {
    selectedContainer.value = containerId;
    refreshFiles();
  }
});
</script>

<style lang="scss" scoped>
.file-management-page {
  padding: 20px;
  
  .page-header {
    margin-bottom: 20px;
    
    h2 {
      margin: 0 0 8px 0;
      font-size: 24px;
      font-weight: 600;
    }
    
    p {
      margin: 0;
      color: var(--color-text-2);
    }
  }
  
  .navigation-card {
    margin-bottom: 20px;
  }
  
  .file-editor {
    .editor-header {
      margin-bottom: 12px;
      padding: 8px 12px;
      background: var(--color-fill-2);
      border-radius: 4px;
      font-weight: 500;
    }
    
    .editor-textarea {
      font-family: 'Consolas', 'Monaco', 'Courier New', monospace;
      font-size: 14px;
    }
  }
  
  :deep(.arco-table-tbody .arco-table-tr) {
    cursor: pointer;
    
    &:hover {
      background-color: var(--color-fill-1);
    }
  }
}
</style>
