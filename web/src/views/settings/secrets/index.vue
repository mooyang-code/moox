<template>
  <div class="admin-page">
    <div class="page-head">
      <div>
        <h2>秘钥管理</h2>
        <span>统一管理系统中的各类秘钥（云厂商、SSH、交易所等）</span>
      </div>
      <a-space>
        <a-button type="primary" @click="openCreate">
          <template #icon><icon-plus /></template>
          新增秘钥
        </a-button>
        <a-button @click="load">
          <template #icon><icon-refresh /></template>
          刷新
        </a-button>
      </a-space>
    </div>

    <div class="filter-bar">
      <a-input-search
        v-model="filters.keyword"
        placeholder="搜索名称或描述"
        style="width: 240px"
        allow-clear
        @search="onSearch"
      />
      <a-select v-model="filters.category" placeholder="分类" style="width: 140px" allow-clear @change="load">
        <a-option v-for="item in categoryOptions" :key="item.value" :value="item.value">{{ item.label }}</a-option>
      </a-select>
      <a-select v-model="filters.status" placeholder="状态" style="width: 120px" allow-clear @change="load">
        <a-option value="active">启用</a-option>
        <a-option value="inactive">禁用</a-option>
      </a-select>
    </div>

    <a-table
      row-key="secret_id"
      size="small"
      :bordered="{ cell: true }"
      :loading="loading"
      :data="rows"
      :pagination="pagination"
      :scroll="{ x: 'max-content' }"
      @page-change="onPageChange"
      @page-size-change="onPageSizeChange"
    >
      <template #columns>
        <a-table-column title="名称" data-index="name" :width="180" />
        <a-table-column title="分类" :width="100">
          <template #cell="{ record }">
            <a-tag size="small" :color="categoryColor(record.category)">{{ categoryLabel(record.category) }}</a-tag>
          </template>
        </a-table-column>
        <a-table-column title="提供方" data-index="provider" :width="110" />
        <a-table-column title="类型" data-index="secret_type" :width="110" />
        <a-table-column title="标识 (Key ID)" data-index="key_id" :width="200" :ellipsis="true" :tooltip="true" />
        <a-table-column title="秘钥值" :width="140">
          <template #cell>
            <a-space>
              <span>******</span>
              <a-tooltip content="秘钥已加密存储，列表不展示明文">
                <icon-info-circle />
              </a-tooltip>
            </a-space>
          </template>
        </a-table-column>
        <a-table-column title="状态" :width="90">
          <template #cell="{ record }">
            <a-tag size="small" :color="record.status === 'active' ? 'green' : 'gray'">
              {{ record.status === 'active' ? '启用' : '禁用' }}
            </a-tag>
          </template>
        </a-table-column>
        <a-table-column title="创建人" data-index="creator" :width="100" />
        <a-table-column title="最后使用" :width="180">
          <template #cell="{ record }">{{ formatTime(record.last_used_at) || '-' }}</template>
        </a-table-column>
        <a-table-column title="更新时间" :width="180">
          <template #cell="{ record }">{{ formatTime(record.modify_time) }}</template>
        </a-table-column>
        <a-table-column title="操作" :width="200" align="center" :fixed="'right'">
          <template #cell="{ record }">
            <a-space>
              <a-popconfirm :content="toggleConfirmText(record)" @ok="toggleStatus(record)">
                <a-button size="mini" type="text">
                  {{ record.status === 'active' ? '禁用' : '启用' }}
                </a-button>
              </a-popconfirm>
              <a-button size="mini" type="text" @click="openEdit(record)">编辑</a-button>
              <a-popconfirm content="确认删除该秘钥？" @ok="remove(record)">
                <a-button size="mini" type="text" status="danger">删除</a-button>
              </a-popconfirm>
            </a-space>
          </template>
        </a-table-column>
      </template>
    </a-table>

    <!-- 新增/编辑弹窗 -->
    <a-modal v-model:visible="visible" width="720px" :title="modalTitle" @before-ok="submit" @cancel="visible = false">
      <a-form :model="form" auto-label-width>
        <a-form-item field="name" label="名称" required>
          <a-input v-model="form.name" placeholder="例如：腾讯云-生产环境" />
        </a-form-item>
        <a-form-item field="category" label="分类" required>
          <a-select v-model="form.category" placeholder="选择分类">
            <a-option v-for="item in categoryOptions" :key="item.value" :value="item.value">{{ item.label }}</a-option>
          </a-select>
        </a-form-item>
        <a-form-item field="provider" label="提供方">
          <a-input v-model="form.provider" placeholder="例如：tencent / aliyun / binance" />
        </a-form-item>
        <a-form-item field="secret_type" label="类型">
          <a-select v-model="form.secret_type" placeholder="选择类型">
            <a-option value="api_key">API 密钥对</a-option>
            <a-option value="password">密码</a-option>
            <a-option value="token">访问令牌</a-option>
            <a-option value="certificate">证书</a-option>
            <a-option value="ssh_key">SSH 密钥</a-option>
            <a-option value="other">其他</a-option>
          </a-select>
        </a-form-item>
        <a-form-item field="key_id" label="标识 (Key ID)">
          <a-input v-model="form.key_id" placeholder="公开标识，如 SecretId / 用户名 / API Key" />
        </a-form-item>
        <a-form-item field="secret_value" label="秘钥值" :required="!editing">
          <a-input-password
            v-model="form.secret_value"
            :placeholder="editing ? '留空表示不修改秘钥值' : '输入秘钥值'"
            allow-clear
          />
        </a-form-item>
        <a-form-item field="extra_config" label="额外配置">
          <a-textarea
            v-model="form.extra_config"
            :auto-size="{ minRows: 2, maxRows: 5 }"
            placeholder="JSON 格式，用于存储额外参数"
          />
        </a-form-item>
        <a-form-item field="description" label="描述">
          <a-textarea v-model="form.description" :auto-size="{ minRows: 2, maxRows: 4 }"></a-textarea>
        </a-form-item>
      </a-form>
    </a-modal>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue';
