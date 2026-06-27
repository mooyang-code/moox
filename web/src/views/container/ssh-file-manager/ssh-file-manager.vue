<template>
  <div class="ssh-file-manager-page">
    <SpaceContextBar />
    <!-- 路径输入框 -->
    <div class="path-input-bar">
      <a-input
        v-model="pathInput"
        placeholder="输入路径后按回车导航"
        allow-clear
        @press-enter="onPathInputEnter"
      >
        <template #prefix>
          <icon-folder />
        </template>
      </a-input>
    </div>

    <!-- 工具栏 -->
    <div class="toolbar">
      <a-space>
        <a-button @click="goParent">
          <template #icon><icon-left /></template>
          返回上级
        </a-button>
        <a-button type="primary" @click="onMkdirClick">
          <template #icon><icon-folder-add /></template>
          新建目录
        </a-button>
        <a-upload
          :action="uploadAction"
          :show-file-list="false"
          :data="uploadFormData"
          name="file"
          @success="onUploadSuccess"
          @error="onUploadError"
        >
          <template #upload-button>
            <a-button type="primary" status="success">
              <template #icon><icon-upload /></template>
              上传文件
            </a-button>
          </template>
        </a-upload>
        <a-button @click="refresh">
          <template #icon><icon-refresh /></template>
          刷新
        </a-button>
      </a-space>
    </div>

    <!-- 文件列表表格 -->
    <a-table
      row-key="path"
      :loading="loading"
      :data="fileList"
      :pagination="false"
      :bordered="false"
      @row-dblclick="onRowDblClick"
    >
      <template #columns>
        <a-table-column title="名称" data-index="name" :width="240">
          <template #cell="{ record }">
            <a-space>
              <icon-folder v-if="record.type === 'd'" style="color: #ffb84d; font-size: 18px;" />
              <icon-file v-else style="color: #4080ff; font-size: 18px;" />
              <span class="file-name">{{ record.name }}</span>
            </a-space>
          </template>
        </a-table-column>
        <a-table-column title="类型" data-index="type" :width="70">
          <template #cell="{ record }">
            <a-tag size="small" :color="record.type === 'd' ? 'orangered' : 'blue'">
              {{ record.type === 'd' ? '目录' : '文件' }}
            </a-tag>
          </template>
        </a-table-column>
        <a-table-column title="大小" data-index="size" :width="70">
          <template #cell="{ record }">
            {{ record.type === 'd' ? '-' : formatFileSize(record.size) }}
          </template>
        </a-table-column>
        <a-table-column title="权限" data-index="mode" :width="130">
          <template #cell="{ record }">
            <a-tag size="small">{{ record.mode }}</a-tag>
          </template>
        </a-table-column>
        <a-table-column title="修改时间" data-index="mod_time" :width="180">
          <template #cell="{ record }">
            {{ record.mod_time }}
          </template>
        </a-table-column>
        <a-table-column title="操作" :width="80">
          <template #cell="{ record }">
            <a-space :size="4">
              <a-button
                v-if="record.type === 'f'"
                type="text"
                size="small"
                @click.stop="downloadFile(record)"
              >
                <template #icon><icon-download /></template>
                下载
              </a-button>
              <a-popconfirm
                content="确定要删除吗？此操作不可恢复。"
                ok-text="确定"
                cancel-text="取消"
                @ok="() => deleteItem(record)"
                position="tr"
              >
                <a-button type="text" size="small" status="danger" @click.stop>
                  <template #icon><icon-delete /></template>
                  删除
                </a-button>
              </a-popconfirm>
            </a-space>
          </template>
        </a-table-column>
      </template>
    </a-table>

    <!-- 新建目录弹窗 -->
    <a-modal
      v-model:visible="mkdirModalVisible"
      title="新建目录"
      @ok="onMkdirConfirm"
      @cancel="mkdirModalVisible = false"
      :ok-loading="mkdirLoading"
    >
      <a-form :model="{ dirName: newDirName }" layout="vertical">
        <a-form-item label="目录名称">
          <a-input
            v-model="newDirName"
            placeholder="请输入目录名称"
            allow-clear
            @press-enter="onMkdirConfirm"
          />
        </a-form-item>
      </a-form>
    </a-modal>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, watch } from 'vue';
import { useRoute } from 'vue-router';
import { Message } from '@arco-design/web-vue';
import SpaceContextBar from '@/components/SpaceContextBar/index.vue';
import {
  sftpList,
  sftpMkdir,
  sftpDelete,
  getSftpDownloadUrl,
  getSftpUploadUrl,
  type SftpFileItem,
} from '@/api/modules/ssh';

const props = defineProps<{
  sessionId?: string;
}>();

const route = useRoute();

// ---------- 状态 ----------
const resolvedSessionId = computed(() => props.sessionId || (route.query.sessionId as string) || '');
const loading = ref(false);
const fileList = ref<SftpFileItem[]>([]);
const breadcrumbs = ref<{ name: string; dir: string }[]>([]);
const currentDir = ref('/');
const pathInput = ref('/');

