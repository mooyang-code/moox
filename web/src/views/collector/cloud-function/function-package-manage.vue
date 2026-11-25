<template>
  <!-- 外层用抽屉，里面还会再打开“上传”弹窗，免得弹窗叠弹窗 -->
  <a-drawer
    :visible="visible"
    title="代码包"
    width="min(1200px, 100vw)"
    :mask-closable="false"
    :footer="false"
    @update:visible="handlePackageManageVisibleChange"
    @cancel="handleCancel"
  >
    <div class="function-package-manage">
      <div class="package-toolbar">
        <a-input v-model="keyword" placeholder="搜索包名或版本" style="width: 220px" allow-clear>
          <template #prefix><icon-search /></template>
        </a-input>
        <span class="muted">对账器按 scf_plan 配置的包名取本空间最新的可用代码包；每个包名只保留最新 3 个。</span>
        <a-button class="package-upload-button" type="primary" @click="onAdd">
          <template #icon><icon-plus /></template>
          上传代码包
        </a-button>
      </div>

      <a-table
        row-key="package_id"
        size="small"
        :data="filteredPackages"
        :bordered="{ cell: true }"
        :loading="loading"
        :pagination="false"
      >
        <template #empty>
          <div class="empty-text">{{ loading ? "读取中…" : "本空间还没有代码包" }}</div>
        </template>
        <template #columns>
          <a-table-column title="包名" data-index="package_name" :width="180" />
          <a-table-column title="版本" data-index="version" :width="120" />
          <a-table-column title="运行时" data-index="runtime" :width="90" />
          <a-table-column title="大小" :width="100" align="right">
            <template #cell="{ record }">{{ formatFileSize(record.file_size) }}</template>
          </a-table-column>
          <a-table-column title="状态" :width="80">
            <template #cell="{ record }">
              <span :class="record.status === 'available' ? '' : 'warn-text'">{{ packageStatusLabel(record.status) }}</span>
            </template>
          </a-table-column>
          <a-table-column title="云账号" :width="160">
            <template #cell="{ record }">{{ accountName(record.cloud_account_id) }}</template>
          </a-table-column>
          <a-table-column title="COS 位置">
            <template #cell="{ record }">
              <span class="mono-text single-line" :title="cosLocation(record)">{{ cosLocation(record) }}</span>
            </template>
          </a-table-column>
          <a-table-column title="MD5" :width="140">
            <template #cell="{ record }">
              <span class="mono-text single-line" :title="record.file_md5">{{ record.file_md5 || "-" }}</span>
            </template>
          </a-table-column>
          <a-table-column title="上传时间" :width="190">
            <template #cell="{ record }">{{ formatInstant(record.created_at, "UTC") }}</template>
          </a-table-column>
          <a-table-column title="操作" :width="80" align="center" fixed="right">
            <template #cell="{ record }">
              <a-popconfirm content="删除这个代码包（COS 对象一并删除）？" position="tr" @ok="onDelete(record)">
                <a-button size="mini" status="danger">删除</a-button>
              </a-popconfirm>
            </template>
          </a-table-column>
        </template>
      </a-table>
    </div>

    <a-modal v-model:visible="uploadVisible" title="上传代码包" :width="600" :mask-closable="false" @cancel="handleUploadCancel">
      <a-form :model="uploadForm" layout="vertical">
        <a-form-item field="package_name" label="包名" required>
          <a-input v-model="uploadForm.package_name" placeholder="与 scf_plan 配置里的 package_name 一致" />
        </a-form-item>

        <a-form-item field="version" label="版本" required>
          <a-input v-model="uploadForm.version" placeholder="从文件名解析" readonly disabled />
          <template #extra>
            <span class="muted">从文件名解析，格式：xxx-v1.0.45.zip</span>
          </template>
        </a-form-item>

        <a-form-item label="运行时">{{ uploadForm.runtime }}</a-form-item>

        <a-form-item field="cloud_account_id" label="云账号" required>
          <a-select v-model="uploadForm.cloud_account_id" placeholder="选择存放代码包的云账号（COS）">
            <a-option v-for="account in cloudAccountOptions" :key="account.account_id" :value="account.account_id">
              {{ account.account_name || account.account_id }}
            </a-option>
          </a-select>
        </a-form-item>

        <a-form-item label="代码包文件">
          <a-upload
            :file-list="fileList"
            :auto-upload="false"
            :show-file-list="true"
            :limit="1"
            accept=".zip"
            @change="onFileChange"
          >
            <template #upload-button>
              <div class="upload-area">
                <div><icon-upload class="upload-icon" /></div>
                <div>点击或拖拽上传 ZIP 文件</div>
                <div class="muted">不超过 100MB</div>
              </div>
            </template>
          </a-upload>
        </a-form-item>
      </a-form>

      <template #footer>
        <a-space>
          <a-button @click="handleUploadCancel">取消</a-button>
          <a-button type="primary" :loading="uploading" @click="handleUploadOk">上传</a-button>
        </a-space>
      </template>
    </a-modal>
  </a-drawer>
