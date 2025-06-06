<template>
  <div class="moox-page">
    <a-spin :loading="loading" style="display: block">
      <div class="moox-inner container">
        <a-row justify="center">
          <a-col :xs="22" :sm="18" :md="16" :lg="16" :xl="12" :xxl="12">
            <a-steps :current="currentStep" line-less>
              <a-step description="创建项目">基本信息</a-step>
              <a-step description="创建数据集">数据集</a-step>
              <a-step description="设置数据主键字段">主键配置</a-step>
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
                <a-form-item v-if="form.dataType === '2'" field="timePeriod" label="时序周期">
                  <a-input v-model="form.timePeriod" placeholder="请输入时序周期" />
                </a-form-item>
                <a-form-item field="validationRules" label="数据校验规则">
                  <a-textarea v-model="form.validationRules" placeholder="请输入JSON格式的数据校验规则（选填）" />
                </a-form-item>
                <a-form-item field="datasetRemark" label="备注">
                  <a-input v-model="form.datasetRemark" placeholder="请输入备注" />
                </a-form-item>
              </div>

              <!-- 步骤3：主键配置 -->
              <div v-if="currentStep == 3">
                <a-form-item field="fieldNameCn" label="字段中文名">
                  <a-input v-model="form.fieldNameCn" placeholder="请输入字段中文名" />
                </a-form-item>
                <a-form-item field="fieldNameEn" label="字段英文名">
                  <a-input v-model="form.fieldNameEn" placeholder="请输入字段英文名" />
                </a-form-item>
                <a-form-item field="fieldDescription" label="字段描述">
                  <a-input v-model="form.fieldDescription" placeholder="请输入字段描述" />
                </a-form-item>
                <a-form-item field="isRequired" label="是否必填">
                  <a-switch v-model="form.isRequired" />
                </a-form-item>
                <a-form-item field="isUnique" label="值是否唯一">
                  <a-switch v-model="form.isUnique" />
                </a-form-item>
                <a-form-item field="isMetadata" label="是否为元数据字段">
                  <a-switch v-model="form.isMetadata" />
                </a-form-item>
                <a-form-item field="fieldValidationRules" label="数据校验规则">
                  <a-textarea v-model="form.fieldValidationRules" placeholder="请输入JSON格式的数据校验规则（选填）" />
                </a-form-item>
                <a-form-item field="writeExample" label="写入示例">
                  <a-input v-model="form.writeExample" placeholder="请输入写入示例（选填）" />
                </a-form-item>
                <a-form-item field="fieldRemark" label="备注">
                  <a-input v-model="form.fieldRemark" placeholder="请输入备注" />
                </a-form-item>
                <a-form-item field="primaryFormat" label="字段主要类型">
                  <a-radio-group v-model="form.primaryFormat">
                    <a-row :gutter="16">
                      <a-col :span="12">
                        <a-radio value="1">字符串</a-radio>
                        <a-radio value="2">整型</a-radio>
                        <a-radio value="3">双精度浮点数</a-radio>
                        <a-radio value="4">时间类型</a-radio>
                      </a-col>
                      <a-col :span="12">
                        <a-radio value="5">整型向量</a-radio>
                        <a-radio value="6">Set类型</a-radio>
                        <a-radio value="7">Map类型k-v</a-radio>
                        <a-radio value="8">Map类型k-list</a-radio>
                      </a-col>
                    </a-row>
                  </a-radio-group>
                </a-form-item>
                <a-form-item field="secondaryFormat" label="字段次要类型">
                  <a-radio-group v-model="form.secondaryFormat">
                    <a-row :gutter="16">
                      <a-col :span="12">
                        <a-radio value="1">
                          <span>布尔类型</span>
                          <a-tooltip content="格式: true/false">
                            <icon-question-circle style="margin-left: 4px" />
                          </a-tooltip>
                        </a-radio>
                        <a-radio value="2">
                          <span>日期</span>
                          <a-tooltip content="格式: 2021-02-03">
                            <icon-question-circle style="margin-left: 4px" />
                          </a-tooltip>
                        </a-radio>
                        <a-radio value="3">
                          <span>日期范围</span>
                          <a-tooltip content="格式: 2021-02-03 ~ 2022-03-02">
                            <icon-question-circle style="margin-left: 4px" />
                          </a-tooltip>
                        </a-radio>
                        <a-radio value="4">
                          <span>日期时间</span>
                          <a-tooltip content="格式: 2021-02-03 08:00:00">
                            <icon-question-circle style="margin-left: 4px" />
                          </a-tooltip>
                        </a-radio>
                        <a-radio value="5">
                          <span>日期时间范围</span>
                          <a-tooltip content="格式: 2021-02-03 08:00:00 ~ 2022-03-02 09:00:00">
                            <icon-question-circle style="margin-left: 4px" />
                          </a-tooltip>
                        </a-radio>
                        <a-radio value="6">
                          <span>秒级时间戳</span>
                          <a-tooltip content="格式: 1661411887">
                            <icon-question-circle style="margin-left: 4px" />
                          </a-tooltip>
                        </a-radio>
                      </a-col>
                      <a-col :span="12">
                        <a-radio value="7">
                          <span>ISO8601日期</span>
                          <a-tooltip content="格式: 2025-04-12T20:36:00+08:00">
                            <icon-question-circle style="margin-left: 4px" />
                          </a-tooltip>
                        </a-radio>
                        <a-radio value="8">
                          <span>链接</span>
                          <a-tooltip content="格式: http://example.com">
                            <icon-question-circle style="margin-left: 4px" />
                          </a-tooltip>
                        </a-radio>
                        <a-radio value="9">
                          <span>JSON值</span>
                          <a-tooltip content="格式: {&quot;key&quot;: &quot;value&quot;}">
                            <icon-question-circle style="margin-left: 4px" />
                          </a-tooltip>
                        </a-radio>
                        <a-radio value="10">
                          <span>选项值ID</span>
                          <a-tooltip content="格式: 1+2+3">
                            <icon-question-circle style="margin-left: 4px" />
                          </a-tooltip>
                        </a-radio>
                        <a-radio value="11">
                          <span>选项值翻译</span>
                          <a-tooltip content="格式: 选项1+选项2+选项3">
                            <icon-question-circle style="margin-left: 4px" />
                          </a-tooltip>
                        </a-radio>
                      </a-col>
                    </a-row>
                  </a-radio-group>
                </a-form-item>
              </div>

              <!-- 步骤4：完成创建 -->
              <div v-if="currentStep == 4">
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

              <a-form-item v-if="currentStep != 4">
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

