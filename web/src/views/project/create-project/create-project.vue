<template>
  <div class="moox-page">
    <a-spin :loading="loading" style="display: block">
      <div class="moox-inner container create-project-container">
        <a-row justify="center">
          <a-col :xs="22" :sm="18" :md="16" :lg="16" :xl="12" :xxl="12">
            <a-steps :current="currentStep" line-less>
              <a-step description="创建项目">基本信息</a-step>
              <a-step description="创建数据集">数据集</a-step>
              <a-step description="创建成功">完成创建</a-step>
            </a-steps>
          </a-col>
        </a-row>
        <a-row justify="center" class="margin-top">
          <a-col :xs="18" :sm="12" :md="12" :lg="12" :xl="8" :xxl="8">
            <a-form ref="formRef" auto-label-width :model="form" :rules="rules" @submit="handleSubmit">
              <!-- 步骤1：基本信息 -->
              <div v-if="currentStep == 1">
                <a-form-item field="projectName" label="项目名">
                  <a-input v-model="form.projectName" placeholder="请输入项目名" />
                </a-form-item>
                <a-form-item field="projectRemark" label="备注">
                  <a-input v-model="form.projectRemark" placeholder="请输入备注" />
                </a-form-item>
              </div>

              <!-- 步骤2：数据集 -->
              <div v-if="currentStep == 2">
                <a-form-item field="datasetName" label="数据集名">
                  <template #label>
                    <span>数据集名</span>
                    <a-tooltip content="一些相关数据可以放在同一个数据集中存储，例如币安K线数据可以独立一个数据集，币安基本面数据可以独立一个数据集">
                      <icon-question-circle style="margin-left: 4px" />
                    </a-tooltip>
                  </template>
                  <a-input v-model="form.datasetName" placeholder="请输入数据集名" />
                </a-form-item>
                <a-form-item field="dataType" label="数据类型">
                  <a-select v-model="form.dataType" placeholder="请选择数据类型">
                    <a-option value="1">静态数据</a-option>
                    <a-option value="2">时序数据</a-option>
                  </a-select>
                </a-form-item>
                <a-form-item
                  v-if="form.dataType === '2'"
                  field="timePeriod"
                  label="时序周期"
                  :rules="[
                    { required: true, message: '时序数据需要设置时序周期' },
                    { validator: validateTimePeriod }
                  ]"
                >
                  <a-input
                    v-model="form.timePeriod"
                    placeholder="请输入时序周期，如：0（无固定周期）或 1m+5m+1H+1D"
                    @blur="handleTimePeriodBlur"
                  />
                  <template #extra>
                    <div style="font-size: 12px; color: #8c8c8c;">
                      <div v-if="timePeriodValidationMessage" :style="{ color: timePeriodValidationResult?.isValid ? '#52c41a' : '#ff4d4f', marginBottom: '4px' }">
                        {{ timePeriodValidationMessage }}
                      </div>
                      <div>输入 <span style="color: #1890ff;">0</span> 表示无固定周期；多个周期用+分割，例如：1m+5m+1H+1D（1分钟+5分钟+1小时+1天）</div>
                      <div style="margin-top: 4px;">
                        <span>支持的单位：</span>
                        <span style="color: #1890ff;">s(秒) m(分钟) H(小时) D(天) W(周) M(月) Y(年)</span>
                      </div>
                    </div>
                  </template>
                </a-form-item>
                <a-form-item field="validationRules" label="数据校验规则">
                  <a-textarea v-model="form.validationRules" placeholder="请输入JSON格式的数据校验规则（选填）" />
                </a-form-item>
                <a-form-item field="datasetRemark" label="备注">
                  <a-input v-model="form.datasetRemark" placeholder="请输入备注" />
                </a-form-item>
              </div>

              <!-- 步骤3：完成创建 -->
              <div v-if="currentStep == 3">
                <a-result status="success" title="提交成功">
                  <template #subtitle>项目创建成功</template>
                  <template #extra>
                    <a-space>
                      <a-button type="primary">查看详情</a-button>
                      <a-button @click="currentStep = 1">再次创建</a-button>
                    </a-space>
                  </template>
                </a-result>
              </div>

              <a-form-item v-if="currentStep != 3">
                <a-space>
                  <a-button @click="onLastStep" v-if="currentStep != 1">上一步</a-button>
                  <a-button html-type="submit" type="primary">下一步</a-button>
                </a-space>
              </a-form-item>
            </a-form>
          </a-col>
        </a-row>
      </div>
    </a-spin>
  </div>
</template>

<script setup lang="ts" name="CreateProject">
import { ref, onBeforeUnmount } from 'vue';
import { Message } from '@arco-design/web-vue';
import { useRouter } from 'vue-router';
import { api, AUTH_INFO } from '@/api/config';
import { parseFreqInput, validateTimeSeriesFreqs, type TimeSeriesValidationResult } from '@/utils/timeSeriesValidator';

const router = useRouter();

const loading = ref(false);
const currentStep = ref(1);
const createdProjectId = ref('');

// 时序周期验证相关
const timePeriodValidationResult = ref<TimeSeriesValidationResult | null>(null);
const timePeriodValidationMessage = ref<string>('');

interface CreateProjectResponseData {
  ret_info: {
    code: number;
    msg: string;
  };
  proj_id: number;
}