</template>

<script setup lang="ts">
import { computed, reactive, ref, watch } from "vue";
import { Message } from "@arco-design/web-vue";
import type { FileItem } from "@arco-design/web-vue";
import {
  deleteFunctionPackage,
  formatFileSize,
  listFunctionPackages,
  packageStatusLabel,
  uploadFunctionPackage,
  type FunctionPackage
} from "@/api/function-package";
import { getCloudAccountList, type CloudAccount } from "@/api/cloud-account";
import { useSpaceStore } from "@/store/modules/space";
import { selectedSpaceGeneration } from "@/api/admin/space-header";
import { createLatestRequestGuard } from "@/utils/latest-request";
import { formatInstant } from "@/utils/time-zone";

const props = defineProps<{ modelValue?: boolean }>();
const emit = defineEmits<{ "update:modelValue": [value: boolean] }>();

const MAX_PACKAGE_SIZE = 100 * 1024 * 1024;
// 云函数一律用 Go1 运行时创建，代码包的运行时只是登记
const PACKAGE_RUNTIME = "Go1";

const spaceStore = useSpaceStore();
const listGuard = createLatestRequestGuard();
// 上传提交序号：取消/关闭/重新打开上传弹窗、切换空间都会使进行中的提交失效
let uploadSubmitToken = 0;
const visible = ref(Boolean(props.modelValue));
const loading = ref(false);
const packageList = ref<FunctionPackage[]>([]);
const cloudAccountOptions = ref<CloudAccount[]>([]);
const keyword = ref("");
const uploadVisible = ref(false);
const uploading = ref(false);
const fileList = ref<FileItem[]>([]);

const defaultUploadForm = { package_name: "", version: "", runtime: PACKAGE_RUNTIME, cloud_account_id: "" };
const uploadForm = reactive({ ...defaultUploadForm });

const filteredPackages = computed(() => {
  const text = keyword.value.trim().toLowerCase();
  if (!text) return packageList.value;
  return packageList.value.filter(item => `${item.package_name} ${item.version}`.toLowerCase().includes(text));
});

watch(
  () => props.modelValue,
  open => {
    visible.value = Boolean(open);
    if (open) void Promise.all([loadPackageList(), loadCloudAccounts()]);
  }
);

watch(visible, open => {
  emit("update:modelValue", open);
  if (!open) resetForSpace();
});

// 关闭抽屉或切换空间：作废在途请求，清空列表，避免组件常驻时重新打开看到其他空间的数据
function resetForSpace() {
  uploadSubmitToken += 1;
  uploading.value = false;
  listGuard.invalidate();
  loading.value = false;
  packageList.value = [];
}

watch(
  () => spaceStore.selectedSpaceId,
  (spaceId, oldSpaceId) => {
    if (spaceId === oldSpaceId) return;
    resetForSpace();
    if (visible.value) void loadPackageList();
  }
);

const handlePackageManageVisibleChange = (open: boolean) => {
  visible.value = open;
};

const handleCancel = () => {
  visible.value = false;
};

async function loadPackageList() {
  const spaceId = spaceStore.selectedSpaceId;
  if (!spaceId) return;
  const request = listGuard.begin();
  loading.value = true;
  try {
    const packages = await listFunctionPackages(spaceId);
    // 抽屉关闭、空间切换或有更新的请求后，旧响应丢弃
    if (!request.isLatest() || spaceId !== spaceStore.selectedSpaceId) return;
    packageList.value = packages;
  } catch (error) {
    if (request.isLatest()) Message.error(`加载代码包失败：${errorText(error)}`);
  } finally {
    if (request.isLatest()) loading.value = false;
  }
}

async function loadCloudAccounts() {
  try {
    cloudAccountOptions.value = await getCloudAccountList();
  } catch (error) {
    cloudAccountOptions.value = [];
    Message.error(`加载云账号失败：${errorText(error)}`);
  }
}

const accountName = (accountId: string) => {
  const account = cloudAccountOptions.value.find(item => item.account_id === accountId);
  return account?.account_name || accountId || "-";
};

const cosLocation = (record: FunctionPackage) =>
  record.cos_path ? `${record.cos_bucket}/${record.cos_path}（${record.cos_region}）` : "-";

