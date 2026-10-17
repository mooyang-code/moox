<template>
  <div class="moox-page">
    <div class="moox-inner">
      <div class="page-head">
        <h2>秘钥管理</h2>
        <a-space wrap>
          <a-input-search
            v-model="filters.keyword"
            placeholder="搜索名称或描述"
            style="width: 240px"
            allow-clear
            @search="onSearch"
            @clear="onSearch"
          />
          <a-select v-model="filters.category" placeholder="分类" style="width: 140px" allow-clear @change="load">
            <a-option v-for="item in categoryOptions" :key="item.value" :value="item.value">{{ item.label }}</a-option>
          </a-select>
          <a-select v-model="filters.status" placeholder="状态" style="width: 120px" allow-clear @change="load">
            <a-option value="active">启用</a-option>
            <a-option value="inactive">禁用</a-option>
          </a-select>
          <a-button type="primary" status="success" @click="openCreate">
            <template #icon><icon-plus /></template>
            新增秘钥
          </a-button>
          <template v-if="isAdmin">
            <a-button @click="openExport">
              <template #icon><icon-download /></template>
              导出全部
            </a-button>
            <a-button @click="openImport">
              <template #icon><icon-upload /></template>
              导入
            </a-button>
          </template>
        </a-space>
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
                {{ record.status === "active" ? "启用" : "禁用" }}
              </a-tag>
            </template>
          </a-table-column>
          <a-table-column title="创建人" data-index="creator" :width="100" />
          <a-table-column title="最后使用" :width="180">
            <template #cell="{ record }">{{ formatTime(record.last_used_at) || "-" }}</template>
          </a-table-column>
          <a-table-column title="更新时间" :width="180">
            <template #cell="{ record }">{{ formatTime(record.modify_time) }}</template>
          </a-table-column>
          <a-table-column title="操作" :width="200" align="center" :fixed="'right'">
            <template #cell="{ record }">
              <a-space>
                <a-popconfirm :content="toggleConfirmText(record)" @ok="toggleStatus(record)">
                  <a-button size="mini" type="text">
                    {{ record.status === "active" ? "禁用" : "启用" }}
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
            <a-input v-model="form.name" placeholder="例如：交易所-生产环境" />
          </a-form-item>
          <a-form-item field="category" label="分类" required>
            <a-select v-model="form.category" placeholder="选择分类">
              <a-option v-for="item in categoryOptions" :key="item.value" :value="item.value">{{ item.label }}</a-option>
            </a-select>
          </a-form-item>
          <a-form-item field="provider" label="提供方">
            <a-input v-model="form.provider" placeholder="例如：binance / okx / mysql" />
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

      <!-- 导出：全部秘钥连同明文用口令加密成迁移包，在服务端完成，浏览器只拿到密文 -->
      <a-modal
        v-model:visible="exportVisible"
        width="560px"
        title="导出全部秘钥"
        ok-text="导出"
        :ok-loading="exporting"
        @before-ok="submitExport"
        @close="resetExport"
      >
        <a-alert type="warning" class="transfer-alert">
          迁移包包含全部秘钥的明文（系统秘钥与配置备份除外），用下面的口令加密；在新环境“导入”时需要同一口令。
          口令丢失无法找回，迁移包和口令请分开保管。
        </a-alert>
        <a-form :model="exportForm" auto-label-width>
          <a-form-item field="passphrase" label="导出口令" required :help="`至少 ${MIN_PASSPHRASE_LENGTH} 个字符`">
            <a-input-password v-model="exportForm.passphrase" autocomplete="new-password" allow-clear />
          </a-form-item>
          <a-form-item field="confirm" label="确认口令" required>
            <a-input-password v-model="exportForm.confirm" autocomplete="new-password" allow-clear />
          </a-form-item>
        </a-form>
      </a-modal>

      <!-- 导入：按 secret_id 导入迁移包，引用这些秘钥的配置迁移后仍然有效 -->
      <a-modal
        v-model:visible="importVisible"
        width="600px"
        title="导入秘钥"
        :ok-text="importResult ? '完成' : '导入'"
        :ok-loading="importing"
        :hide-cancel="!!importResult"
        @before-ok="submitImport"
        @close="resetImport"
      >
        <template v-if="importResult">
          <a-result status="success" :title="importSummary(importResult)" class="transfer-result" />
          <div v-if="importResult.skipped?.length" class="transfer-list">
            <strong>已存在、未覆盖：</strong>{{ importResult.skipped.join("、") }}
          </div>
          <div v-if="importResult.failed?.length" class="transfer-list is-failed">
            <strong>导入失败：</strong>
            <div v-for="item in importResult.failed" :key="item">{{ item }}</div>
          </div>
        </template>
        <a-form v-else :model="importForm" auto-label-width>
          <a-form-item label="迁移包" required>
            <a-space>
              <a-button @click="pickFile">选择文件</a-button>
              <span class="transfer-file">{{ importForm.fileName || "从秘钥管理“导出全部”得到的 .json 文件" }}</span>
            </a-space>
            <input ref="fileInput" type="file" accept=".json,application/json" hidden @change="onFileChange" />
          </a-form-item>
          <a-form-item field="passphrase" label="导出口令" required>
            <a-input-password v-model="importForm.passphrase" autocomplete="off" allow-clear />
          </a-form-item>
          <a-form-item field="overwrite" label="已存在时">
            <a-radio-group v-model="importForm.overwrite">
              <a-radio :value="false">跳过</a-radio>
              <a-radio :value="true">用迁移包覆盖</a-radio>
            </a-radio-group>
          </a-form-item>
        </a-form>
      </a-modal>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref } from "vue";