// 新建目录相关
const mkdirModalVisible = ref(false);
const mkdirLoading = ref(false);
const newDirName = ref('');

// ---------- 上传相关 ----------
const uploadAction = computed(() => getSftpUploadUrl());

const uploadFormData = computed(() => ({
  session_id: resolvedSessionId.value,
  path: currentDir.value,
}));

// ---------- 工具函数 ----------
const formatFileSize = (bytes: number): string => {
  if (bytes === 0) return '0 B';
  const units = ['B', 'KB', 'MB', 'GB', 'TB'];
  const k = 1024;
  const i = Math.floor(Math.log(bytes) / Math.log(k));
  const size = parseFloat((bytes / Math.pow(k, i)).toFixed(2));
  return `${size} ${units[i]}`;
};

// ---------- 数据加载 ----------
const loadDir = async (path: string) => {
  if (!resolvedSessionId.value) {
    Message.warning('缺少 sessionId 参数');
    return;
  }
  loading.value = true;
  try {
    const response = await sftpList(resolvedSessionId.value, path);
    const res = response.data;
    if (res.ret_info?.code === 0) {
      fileList.value = res.files || [];
      breadcrumbs.value = res.paths || [];
      currentDir.value = res.current_dir;
      pathInput.value = res.current_dir;
    } else {
      Message.error(res.ret_info?.msg || '加载目录失败');
    }
  } catch (error) {
    console.error('加载目录失败:', error);
    Message.error('加载目录失败');
  } finally {
    loading.value = false;
  }
};

const refresh = () => {
  loadDir(currentDir.value);
};

// ---------- 导航 ----------
const goParent = () => {
  if (currentDir.value === '/') return;
  const parts = currentDir.value.replace(/\/+$/, '').split('/');
  parts.pop();
  const parentDir = parts.length <= 1 ? '/' : parts.join('/');
  loadDir(parentDir);
};

const onPathInputEnter = () => {
  const target = pathInput.value.trim();
  if (!target) return;
  loadDir(target);
};

const onRowDblClick = (record: SftpFileItem) => {
  if (record.type === 'd') {
    loadDir(record.path);
  }
};

// ---------- 新建目录 ----------
const onMkdirClick = () => {
  newDirName.value = '';
  mkdirModalVisible.value = true;
};

const onMkdirConfirm = async () => {
  const name = newDirName.value.trim();
  if (!name) {
    Message.warning('请输入目录名称');
    return;
  }
  mkdirLoading.value = true;
  try {
    const targetPath = currentDir.value === '/'
      ? `/${name}`
      : `${currentDir.value}/${name}`;
    const response = await sftpMkdir(resolvedSessionId.value, targetPath);
    const res = response.data;
    if (res.ret_info?.code === 0) {
      Message.success('目录创建成功');
      mkdirModalVisible.value = false;
      refresh();
    } else {
      Message.error(res.ret_info?.msg || '创建目录失败');
    }
  } catch (error) {
    console.error('创建目录失败:', error);
    Message.error('创建目录失败');
  } finally {
    mkdirLoading.value = false;
  }
};

// ---------- 上传回调 ----------
const onUploadSuccess = () => {
  Message.success('文件上传成功');
  refresh();
};

const onUploadError = () => {
  Message.error('文件上传失败');
};

// ---------- 下载 ----------
const downloadFile = (record: SftpFileItem) => {
  const url = getSftpDownloadUrl(resolvedSessionId.value, record.path);
  const a = document.createElement('a');
  a.href = url;
  a.download = record.name;
  document.body.appendChild(a);
  a.click();
  document.body.removeChild(a);
};

// ---------- 删除 ----------
const deleteItem = async (record: SftpFileItem) => {
  try {
    const response = await sftpDelete(resolvedSessionId.value, record.path);
    const res = response.data;
    if (res.ret_info?.code === 0) {
      Message.success('删除成功');
      refresh();
    } else {
      Message.error(res.ret_info?.msg || '删除失败');
    }
  } catch (error) {
    console.error('删除失败:', error);
    Message.error('删除失败');
  }
};

// ---------- 初始化 & 监听 ----------
// 当 sessionId 变化时（包括首次传入），重新加载根目录
watch(resolvedSessionId, (id) => {
  if (id) {
    currentDir.value = '/';
    pathInput.value = '/';
    loadDir('/');
  }
}, { immediate: true });
</script>

<style lang="scss" scoped>
.ssh-file-manager-page {
  box-sizing: border-box;
  flex: 1;
  padding: 20px;
  overflow-y: auto;
  background: #fff;

  .path-input-bar {
    margin-bottom: 12px;
  }

  .toolbar {
    margin-bottom: 16px;
  }

  :deep(.arco-table-tbody .arco-table-tr) {
    cursor: pointer;

    &:hover {
      background-color: var(--color-fill-1);
    }

    .file-name {
      transition: color 0.15s;
    }

    &:hover .file-name {
      color: rgb(var(--primary-6, 22, 93, 255));
    }
  }
}
</style>
