<template>
  <a-modal
    v-model:visible="visible"
    title="云账户管理"
    :width="960"
    :mask-closable="false"
    :footer="false"
    @cancel="handleCancel"
  >
    <div class="cloud-account-manage">
      <a-row>
        <a-button type="primary" status="success" @click="onAdd">
          <template #icon><icon-plus /></template>
          新增云账户
        </a-button>
      </a-row>

      <a-table
        row-key="account_id"
        size="small"
        :data="accountList"
        :bordered="{ cell: true }"
        :loading="loading"
        :scroll="{ y: 400 }"
        style="margin-top: var(--moox-space-4)"
      >
        <template #columns>
          <a-table-column title="账户名称" data-index="account_name" :width="180"></a-table-column>
          <a-table-column title="云厂商" data-index="provider" :width="120">
            <template #cell="{ record }">
              <a-tag :color="getProviderColor(record.provider)">
                {{ getProviderName(record.provider) }}
              </a-tag>
            </template>
          </a-table-column>
          <a-table-column title="云密钥" data-index="credential_secret_id" :width="220">
            <template #cell="{ record }">
              <span>{{ secretName(record.credential_secret_id) }}</span>
            </template>
          </a-table-column>
          <a-table-column title="应用ID" data-index="app_id" :width="150">
            <template #cell="{ record }">
              <span>{{ record.app_id || "-" }}</span>
            </template>
          </a-table-column>
          <a-table-column title="COS区域" data-index="cos_region" :width="150">
            <template #cell="{ record }">
              <span>{{ record.cos_region || "-" }}</span>
            </template>
          </a-table-column>
          <a-table-column title="COS桶名" data-index="cos_bucket" :width="200">
            <template #cell="{ record }">
              <span>{{ record.cos_bucket || "-" }}</span>
            </template>
          </a-table-column>
          <a-table-column title="创建时间" data-index="created_at" :width="180">
            <template #cell="{ record }">
              {{ formatTime(record.created_at || record.create_time) }}
            </template>
          </a-table-column>
          <a-table-column title="操作" :width="160" align="center" fixed="right">
            <template #cell="{ record }">
              <a-space>
                <a-button type="primary" size="mini" status="success" @click="onEdit(record)">
                  <template #icon><icon-edit /></template>
                  编辑
                </a-button>
                <a-popconfirm
                  content="确定要删除该云账户吗？删除后相关的云函数节点将无法使用。"
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

    <!-- 新增/编辑弹窗 -->
    <a-modal
      v-model:visible="formVisible"
      :title="isEdit ? '编辑云账户' : '新增云账户'"
      :width="600"
      :mask-closable="false"
      @cancel="handleFormCancel"
      @ok="handleFormOk"
    >
      <a-form :model="form" layout="vertical" ref="formRef">
        <a-form-item field="account_name" label="账户名称" required>
          <a-input v-model="form.account_name" placeholder="请输入账户名称" />
        </a-form-item>

        <a-form-item field="provider" label="云厂商" required>
          <a-select v-model="form.provider" placeholder="请选择云厂商" :disabled="isEdit">
            <a-option value="tencent">腾讯云</a-option>
          </a-select>
        </a-form-item>

        <a-form-item field="credential_secret_id" label="云密钥" required>
          <a-select v-model="form.credential_secret_id" placeholder="请选择已启用的腾讯云密钥">
            <a-option v-for="secret in cloudSecrets" :key="secret.secret_id" :value="secret.secret_id">
              {{ secret.name }} ({{ secret.key_id || secret.secret_id }})
            </a-option>
          </a-select>
        </a-form-item>

        <a-divider v-if="form.provider === 'tencent'" orientation="left">COS配置(可选)</a-divider>

        <a-form-item v-if="form.provider === 'tencent'" field="cos_url" label="COS控制台URL">
          <a-input
            v-model="cosUrl"
            placeholder="粘贴COS控制台URL,如:https://console.cloud.tencent.com/cos/bucket?bucket=xxx&region=xxx"
            @blur="parseCosUrl"
          />
          <template #extra>
            <span style="color: #86909c; font-size: 12px"> 粘贴COS控制台URL后将自动解析并填充应用ID、桶名和地区 </span>
          </template>
        </a-form-item>

        <a-form-item field="app_id" label="应用ID">
          <a-input v-model="form.app_id" placeholder="请输入应用ID（可选）" />
        </a-form-item>

        <a-form-item field="cos_region" label="COS区域">
          <a-input v-model="form.cos_region" placeholder="请输入COS区域（可选），如：ap-guangzhou" />
        </a-form-item>

        <a-form-item field="cos_bucket" label="COS桶名">
          <a-input v-model="form.cos_bucket" placeholder="请输入COS桶名（可选）" />
        </a-form-item>

        <a-form-item field="extra_config" label="额外配置（可选）">
          <a-textarea v-model="form.extra_config" placeholder='JSON格式的额外配置，例如：{"region": "ap-guangzhou"}' :rows="4" />
        </a-form-item>
      </a-form>
    </a-modal>
  </a-modal>
