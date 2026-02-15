<template>
  <div class="ssh-hosts-page">
    <div class="page-header">
      <h2>主机管理</h2>
      <p>管理 SSH 主机配置，支持密码和证书两种认证方式连接远程服务器</p>
    </div>

    <div class="page-content">
      <a-card :bordered="false" class="host-card">
        <!-- 工具栏 -->
        <div class="toolbar">
          <a-input
            v-model="keyword"
            placeholder="搜索主机名称或地址"
            allow-clear
            style="width: 280px"
            @press-enter="onSearch"
            @clear="onSearch"
          >
            <template #prefix>
              <icon-search />
            </template>
          </a-input>
          <a-button type="primary" @click="onAdd">
            <template #icon><icon-plus /></template>
            <span>新增主机</span>
          </a-button>
        </div>

        <!-- 主机列表 -->
        <a-table
          row-key="id"
          :loading="loading"
          :data="hostList"
          :bordered="false"
          :pagination="false"
          :scroll="{ x: 900 }"
        >
          <template #columns>
            <a-table-column title="名称" data-index="name" :width="160">
              <template #cell="{ record }">
                <span class="host-name">{{ record.name }}</span>
              </template>
            </a-table-column>
            <a-table-column title="地址" data-index="address" :width="180" />
            <a-table-column title="端口" data-index="port" :width="80" align="center" />
            <a-table-column title="用户" data-index="user" :width="120" />
            <a-table-column title="认证方式" :width="100" align="center">
              <template #cell="{ record }">
                <a-tag v-if="record.auth_type === 'pwd'" size="small" color="arcoblue">密码</a-tag>
                <a-tag v-else size="small" color="green">证书</a-tag>
              </template>
            </a-table-column>
            <a-table-column title="操作" :width="200" align="center" fixed="right">
              <template #cell="{ record }">
                <a-space>
                  <a-link type="primary" @click="onConnect(record)">连接</a-link>
                  <a-link @click="onEdit(record)">编辑</a-link>
                  <a-popconfirm
                    content="确定要删除该主机吗？删除后将无法恢复。"
                    ok-text="确定"
                    cancel-text="取消"
                    @ok="() => onDelete(record)"
                    position="tr"
                  >
                    <a-link status="danger">删除</a-link>
                  </a-popconfirm>
                </a-space>
              </template>
            </a-table-column>
          </template>
        </a-table>

        <!-- 分页 -->
        <div class="pagination-wrapper">
          <a-pagination
            v-model:current="pagination.current"
            v-model:page-size="pagination.pageSize"
            :total="total"
            show-total
            show-jumper
            show-page-size
            :page-size-options="[10, 20, 50]"
            @change="onPageChange"
            @page-size-change="onPageSizeChange"
          />
        </div>
      </a-card>
    </div>

    <!-- 新增 / 编辑主机弹窗 -->
    <a-modal
      v-model:visible="modalVisible"
      :title="isEdit ? '编辑主机' : '新增主机'"
      :width="620"
      :ok-loading="submitLoading"
      @before-ok="handleSubmit"
      @cancel="handleCancel"
      unmount-on-close
    >
      <a-form
        ref="formRef"
        :model="formData"
        :rules="formRules"
        auto-label-width
        layout="vertical"
      >
        <!-- 基本信息 -->
        <a-typography-title :heading="6" style="margin-top: 0; margin-bottom: 16px;">基本信息</a-typography-title>
        <a-row :gutter="16">
          <a-col :span="12">
            <a-form-item field="name" label="名称" validate-trigger="blur">
              <a-input v-model="formData.name" placeholder="请输入主机名称" allow-clear />
            </a-form-item>
          </a-col>
          <a-col :span="12">
            <a-form-item field="address" label="地址" validate-trigger="blur">
              <a-input v-model="formData.address" placeholder="请输入主机地址" allow-clear />
            </a-form-item>
          </a-col>
        </a-row>
        <a-row :gutter="16">
          <a-col :span="12">
            <a-form-item field="port" label="端口" validate-trigger="blur">
              <a-input-number
                v-model="formData.port"
                placeholder="请输入端口号"
                :min="1"
                :max="65535"
                :style="{ width: '100%' }"
              />
            </a-form-item>
          </a-col>
          <a-col :span="12">
            <a-form-item field="user" label="用户" validate-trigger="blur">
              <a-input v-model="formData.user" placeholder="请输入用户名" allow-clear />
            </a-form-item>
          </a-col>
        </a-row>

        <!-- 认证方式 -->
        <a-typography-title :heading="6" style="margin-bottom: 16px;">认证方式</a-typography-title>
        <a-form-item field="auth_type" label="认证类型">
          <a-radio-group v-model="formData.auth_type" type="button">
            <a-radio value="pwd">密码</a-radio>
            <a-radio value="cert">证书</a-radio>
          </a-radio-group>
        </a-form-item>

        <template v-if="formData.auth_type === 'pwd'">
          <a-form-item field="password" label="密码">
            <a-input-password v-model="formData.password" placeholder="请输入密码" allow-clear />
          </a-form-item>
        </template>
        <template v-else>
          <a-form-item field="cert_data" label="证书内容">
            <a-textarea
              v-model="formData.cert_data"
              placeholder="请粘贴 PEM 格式的私钥内容"
              :auto-size="{ minRows: 4, maxRows: 8 }"
              allow-clear
            />
          </a-form-item>
          <a-form-item field="cert_pwd" label="证书密码">
            <a-input-password v-model="formData.cert_pwd" placeholder="证书密码（如有）" allow-clear />
          </a-form-item>
        </template>

        <!-- 终端设置 -->
        <a-collapse :default-active-key="[]" :bordered="false" expand-icon-position="right" class="form-collapse">
          <a-collapse-item key="terminal" header="终端设置">
            <a-row :gutter="16">
              <a-col :span="12">
                <a-form-item field="font_size" label="字体大小">
                  <a-input-number
                    v-model="formData.font_size"
                    placeholder="14"
                    :min="8"
                    :max="36"
                    :style="{ width: '100%' }"
                  />
                </a-form-item>
              </a-col>
              <a-col :span="12">
                <a-form-item field="font_family" label="字体">
                  <a-select v-model="formData.font_family" placeholder="请选择字体" allow-clear>
                    <a-option value="Consolas">Consolas</a-option>
                    <a-option value="Monaco">Monaco</a-option>
                    <a-option value="Courier New">Courier New</a-option>
                    <a-option value="monospace">monospace</a-option>
                    <a-option value="Menlo">Menlo</a-option>
                    <a-option value="Source Code Pro">Source Code Pro</a-option>
                  </a-select>
                </a-form-item>
              </a-col>
            </a-row>
            <a-row :gutter="16">
              <a-col :span="8">
                <a-form-item field="background" label="背景色">
                  <a-input v-model="formData.background" placeholder="#1e1e1e" allow-clear />
                </a-form-item>
              </a-col>
              <a-col :span="8">
                <a-form-item field="foreground" label="前景色">
                  <a-input v-model="formData.foreground" placeholder="#d4d4d4" allow-clear />
                </a-form-item>
              </a-col>
              <a-col :span="8">
                <a-form-item field="cursor_color" label="光标颜色">
                  <a-input v-model="formData.cursor_color" placeholder="#d4d4d4" allow-clear />
                </a-form-item>
              </a-col>
            </a-row>
            <a-form-item field="cursor_style" label="光标样式">
              <a-select v-model="formData.cursor_style" placeholder="请选择光标样式" allow-clear>
                <a-option value="block">Block</a-option>
                <a-option value="underline">Underline</a-option>
                <a-option value="bar">Bar</a-option>
              </a-select>
            </a-form-item>
          </a-collapse-item>

          <!-- 高级配置 -->
          <a-collapse-item key="advanced" header="高级配置">
            <a-row :gutter="16">
              <a-col :span="12">
                <a-form-item field="shell" label="Shell">
                  <a-input v-model="formData.shell" placeholder="/bin/bash" allow-clear />
                </a-form-item>
              </a-col>
              <a-col :span="12">
                <a-form-item field="pty_type" label="PTY 类型">
                  <a-input v-model="formData.pty_type" placeholder="xterm-256color" allow-clear />
                </a-form-item>
              </a-col>
            </a-row>
            <a-form-item field="init_cmd" label="初始命令">
              <a-textarea
                v-model="formData.init_cmd"
                placeholder="连接后自动执行的命令，每行一条"
                :auto-size="{ minRows: 3, maxRows: 6 }"
                allow-clear
              />
            </a-form-item>
          </a-collapse-item>
        </a-collapse>
      </a-form>
    </a-modal>
  </div>