function onAdd() {
  Object.assign(uploadForm, {
    ...defaultUploadForm,
    // 与 scf_plan 的默认包名一致；配置里改过包名时手工改成配置里的值
    package_name: `moox-collector-${spaceStore.selectedSpaceId}-plan`,
    cloud_account_id: cloudAccountOptions.value.length === 1 ? cloudAccountOptions.value[0].account_id : ""
  });
  fileList.value = [];
  uploadSubmitToken += 1; // 重新打开视为新的提交，旧提交作废
  uploading.value = false;
  uploadVisible.value = true;
}

/** 选文件时检查格式与大小，并从文件名（xxx-v1.0.45.zip）解析版本。 */
function onFileChange(items: FileItem[], item: FileItem) {
  fileList.value = items;
  uploadForm.version = "";
  const file = item?.file;
  if (!file) return;
  const problem = !file.name.toLowerCase().endsWith(".zip")
    ? "只支持 ZIP 文件"
    : file.size === 0
      ? "文件不能为空"
      : file.size > MAX_PACKAGE_SIZE
        ? "文件不能超过 100MB"
        : "";
  const version = /-v(\d+\.\d+\.\d+)\.zip$/i.exec(file.name)?.[1];
  if (problem || !version) {
    Message.error(problem || "文件名格式不对，应为 xxx-v1.0.45.zip（例如 collector-scf-v1.0.45.zip）");
    fileList.value = [];
    return;
  }
  uploadForm.version = `v${version}`;
}

function handleUploadCancel() {
  uploadSubmitToken += 1; // 取消即作废进行中的提交
  uploading.value = false;
  uploadVisible.value = false;
}

/** 上传前的检查，返回第一条问题；同包名同版本已存在时直接拦下。 */
function uploadProblem(): string {
  const name = uploadForm.package_name.trim();
  if (!name) return "请填写包名";
  if (!fileList.value[0]?.file || !uploadForm.version) return "请选择代码包文件";
  if (!uploadForm.cloud_account_id) return "请选择云账号";
  if (packageList.value.some(item => item.package_name === name && item.version === uploadForm.version)) {
    return `${name} ${uploadForm.version} 已存在，请换一个版本`;
  }
  return "";
}

async function handleUploadOk() {
  const problem = uploadProblem();
  if (problem) {
    Message.error(problem);
    return;
  }
  // 点击时固定发起空间与代次：之后 await 返回时若空间已切换（含 A→B→A），整个提交作废，不能改传到新空间
  const spaceId = spaceStore.selectedSpaceId;
  const generation = selectedSpaceGeneration();
  const token = ++uploadSubmitToken;
  const stale = () =>
    token !== uploadSubmitToken || spaceStore.selectedSpaceId !== spaceId || selectedSpaceGeneration() !== generation;
  uploading.value = true;
  try {
    await uploadFunctionPackage(
      {
        package_name: uploadForm.package_name.trim(),
        version: uploadForm.version,
        runtime: uploadForm.runtime,
        cloud_account_id: uploadForm.cloud_account_id
      },
      fileList.value[0].file as File,
      spaceId
    );
    Message.success("上传成功");
    // 已作废的提交不能关闭用户重新打开、正在填写的新弹窗
    if (stale()) return;
    uploadVisible.value = false;
    void loadPackageList();
  } catch (error) {
    if (!stale()) Message.error(`上传代码包失败：${errorText(error)}`);
  } finally {
    if (!stale()) uploading.value = false;
  }
}

async function onDelete(record: FunctionPackage) {
  try {
    await deleteFunctionPackage(record.space_id || spaceStore.selectedSpaceId, record.package_id);
    Message.success("已删除");
    void loadPackageList();
  } catch (error) {
    Message.error(`删除代码包失败：${errorText(error)}`);
  }
}

const errorText = (error: unknown) => (error instanceof Error && error.message ? error.message : "未知原因");
</script>

<style scoped>
.function-package-manage {
  min-height: 600px;
}

.package-toolbar {
  display: flex;
  flex-wrap: wrap;
  gap: var(--moox-space-2);
  align-items: center;
  margin-bottom: var(--moox-space-2);
}

.package-upload-button {
  margin-left: auto;
}

.upload-area {
  padding: var(--moox-space-6);
  border: 1px dashed var(--color-border-3);
  border-radius: 6px;
  color: var(--color-text-2);
  text-align: center;
  cursor: pointer;
}

.upload-area:hover {
  border-color: rgb(var(--primary-6));
}

.upload-icon {
  color: var(--color-text-4);
  font-size: 32px;
}

.single-line {
  display: block;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.mono-text {
  font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace;
  font-size: 12px;
}

.muted {
  color: var(--color-text-3);
  font-size: 12px;
}

.warn-text {
  color: rgb(var(--warning-6));
}

.empty-text {
  padding: var(--moox-space-4) 0;
  color: var(--color-text-3);
  text-align: center;
}
</style>