</template>

<script setup lang="ts">
import { ref, watch, reactive } from "vue";
import { Message } from "@arco-design/web-vue";
import {
  getCloudAccountList,
  createCloudAccount,
  updateCloudAccount,
  deleteCloudAccount,
  type CloudAccount
} from "@/api/cloud-account";
import { listSecrets, type Secret } from "@/api/admin/secret";

// Props
const props = defineProps<{
  modelValue: boolean;
}>();

// Emits
const emit = defineEmits<{
  "update:modelValue": [value: boolean];
  refresh: [];
}>();

// 响应式数据
const visible = ref(props.modelValue);
const loading = ref(false);
const accountList = ref<CloudAccount[]>([]);
const cloudSecrets = ref<Secret[]>([]);
const total = ref(0);
const formVisible = ref(false);
const isEdit = ref(false);
const formRef = ref();

// 表单数据
const defaultForm = {
  account_id: "",
  account_name: "",
  provider: "tencent",
  credential_secret_id: "",
  app_id: "",
  cos_region: "",
  cos_bucket: "",
  extra_config: ""
};

const form = reactive({ ...defaultForm });

// COS URL 输入框
const cosUrl = ref("");

// 监听属性变化
watch(
  () => props.modelValue,
  newVal => {
    visible.value = newVal;
    if (newVal) {
      loadAccountList();
      loadCloudSecrets();
    }
  }
);

watch(visible, newVal => {
  emit("update:modelValue", newVal);
});

// 加载账户列表
const loadAccountList = async () => {
  loading.value = true;
  try {
    const accounts = await getCloudAccountList();
    accountList.value = accounts || [];
    total.value = accountList.value.length;
  } catch (error) {
    console.error("加载云账户列表失败:", error);
    Message.error(error instanceof Error ? error.message : "加载云账户失败：请确认已登录且 moox-cloudnode 服务已部署");
  } finally {
    loading.value = false;
  }
};

const loadCloudSecrets = async () => {
  const response = await listSecrets({ category: "cloud", provider: "tencent", status: "active", limit: 200 });
  cloudSecrets.value = response.secrets ?? [];
};

const secretName = (secretId: string) => {
  const secret = cloudSecrets.value.find(item => item.secret_id === secretId);
  return secret ? `${secret.name} (${secret.key_id || secretId})` : secretId;
};

// 生成唯一的account_id
const generateAccountId = () => {
  const timestamp = Date.now();
  const random = Math.floor(Math.random() * 1000);
  return `account_${timestamp}_${random}`;
};

// 新增
const onAdd = () => {
  isEdit.value = false;
  Object.assign(form, {
    ...defaultForm,
    account_id: generateAccountId()
  });
  cosUrl.value = "";
  formVisible.value = true;
};