import { Message } from "@arco-design/web-vue";
import { storeToRefs } from "pinia";
import {
  listSecrets,
  createSecret,
  updateSecret,
  deleteSecret,
  toggleSecretStatus,
  exportSecrets,
  importSecrets,
  type ImportSecretsRsp,
  type Secret
} from "@/api/admin/secret";
import { useUserInfoStore } from "@/store/modules/user-info";
import { defaultPagination, formatTime } from "@/views/data/shared/metadata-utils";
import { MIN_PASSPHRASE_LENGTH, exportFileName, importSummary, passphraseError, saveTextFile } from "./secret-transfer";

defineOptions({ name: "SettingsSecrets" });

const rows = ref<Secret[]>([]);
const loading = ref(false);
const visible = ref(false);
const editing = ref(false);
const pagination = reactive(defaultPagination());

const filters = reactive({
  keyword: "",
  category: "",
  status: ""
});

const categoryOptions = [
  { label: "SSH 凭证", value: "ssh" },
  { label: "交易所", value: "exchange" },
  { label: "数据库", value: "database" },
  { label: "系统令牌", value: "jwt" },
  { label: "其他", value: "other" }
];

function categoryLabel(value: string) {
  return categoryOptions.find(o => o.value === value)?.label || value;
}

function categoryColor(value: string) {
  const map: Record<string, string> = {
    ssh: "cyan",
    exchange: "orange",
    database: "purple",
    jwt: "red",
    other: "gray"
  };
  return map[value] || "gray";
}

const form = reactive({
  secret_id: "",
  name: "",
  description: "",
  category: "ssh",
  provider: "",
  secret_type: "api_key",
  key_id: "",
  secret_value: "",
  extra_config: "{}"
});

const modalTitle = computed(() => (editing.value ? "编辑秘钥" : "新增秘钥"));

async function load() {
  loading.value = true;
  try {
    const rsp = await listSecrets({
      keyword: filters.keyword || undefined,
      category: filters.category || undefined,
      status: filters.status || undefined,
      offset: (pagination.current - 1) * pagination.pageSize,
      limit: pagination.pageSize
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
    secret_id: "",
    name: "",
    description: "",
    category: "ssh",
    provider: "",
    secret_type: "api_key",
    key_id: "",
    secret_value: "",
    extra_config: "{}"
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
    secret_id: record.secret_id || "",
    name: record.name || "",
    description: record.description || "",
    category: record.category || "ssh",
    provider: record.provider || "",
    secret_type: record.secret_type || "api_key",
    key_id: record.key_id || "",
    secret_value: "",
    extra_config: record.extra_config || "{}"
  });
  visible.value = true;
}

async function submit(): Promise<boolean> {
  if (!form.name) {
    Message.warning("请填写秘钥名称");
    return false;
  }
  if (!form.category) {
    Message.warning("请选择分类");
    return false;
  }
  if (!editing.value && !form.secret_value) {
    Message.warning("请输入秘钥值");
    return false;
  }
  // JSON 校验
  if (form.extra_config) {
    try {
      JSON.parse(form.extra_config);
    } catch {
      Message.warning("额外配置不是有效的 JSON 格式");
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
        secret_type: form.secret_type
      });
      Message.success("秘钥已更新");
    } else {
      await createSecret({
        name: form.name,
        description: form.description,
        category: form.category,
        provider: form.provider,
        secret_type: form.secret_type,
        key_id: form.key_id,
        secret_value: form.secret_value,
        extra_config: form.extra_config
      });
      Message.success("秘钥已创建");
    }
    await load();
    return true;
  } catch {
    return false;
  }
}