// 提交项目数据到后台
const submitProjectData = async () => {
  try {
    const projectData = {
      auth_info: AUTH_INFO,
      proj_name: form.value.projectName,
      remark: form.value.projectRemark,
      dataset: {
        dataset_name: form.value.datasetName,
        data_type: parseInt(form.value.dataType),
        time_series_period: form.value.timePeriod,
        validation_rule: form.value.validationRules,
        remark: form.value.datasetRemark
      }
    };

    console.log('提交项目数据:', projectData);
    
    const response = await api.post('/metadata/CreateProject', projectData);
    console.log('API响应:', response);
    
    // 现在 response.data 直接是精简后的协议数据
    const data = response.data as CreateProjectResponseData;
    console.log('协议数据:', data);
    
    // 检查协议级别的错误（非0表示错误）
    if (data.ret_info.code !== 0) {
      throw new Error(data.ret_info.msg || '项目创建失败');
    }
    
    // 检查是否有项目ID
    if (!data.proj_id) {
      throw new Error('未返回项目ID');
    }
    
    createdProjectId.value = data.proj_id.toString();
    Message.success({
      content: data.ret_info.msg || '项目创建成功',
      duration: 3000
    });
    return true;
  } catch (error: unknown) {
    console.error('API Error:', error);

    let errorMessage = '项目创建失败';

    if (!error) {
      errorMessage = '未知错误：API调用返回undefined';
    } else if (error && typeof error === 'object') {
      const errorObj = error as any;
      if (errorObj.response?.data?.ret_info?.msg) {
        // HTTP错误响应，使用协议中的消息
        errorMessage = errorObj.response.data.ret_info.msg;
      } else if (errorObj.message) {
        errorMessage = errorObj.message;
      }
    } else if (typeof error === 'string') {
      errorMessage = error;
    } else if (error instanceof Error) {
      errorMessage = error.message;
    }

    Message.error({
      content: errorMessage,
      duration: 3000
    });
    return false;
  }
};

// 时序周期验证函数
const validateTimePeriod = (value: string, callback: (error?: string) => void) => {
  if (!value && form.value.dataType === '2') {
    callback('时序数据需要设置时序周期');
    return;
  }

  if (value && form.value.dataType === '2') {
    const result = validateTimeSeriesFreqs(parseFreqInput(value));
    timePeriodValidationResult.value = result;
    timePeriodValidationMessage.value = result.message;

    if (!result.isValid) {
      callback(result.message);
      return;
    }
  }

  callback();
};

// 时序周期输入框失焦时验证
const handleTimePeriodBlur = () => {
  if (form.value.timePeriod && form.value.dataType === '2') {
    const result = validateTimeSeriesFreqs(parseFreqInput(form.value.timePeriod));
    timePeriodValidationResult.value = result;
    timePeriodValidationMessage.value = result.message;
  } else {
    timePeriodValidationResult.value = null;
    timePeriodValidationMessage.value = '';
  }
};

// 表单提交时的验证
const handleSubmit = async ({ errors }: ArcoDesign.ArcoSubmit) => {
  if (errors) {
    // 获取第一个错误信息
    const firstError = Object.values(errors)[0];
    if (firstError) {
      Message.error({
        content: firstError[0].message,
        duration: 3000
      });
    }
    return;
  }
  
  if (currentStep.value == 3) return;

  if (currentStep.value == 2) {
    loading.value = true;
    try {
      const success = await submitProjectData();
      if (success) {
        currentStep.value += 1;
        // 项目创建成功后，显示提示并跳转到数据集页面引导用户创建数据集
        setTimeout(() => {
          Message.success({
            content: '项目创建成功，正在跳转到数据集页面...',
            duration: 2000
          });
          setTimeout(() => {
            router.push(`/project/${createdProjectId.value}/dataset`);
          }, 2000);
        }, 1000);
      }
    } finally {
      loading.value = false;
    }
  } else {
    currentStep.value += 1;
  }
};

const form = ref({
  // 步骤1：基本信息
  projectName: "",
  projectRemark: "",

  // 步骤2：数据集
  datasetName: "",
  dataType: "",
  timePeriod: "",
  validationRules: "",
  datasetRemark: ""
});

const rules = ref({
  // 步骤1：基本信息
  projectName: [{ required: true, message: "请输入项目名" }],
  projectRemark: [{ required: true, message: "请输入备注" }],

  // 步骤2：数据集
  datasetName: [{ required: true, message: "请输入数据集名" }],
  dataType: [{ required: true, message: "请选择数据类型" }],
  timePeriod: [{ required: true, message: "请输入时序周期", trigger: "blur" }],
  datasetRemark: [{ required: true, message: "请输入备注" }]
});

const formRef = ref();

const onLastStep = () => {
  if (currentStep.value == 1) return;
  currentStep.value -= 1;
};

// 组件卸载前清理状态
onBeforeUnmount(() => {
  console.log('新建项目组件即将卸载，清理状态');
  // 重置表单状态
  currentStep.value = 1;
  loading.value = false;
  createdProjectId.value = '';
});
</script>

<style lang="scss" scoped>
.container, .create-project-container {
  padding: 60px 0;
}
.margin-top {
  margin-top: 60px;
}
</style>