// 编辑
const onEdit = (record: CloudAccount) => {
  isEdit.value = true;
  Object.assign(form, {
    account_id: record.account_id,
    account_name: record.account_name,
    provider: record.provider,
    credential_secret_id: record.credential_secret_id,
    app_id: record.app_id || "",
    cos_region: record.cos_region || "",
    cos_bucket: record.cos_bucket || "",
    extra_config: record.extra_config || ""
  });
  cosUrl.value = "";
  formVisible.value = true;
};

// 删除
const onDelete = async (record: CloudAccount) => {
  try {
    await deleteCloudAccount(record.account_id);
    Message.success("删除成功");
    await loadAccountList();
    emit("refresh");
  } catch (error) {
    console.error("删除云账户失败:", error);
    Message.error("删除云账户失败");
  }
};

// 表单取消
const handleFormCancel = () => {
  formVisible.value = false;
};

// 解析COS URL
const parseCosUrl = () => {
  if (!cosUrl.value) return;

  try {
    const url = new URL(cosUrl.value);
    const params = new URLSearchParams(url.search);

    // 获取bucket参数
    const bucket = params.get("bucket");
    // 获取region参数
    const region = params.get("region");

    if (bucket) {
      form.cos_bucket = bucket;

      // 从桶名中提取应用ID
      // 桶名格式通常为: bucketname-appid
      const parts = bucket.split("-");
      if (parts.length >= 2) {
        const appId = parts[parts.length - 1];
        // 验证是否为纯数字
        if (/^\d+$/.test(appId)) {
          form.app_id = appId;
        }
      }
    }

    if (region) {
      form.cos_region = region;
    }

    if (bucket || region) {
      Message.success("已自动填充COS配置信息");
    } else {
      Message.warning("无法从URL中解析出有效的配置信息");
    }
  } catch {
    Message.error("URL格式不正确,请检查后重试");
  }
};

// 表单确认
const handleFormOk = async () => {
  // 表单验证
  const errors = await formRef.value?.validate();
  if (errors) {
    return;
  }

  // 验证额外配置的JSON格式
  if (form.extra_config) {
    try {
      JSON.parse(form.extra_config);
    } catch {
      Message.error("额外配置必须是有效的JSON格式");
      return;
    }
  }

  try {
    if (isEdit.value) {
      const updateData: any = {
        account_id: form.account_id,
        account_name: form.account_name,
        provider: form.provider,
        credential_secret_id: form.credential_secret_id,
        app_id: form.app_id,
        cos_region: form.cos_region,
        cos_bucket: form.cos_bucket,
        extra_config: form.extra_config || "{}"
      };

      await updateCloudAccount(form.account_id, updateData);
    } else {
      // 新增
      await createCloudAccount({
        account_id: form.account_id,
        account_name: form.account_name,
        provider: form.provider,
        credential_secret_id: form.credential_secret_id,
        app_id: form.app_id,
        cos_region: form.cos_region,
        cos_bucket: form.cos_bucket,
        extra_config: form.extra_config || "{}"
      });
    }

    Message.success(isEdit.value ? "编辑成功" : "新增成功");
    formVisible.value = false;
    await loadAccountList();
    emit("refresh");
  } catch (error: any) {
    console.error("保存云账户失败:", error);
    Message.error(error?.message || "保存云账户失败");
  }
};

// 关闭弹窗
const handleCancel = () => {
  visible.value = false;
};

// 工具函数
const getProviderName = (provider: string) => {
  const providerMap: Record<string, string> = {
    tencent: "腾讯云",
    aliyun: "阿里云",
    aws: "AWS"
  };
  return providerMap[provider] || provider;
};

const getProviderColor = (provider: string) => {
  const colorMap: Record<string, string> = {
    tencent: "blue",
    aliyun: "orange",
    aws: "purple"
  };
  return colorMap[provider] || "gray";
};

const formatTime = (time: string | undefined) => {
  if (!time) return "-";
  return new Date(time).toLocaleString("zh-CN", {
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit"
  });
};

</script>

<style scoped>
.cloud-account-manage {
  min-height: 500px;
}
</style>