</template>

<script setup lang="ts">
import { ref, reactive, onMounted } from 'vue';
import { useRouter } from 'vue-router';
import { Message } from '@arco-design/web-vue';
import { listSSHHosts, createSSHHost, updateSSHHost, deleteSSHHost, type SSHHost } from '@/api/modules/ssh';

const router = useRouter();

// ---------- 列表数据 ----------
const loading = ref(false);
const keyword = ref('');
const hostList = ref<SSHHost[]>([]);
const total = ref(0);
const pagination = reactive({
  current: 1,
  pageSize: 20,
});

// ---------- 弹窗状态 ----------
const modalVisible = ref(false);
const isEdit = ref(false);
const submitLoading = ref(false);
const formRef = ref();

const getDefaultFormData = (): Partial<SSHHost> => ({
  name: '',
  address: '',
  port: 22,
  user: 'root',
  auth_type: 'pwd',
  net_type: 'tcp4',
  password: '',
  cert_data: '',
  cert_pwd: '',
  font_size: 14,
  background: '#1e1e1e',
  foreground: '#d4d4d4',
  cursor_color: '#d4d4d4',
  font_family: 'Consolas',
  cursor_style: 'block',
  shell: '/bin/bash',
  pty_type: 'xterm-256color',
  init_cmd: '',
});

const formData = ref<Partial<SSHHost>>(getDefaultFormData());

