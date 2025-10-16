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
        <a-button type="primary" @click="onAdd">
          <template #icon><icon-plus /></template>
          新增云账户
        </a-button>
      </a-row>
      
      <a-table
        row-key="account_id"
        :data="accountList"
        :bordered="{ cell: true }"
        :loading="loading"
        :scroll="{ y: 400 }"
        style="margin-top: 16px"
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
          <a-table-column title="Access Key ID" data-index="secret_id" :width="200">
            <template #cell="{ record }">
              <a-space>
                <span>{{ record.secret_id }}</span>
                <icon-copy style="cursor: pointer;" @click="copyToClipboard(record.secret_id)" />
              </a-space>
            </template>
          </a-table-column>
          <a-table-column title="Secret Key" data-index="secret_key" :width="200">
            <template #cell="{ record }">
              <a-space>
                <span>{{ record.secret_key }}</span>
                <a-tooltip content="密钥已加密存储，显示为掩码">
                  <icon-info-circle />
                </a-tooltip>
              </a-space>
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
            <a-option value="aliyun">阿里云</a-option>
            <a-option value="aws">AWS</a-option>
          </a-select>
        </a-form-item>
        
        <a-form-item field="secret_id" label="Access Key ID" required>
          <a-input v-model="form.secret_id" placeholder="请输入Access Key ID" />
        </a-form-item>
        
        <a-form-item field="secret_key" label="Secret Key" :required="!isEdit">
          <a-input-password 
            v-model="form.secret_key" 
            :placeholder="isEdit ? '如不修改请留空' : '请输入Secret Key'"
            allow-clear
          />
          <template #extra>
            <span style="color: #86909c; font-size: 12px;">
              {{ isEdit ? '留空表示不修改密钥' : '密钥将加密存储' }}
            </span>
          </template>
        </a-form-item>
        
        <a-form-item field="extra_config" label="额外配置（可选）">
          <a-textarea 
            v-model="form.extra_config" 
            placeholder="JSON格式的额外配置，例如：{&quot;region&quot;: &quot;ap-guangzhou&quot;}" 
            :rows="4"
          />
        </a-form-item>
      </a-form>
    </a-modal>
  </a-modal>
</template>

<script setup lang="ts">
import { ref, watch, reactive } from 'vue';
import { Message } from '@arco-design/web-vue';
import { 
  getCloudAccountList, 
  createCloudAccount, 
  updateCloudAccount, 
  deleteCloudAccount,
  type CloudAccount 
} from '@/api/cloud-account';

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
const accountList = ref<CloudAccount[]>([]);
const formVisible = ref(false);
const isEdit = ref(false);
const formRef = ref();

// 表单数据
const defaultForm = {
  account_id: '',
  account_name: '',
  provider: 'tencent',
  secret_id: '',
  secret_key: '',
  extra_config: ''
};

const form = reactive({ ...defaultForm });

// 监听属性变化
watch(() => props.modelValue, (newVal) => {
  visible.value = newVal;
  if (newVal) {
    loadAccountList();
  }
});

watch(visible, (newVal) => {
  emit('update:modelValue', newVal);
});

// 加载账户列表
const loadAccountList = async () => {
  loading.value = true;
  try {
    const response = await getCloudAccountList();
    // 兼容两种响应格式
    if (response?.code === 200 && response?.data) {
      // 处理数组格式的响应：response.data 可能是数组
      let data = response.data;
      if (Array.isArray(data)) {
        accountList.value = data;
      } else {
        accountList.value = [data].filter(Boolean);
      }
    } else if (response?.ret_info?.code === 0) {
      // 处理数组格式的响应：response.ret_info.data 可能是数组
      let data = response.ret_info.data;
      if (Array.isArray(data)) {
        accountList.value = data;
      } else {
        accountList.value = [data].filter(Boolean);
      }
    } else {
      accountList.value = [];
    }
  } catch (error) {
    console.error('加载云账户列表失败:', error);
    Message.error('加载云账户列表失败');
  } finally {
    loading.value = false;
  }
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
  formVisible.value = true;
};

// 编辑
const onEdit = (record: CloudAccount) => {
  isEdit.value = true;
  Object.assign(form, {
    account_id: record.account_id,
    account_name: record.account_name,
    provider: record.provider,
    secret_id: record.secret_id,
    secret_key: '', // 编辑时密钥留空
    extra_config: record.extra_config || ''
  });
  formVisible.value = true;
};

// 删除
const onDelete = async (record: CloudAccount) => {
  try {
    const response = await deleteCloudAccount(record.account_id);
    if (response?.data?.code === 200 || response?.data?.ret_info?.code === 0) {
      Message.success('删除成功');
      await loadAccountList();
      emit('refresh');
    } else {
      throw new Error('删除失败');
    }
  } catch (error) {
    console.error('删除云账户失败:', error);
    Message.error('删除云账户失败');
  }
};

// 表单取消
const handleFormCancel = () => {
  formVisible.value = false;
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
    } catch (e) {
      Message.error('额外配置必须是有效的JSON格式');
      return;
    }
  }

  try {
    let response;
    if (isEdit.value) {
      // 编辑时，如果密钥为空，则不传递secret_key字段
      const updateData: any = {
        account_id: form.account_id,
        account_name: form.account_name,
        provider: form.provider,
        secret_id: form.secret_id,
        extra_config: form.extra_config || '{}'
      };
      
      if (form.secret_key) {
        updateData.secret_key = form.secret_key;
      }
      
      response = await updateCloudAccount(form.account_id, updateData);
    } else {
      // 新增
      response = await createCloudAccount({
        account_id: form.account_id,
        account_name: form.account_name,
        provider: form.provider,
        secret_id: form.secret_id,
        secret_key: form.secret_key,
        extra_config: form.extra_config || '{}'
      });
    }

    if (response?.data?.code === 200 || response?.data?.ret_info?.code === 0) {
      Message.success(isEdit.value ? '编辑成功' : '新增成功');
      formVisible.value = false;
      await loadAccountList();
      emit('refresh');
    } else {
      throw new Error(response?.data?.message || '操作失败');
    }
  } catch (error: any) {
    console.error('保存云账户失败:', error);
    Message.error(error?.message || '保存云账户失败');
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

const getProviderColor = (provider: string) => {
  const colorMap: Record<string, string> = {
    'tencent': 'blue',
    'aliyun': 'orange',
    'aws': 'purple'
  };
  return colorMap[provider] || 'gray';
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

// 复制到剪贴板
const copyToClipboard = async (text: string) => {
  try {
    await navigator.clipboard.writeText(text);
    Message.success('已复制到剪贴板');
  } catch (error) {
    Message.error('复制失败');
  }
};
</script>

<style scoped>
.cloud-account-manage {
  min-height: 500px;
}
</style>