import { Message } from '@arco-design/web-vue';
import {
  listSecrets,
  createSecret,
  updateSecret,
  deleteSecret,
  toggleSecretStatus,
  type Secret,
} from '@/api/admin/secret';
import { defaultPagination, formatTime } from '@/views/data/shared/metadata-utils';

defineOptions({ name: 'SettingsSecrets' });

const rows = ref<Secret[]>([]);
const loading = ref(false);
const visible = ref(false);
const editing = ref(false);
const pagination = reactive(defaultPagination());

const filters = reactive({
  keyword: '',
  category: '',
  status: '',
});

const categoryOptions = [
  { label: '云厂商', value: 'cloud' },
  { label: 'SSH 凭证', value: 'ssh' },
  { label: '交易所', value: 'exchange' },
  { label: '数据库', value: 'database' },
  { label: '系统令牌', value: 'jwt' },
  { label: '其他', value: 'other' },
];

function categoryLabel(value: string) {
  return categoryOptions.find((o) => o.value === value)?.label || value;
}

function categoryColor(value: string) {
  const map: Record<string, string> = {
    cloud: 'blue',
    ssh: 'cyan',
    exchange: 'orange',
    database: 'purple',
    jwt: 'red',
    other: 'gray',
  };
  return map[value] || 'gray';
}

const form = reactive({
  secret_id: '',
  name: '',
  description: '',
  category: 'cloud',
  provider: '',
  secret_type: 'api_key',
  key_id: '',
  secret_value: '',
  extra_config: '{}',
});

const modalTitle = computed(() => (editing.value ? '编辑秘钥' : '新增秘钥'));

async function load() {
  loading.value = true;
  try {
    const rsp = await listSecrets({
      keyword: filters.keyword || undefined,
      category: filters.category || undefined,
      status: filters.status || undefined,
      offset: (pagination.current - 1) * pagination.pageSize,
      limit: pagination.pageSize,
    });
    rows.value = rsp.secrets || [];
    pagination.total = rsp.total || 0;
  } finally {
    loading.value = false;
  }
}

function onSearch() {
  pagination.current = 1;
  load();
}

function resetForm() {
  Object.assign(form, {
    secret_id: '',
    name: '',
    description: '',
    category: 'cloud',
    provider: '',
    secret_type: 'api_key',
    key_id: '',
    secret_value: '',
    extra_config: '{}',
  });
}

function openCreate() {
  editing.value = false;
  resetForm();
  visible.value = true;
}

function openEdit(record: Secret) {
  editing.value = true;
  Object.assign(form, {
    secret_id: record.secret_id || '',
    name: record.name || '',
    description: record.description || '',
    category: record.category || 'cloud',
    provider: record.provider || '',
    secret_type: record.secret_type || 'api_key',
    key_id: record.key_id || '',
    secret_value: '',
    extra_config: record.extra_config || '{}',
  });
  visible.value = true;
}

async function submit(): Promise<boolean> {
  if (!form.name) {
    Message.warning('请填写秘钥名称');
    return false;
  }
  if (!form.category) {
    Message.warning('请选择分类');
    return false;
  }
  if (!editing.value && !form.secret_value) {
    Message.warning('请输入秘钥值');
    return false;
  }
  // JSON 校验
  if (form.extra_config) {
    try {
      JSON.parse(form.extra_config);
    } catch {
      Message.warning('额外配置不是有效的 JSON 格式');
      return false;
    }
  }
  try {
    if (editing.value) {
      await updateSecret({
        secret_id: form.secret_id,
        name: form.name,
        description: form.description,
        key_id: form.key_id,
        secret_value: form.secret_value || undefined,
        extra_config: form.extra_config,
        category: form.category,
        provider: form.provider,
        secret_type: form.secret_type,
      });
      Message.success('秘钥已更新');
    } else {
      await createSecret({
        name: form.name,
        description: form.description,
        category: form.category,
        provider: form.provider,
        secret_type: form.secret_type,
        key_id: form.key_id,
        secret_value: form.secret_value,
        extra_config: form.extra_config,
      });
      Message.success('秘钥已创建');
    }
    await load();
    return true;
  } catch {
    return false;
  }
}

async function remove(record: Secret) {
  if (!record.secret_id) {
    Message.warning('秘钥标识缺失，无法删除');
    return;
  }
  await deleteSecret(record.secret_id);
  Message.success('秘钥已删除');
  await load();
}

function toggleConfirmText(record: Secret) {
  return `确认${record.status === 'active' ? '禁用' : '启用'}该秘钥？`;
}

async function toggleStatus(record: Secret) {
  if (!record.secret_id) {
    Message.warning('秘钥标识缺失，无法操作');
    return;
  }
  const newStatus = record.status === 'active' ? 'inactive' : 'active';
  await toggleSecretStatus(record.secret_id, newStatus);
  Message.success(newStatus === 'active' ? '已启用' : '已禁用');
  await load();
}

function onPageChange(page: number) {
  pagination.current = page;
  load();
}

function onPageSizeChange(pageSize: number) {
  pagination.current = 1;
  pagination.pageSize = pageSize;
  load();
}

onMounted(load);
</script>

<style scoped>
.admin-page {
  padding: 20px;
}

.page-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 16px;
}

.page-head h2 {
  margin: 0 0 4px;
  font-size: 20px;
  font-weight: 600;
}

.page-head span {
  color: var(--color-text-3);
}

.filter-bar {
  display: flex;
  gap: 12px;
  margin-bottom: 16px;
}
</style>