async function remove(record: Secret) {
  if (!record.secret_id) {
    Message.warning("秘钥标识缺失，无法删除");
    return;
  }
  await deleteSecret(record.secret_id);
  Message.success("秘钥已删除");
  await load();
}

function toggleConfirmText(record: Secret) {
  return `确认${record.status === "active" ? "禁用" : "启用"}该秘钥？`;
}

async function toggleStatus(record: Secret) {
  if (!record.secret_id) {
    Message.warning("秘钥标识缺失，无法操作");
    return;
  }
  const newStatus = record.status === "active" ? "inactive" : "active";
  await toggleSecretStatus(record.secret_id, newStatus);
  Message.success(newStatus === "active" ? "已启用" : "已禁用");
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

// 导出、导入只对全局管理员开放（迁移包里有全部秘钥的明文），其他用户不显示入口。
const { account } = storeToRefs(useUserInfoStore());
const isAdmin = computed(() => !!account.value.user?.admin);

const exportVisible = ref(false);
const exporting = ref(false);
const exportForm = reactive({ passphrase: "", confirm: "" });

function openExport() {
  resetExport();
  exportVisible.value = true;
}

function resetExport() {
  Object.assign(exportForm, { passphrase: "", confirm: "" });
}

async function submitExport(): Promise<boolean> {
  const error = passphraseError(exportForm.passphrase, exportForm.confirm);
  if (error) {
    Message.warning(error);
    return false;
  }
  exporting.value = true;
  try {
    const rsp = await exportSecrets(exportForm.passphrase);
    saveTextFile(exportFileName(new Date()), rsp.bundle);
    Message.success(`已导出 ${rsp.count || 0} 个秘钥`);
    resetExport();
    return true;
  } catch {
    return false;
  } finally {
    exporting.value = false;
  }
}

const importVisible = ref(false);
const importing = ref(false);
const importResult = ref<ImportSecretsRsp | null>(null);
const fileInput = ref<HTMLInputElement>();
const importForm = reactive({ fileName: "", bundle: "", passphrase: "", overwrite: false });

function openImport() {
  resetImport();
  importVisible.value = true;
}

function resetImport() {
  importResult.value = null;
  Object.assign(importForm, { fileName: "", bundle: "", passphrase: "", overwrite: false });
  if (fileInput.value) fileInput.value.value = "";
}

function pickFile() {
  fileInput.value?.click();
}

async function onFileChange(event: Event) {
  const file = (event.target as HTMLInputElement).files?.[0];
  if (!file) return;
  importForm.fileName = file.name;
  importForm.bundle = await file.text();
}

async function submitImport(): Promise<boolean> {
  if (importResult.value) return true;
  if (!importForm.bundle) {
    Message.warning("请选择迁移包文件");
    return false;
  }
  if (!importForm.passphrase) {
    Message.warning("请输入导出时设置的口令");
    return false;
  }
  importing.value = true;
  try {
    importResult.value = await importSecrets({
      bundle: importForm.bundle,
      passphrase: importForm.passphrase,
      overwrite: importForm.overwrite
    });
    importForm.passphrase = "";
    await load();
  } catch {
    // 口令错误、文件不对等由全局错误提示展示，弹窗保持打开以便重试。
  } finally {
    importing.value = false;
  }
  return false;
}

onMounted(load);
</script>

<style scoped>
.page-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: var(--moox-space-toolbar-table);
}

.page-head h2 {
  margin: 0;
  font-size: 20px;
  font-weight: 600;
}

.transfer-alert {
  margin-bottom: 16px;
}

.transfer-file {
  color: var(--color-text-3);
}

.transfer-result {
  padding: 8px 0 16px;
}

.transfer-list {
  margin-top: 8px;
  color: var(--color-text-2);
  line-height: 1.8;
}

.transfer-list.is-failed {
  color: rgb(var(--red-6));
}

@media (max-width: 900px) {
  .page-head {
    align-items: stretch;
    flex-direction: column;
  }
  .page-head :deep(.arco-space) {
    justify-content: flex-end;
  }
}
</style>