// ---------- 表单校验 ----------
const formRules = {
  name: [{ required: true, message: '请输入主机名称' }],
  address: [{ required: true, message: '请输入主机地址' }],
  port: [
    { required: true, message: '请输入端口号' },
    {
      validator: (value: number, callback: (error?: string) => void) => {
        if (value < 1 || value > 65535) {
          callback('端口范围为 1 - 65535');
        } else {
          callback();
        }
      },
    },
  ],
  user: [{ required: true, message: '请输入用户名' }],
};

// ---------- 数据加载 ----------
const fetchHosts = async () => {
  loading.value = true;
  try {
    const response = await listSSHHosts({
      keyword: keyword.value || undefined,
      offset: (pagination.current - 1) * pagination.pageSize,
      limit: pagination.pageSize,
    });
    const res = response.data;
    if (res.code === 200) {
      hostList.value = res.data;
      total.value = res.total || 0;
    }
  } catch (error) {
    console.error('加载主机列表失败:', error);
    Message.error('加载主机列表失败');
  } finally {
    loading.value = false;
  }
};

// ---------- 搜索 ----------
const onSearch = () => {
  pagination.current = 1;
  fetchHosts();
};

// ---------- 分页 ----------
const onPageChange = (page: number) => {
  pagination.current = page;
  fetchHosts();
};

const onPageSizeChange = (pageSize: number) => {
  pagination.pageSize = pageSize;
  pagination.current = 1;
  fetchHosts();
};

// ---------- 操作：连接 ----------
const onConnect = (record: SSHHost) => {
  router.push({
    path: '/container-management/ssh-terminal',
    query: { hostId: String(record.id) },
  });
};

// ---------- 操作：新增 ----------
const onAdd = () => {
  isEdit.value = false;
  formData.value = getDefaultFormData();
  modalVisible.value = true;
};

// ---------- 操作：编辑 ----------
const onEdit = (record: SSHHost) => {
  isEdit.value = true;
  formData.value = { ...record };
  modalVisible.value = true;
};

// ---------- 操作：删除 ----------
const onDelete = async (record: SSHHost) => {
  if (!record.id) return;
  try {
    const response = await deleteSSHHost(record.id);
    const res = response.data;
    if (res.code === 200) {
      Message.success('删除成功');
      fetchHosts();
    } else {
      Message.error(res.message || '删除失败');
    }
  } catch (error) {
    console.error('删除主机失败:', error);
    Message.error('删除主机失败');
  }
};

// ---------- 弹窗提交 ----------
const handleSubmit = async (done: (closed: boolean) => void) => {
  try {
    const errors = await formRef.value?.validate();
    if (errors) {
      done(false);
      return;
    }

    submitLoading.value = true;

    const payload: Partial<SSHHost> = { ...formData.value };

    let response;
    if (isEdit.value) {
      response = await updateSSHHost(payload);
    } else {
      response = await createSSHHost(payload);
    }

    const res = response.data;
    if (res.code === 200) {
      Message.success(isEdit.value ? '更新成功' : '创建成功');
      done(true);
      fetchHosts();
    } else {
      Message.error(res.message || (isEdit.value ? '更新失败' : '创建失败'));
      done(false);
    }
  } catch (error) {
    console.error('提交失败:', error);
    Message.error('操作失败，请检查网络连接');
    done(false);
  } finally {
    submitLoading.value = false;
  }
};

const handleCancel = () => {
  formRef.value?.resetFields();
};

// ---------- 初始化 ----------
onMounted(() => {
  fetchHosts();
});
</script>

<style lang="scss" scoped>
.ssh-hosts-page {
  padding: 20px;
  min-height: calc(100vh - 120px);

  .page-header {
    margin-bottom: 20px;

    h2 {
      margin: 0 0 8px 0;
      font-size: 22px;
      font-weight: 600;
      color: var(--color-text-1);
    }

    p {
      margin: 0;
      font-size: 14px;
      color: var(--color-text-3);
    }
  }

  .page-content {
    .host-card {
      border-radius: 6px;

      :deep(.arco-card-body) {
        padding: 20px;
      }
    }
  }

  .toolbar {
    display: flex;
    justify-content: space-between;
    align-items: center;
    margin-bottom: 16px;
  }

  .host-name {
    font-weight: 500;
    color: var(--color-text-1);
  }

  .pagination-wrapper {
    display: flex;
    justify-content: flex-end;
    margin-top: 16px;
    padding-top: 16px;
    border-top: 1px solid var(--color-border-1);
  }

  .form-collapse {
    margin-top: 8px;

    :deep(.arco-collapse-item) {
      border-bottom: none;
    }

    :deep(.arco-collapse-item-header) {
      padding: 8px 0;
      font-weight: 500;
      color: var(--color-text-2);
      background: transparent;
    }

    :deep(.arco-collapse-item-content) {
      padding: 12px 0 0;
      background: transparent;
    }

    :deep(.arco-collapse-item-content-box) {
      padding: 0;
    }
  }
}
</style>