<script setup lang="ts">
import { Message } from '@arco-design/web-vue';
import { api, AUTH_INFO } from '@/api/config';

const loading = ref(false);
const currentStep = ref(1);
const createdProjectId = ref('');

interface CreateProjectResponseData {
  code: number;
  message: string;
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
      },
      fields: [{
        field_name: form.value.fieldNameCn,
        interface_name: form.value.fieldNameEn,
        desc: form.value.fieldDescription,
        is_required: form.value.isRequired,
        is_unique: form.value.isUnique,
        is_metadata: form.value.isMetadata,
        validation_rule: form.value.fieldValidationRules,
        write_example: form.value.writeExample,
        remark: form.value.fieldRemark,
        field_primary_format: parseInt(form.value.primaryFormat),
        field_secondary_format: parseInt(form.value.secondaryFormat)
      }]
    };

    console.log('提交项目数据:', projectData);
    
    const response = await api.post('/trpc.storage.metadata.MetaAdmin/CreateProject', projectData);
    console.log('API响应:', response);
    
    // 现在 response.data 直接是精简后的协议数据
    const data = response.data as CreateProjectResponseData;
    console.log('协议数据:', data);
    
    // 检查协议级别的错误（非0表示错误）
    if (data.code !== 0) {
      throw new Error(data.message || '项目创建失败');
    }
    
    // 检查是否有项目ID
    if (!data.proj_id) {
      throw new Error('未返回项目ID');
    }
    
    createdProjectId.value = data.proj_id.toString();
    Message.success({
      content: data.message || '项目创建成功',
      duration: 3000
    });
    return true;
  } catch (error: any) {
    console.error('API Error:', error);
    
    let errorMessage = '项目创建失败';
    
    if (!error) {
      errorMessage = '未知错误：API调用返回undefined';
    } else if (error.response?.data?.message) {
      // HTTP错误响应，使用协议中的消息
      errorMessage = error.response.data.message;
    } else if (error.message) {
      errorMessage = error.message;
    } else if (typeof error === 'string') {
      errorMessage = error;
    }
    
    Message.error({
      content: errorMessage,
      duration: 3000
    });
    return false;
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
  
  if (currentStep.value == 4) return;
  
  // 验证时序周期
  if (currentStep.value == 2 && form.value.dataType === "2" && !form.value.timePeriod) {
    Message.error({
      content: '请输入时序周期',
      duration: 3000
    });
    return;
  }
  
  if (currentStep.value == 3) {
    loading.value = true;
    try {
      const success = await submitProjectData();
      if (success) {
        currentStep.value += 1;
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
  datasetRemark: "",
  
  // 步骤3：主键配置
  fieldNameCn: "",
  fieldNameEn: "",
  fieldDescription: "",
  isRequired: false,
  isUnique: false,
  isMetadata: false,
  fieldValidationRules: "",
  writeExample: "",
  fieldRemark: "",
  primaryFormat: "",
  secondaryFormat: ""
});

const rules = ref({
  // 步骤1：基本信息
  projectName: [{ required: true, message: "请输入项目名" }],
  projectRemark: [{ required: true, message: "请输入备注" }],
  
  // 步骤2：数据集
  datasetName: [{ required: true, message: "请输入数据集名" }],
  dataType: [{ required: true, message: "请选择数据类型" }],
  timePeriod: [{ required: true, message: "请输入时序周期", trigger: "blur" }],
  datasetRemark: [{ required: true, message: "请输入备注" }],
  
  // 步骤3：主键配置
  fieldNameCn: [{ required: true, message: "请输入字段中文名" }],
  fieldNameEn: [{ required: true, message: "请输入字段英文名" }],
  fieldDescription: [{ required: true, message: "请输入字段描述" }],
  fieldRemark: [{ required: true, message: "请输入备注" }],
  primaryFormat: [{ required: true, message: "请选择字段主要类型" }],
  secondaryFormat: [{ required: true, message: "请选择字段次要类型" }]
});

const formRef = ref();

const onLastStep = () => {
  if (currentStep.value == 1) return;
  currentStep.value -= 1;
};
</script>

<style lang="scss" scoped>
.container {
  padding: 60px 0;
}
.margin-top {
  margin-top: 60px;
}
</style>