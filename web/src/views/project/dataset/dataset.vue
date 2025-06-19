<template>
  <div class="dataset-layout-container">
    <a-spin :loading="loading">
      <template v-if="currentProject">
        <div class="cards-grid">
          <!-- 添加数据集卡片 -->
          <a-card class="add-card" hoverable @click="handleAddDataset">
            <div class="add-content">
              <icon-plus class="add-icon" />
              <div class="add-text">添加数据集</div>
            </div>
          </a-card>

          <!-- 数据集卡片列表 -->
          <a-card 
            v-for="dataset in currentProject.datasets" 
            :key="dataset.dataset_id"
            class="dataset-card" 
            hoverable
          >
            <template #title>
              <div class="card-header">数据集ID: {{ dataset.dataset_id }}</div>
            </template>
            
            <div class="card-content">
              <h3 class="card-title">{{ dataset.dataset_name }}</h3>
              <p class="card-subtitle">{{ dataset.remark || '无备注' }}</p>
              
              <div class="dataset-info">
                <div class="info-item">
                  <span class="info-label">数据类型:</span>
                  <span class="info-value">{{ dataset.data_type === 1 ? '静态数据' : '时序数据' }}</span>
                </div>
                <div class="info-item" v-if="dataset.data_type === 2 && dataset.time_series_period">
                  <span class="info-label">时序周期:</span>
                  <span class="info-value">{{ dataset.time_series_period }}</span>
                </div>
                <div class="info-item" v-if="dataset.validation_rule">
                  <span class="info-label">校验规则:</span>
                  <span class="info-value">{{ dataset.validation_rule }}</span>
                </div>
              </div>
            </div>

            <template #actions>
              <div class="card-actions">
                <a-button type="text" size="small" @click="viewDetail(dataset)">
                  <template #icon>
                    <icon-eye />
                  </template>
                  详情
                </a-button>
                <a-button type="text" size="small" @click="handleEdit(dataset)">
                  <template #icon>
                    <icon-edit />
                  </template>
                  编辑
                </a-button>
                <a-button type="text" size="small" status="danger" @click="handleDelete(dataset)">
                  <template #icon>
                    <icon-delete />
                  </template>
                  删除
                </a-button>
              </div>
            </template>
          </a-card>
        </div>
        
        <a-empty 
          v-if="!currentProject.datasets || !currentProject.datasets.length" 
          description="暂无数据集"
          class="empty-state"
        />
      </template>
      <template v-else>
        <a-result status="404" subtitle="未找到项目信息">
          <template #extra>
            <a-button type="primary" @click="router.push('/project/create-project')">
              创建新项目
            </a-button>
          </template>
        </a-result>
      </template>
    </a-spin>

    <!-- 添加数据集弹窗 -->
    <a-modal 
      v-model:visible="addDatasetModalVisible" 
      :title="isEditMode ? '编辑数据集' : '添加数据集'"
      width="600px"
      @ok="handleSubmitDataset"
      @cancel="handleCancelAddDataset"
    >
      <a-form 
        ref="datasetFormRef" 
        :model="datasetForm" 
        auto-label-width 
        size="medium"
        @submit="handleSubmitDataset"
      >
        <a-form-item 
          field="dataset_name" 
          label="数据集名称"
          :rules="[
            { required: true, message: '数据集名称不能为空' },
            { minLength: 2, message: '数据集名称至少2个字符' }
          ]"
          :validate-trigger="['change', 'input']"
        >
          <a-input 
            v-model="datasetForm.dataset_name" 
            placeholder="请输入数据集名称" 
            allow-clear 
          />
        </a-form-item>

        <a-form-item 
          field="data_type" 
          label="数据类型"
          :rules="isEditMode ? [] : [{ required: true, message: '请选择数据类型' }]"
        >
          <a-radio-group v-model="datasetForm.data_type" :disabled="isEditMode">
            <a-radio :value="1">静态数据</a-radio>
            <a-radio :value="2">时序数据</a-radio>
          </a-radio-group>
          <div v-if="isEditMode" style="font-size: 12px; color: #8c8c8c; margin-top: 4px;">
            编辑模式下数据类型不可修改
          </div>
        </a-form-item>

        <a-form-item 
          field="freqs" 
          label="时序周期"
          v-if="datasetForm.data_type === 2"
          :rules="isEditMode ? [] : [{ required: true, message: '时序数据需要设置时序周期' }]"
        >
          <a-input 
            v-model="datasetForm.freqs" 
            placeholder="请输入时序周期，如：1m+5m+1H+1D" 
            allow-clear 
            :disabled="isEditMode"
          />
          <template #extra>
            <div style="font-size: 12px; color: #8c8c8c;">
              <span v-if="!isEditMode">多个周期用+分割，例如：1m+5m+1H+1D（1分钟+5分钟+1小时+1天）</span>
              <span v-else>编辑模式下时序周期不可修改</span>
            </div>
          </template>
        </a-form-item>

        <a-form-item 
          field="check_rules" 
          label="数据校验规则"
        >
          <a-select v-model="datasetForm.check_rules" placeholder="请选择校验规则" allow-clear>
            <a-option value="required">必填校验</a-option>
            <a-option value="numeric">数值校验</a-option>
            <a-option value="email">邮箱校验</a-option>
            <a-option value="phone">手机号校验</a-option>
            <a-option value="custom">自定义规则</a-option>
          </a-select>
        </a-form-item>

        <a-form-item 
          field="comment" 
          label="备注说明"
        >
          <a-textarea 
            v-model="datasetForm.comment" 
            placeholder="请输入数据集备注说明" 
            :max-length="200"
            show-word-limit
            :auto-size="{ minRows: 3, maxRows: 6 }"
          />
        </a-form-item>
      </a-form>
    </a-modal>
  </div>
</template>

<script setup lang="ts">
import { ref, onMounted, computed, reactive } from 'vue';
import { useRoute, useRouter } from 'vue-router';
import { Message, Modal } from '@arco-design/web-vue';
import { listProjects, type Project } from '@/api/project';
import { createDataset, updateDataset, deleteDataset } from '@/api/dataset';
import { 
  IconPlus, 
  IconEye, 
  IconEdit, 
  IconDelete 
} from '@arco-design/web-vue/es/icon';

const route = useRoute();
const router = useRouter();
const loading = ref(false);

// 项目列表
const projects = ref<Project[]>([]);

// 弹窗状态
const addDatasetModalVisible = ref(false);
const datasetFormRef = ref();
const isEditMode = ref(false);
const currentEditDataset = ref<any>(null);

// 数据集表单数据
const datasetForm = reactive({
  dataset_name: '',
  data_type: 1,
  freqs: '', // 时序周期，对应后端的freqs字段
  check_rules: '', // 校验规则，对应后端的check_rules字段
  comment: '' // 备注，对应后端的comment字段
});

// 获取当前项目
const currentProject = computed(() => {
  const projectId = Number(route.params.projectId);
  return projects.value.find(p => Number(p.id) === projectId);
});

// 获取项目列表
const fetchProjects = async () => {
  loading.value = true;
  try {
    projects.value = await listProjects();
  } catch (error) {
    console.error('获取项目列表失败:', error);
    Message.error('获取项目列表失败');
  } finally {
    loading.value = false;
  }
};

const viewDetail = (dataset: any) => {
  // 跳转到数据集详情页（如有需要）
  // router.push({ path: `/project/${currentProject.value.name}/dataset/${dataset.dataset_id}`, query: { projectId: currentProject.value.id } });
  Message.info('详情功能开发中');
};

const handleAddDataset = () => {
  isEditMode.value = false;
  currentEditDataset.value = null;
  resetDatasetForm();
  addDatasetModalVisible.value = true;
};

const handleEdit = (dataset: any) => {
  isEditMode.value = true;
  currentEditDataset.value = dataset;
  
  // 预填充表单数据，需要将后端字段映射到表单字段
  Object.assign(datasetForm, {
    dataset_name: dataset.dataset_name || '',
    data_type: dataset.data_type || 1,
    freqs: dataset.time_series_period || '', // 后端字段time_series_period映射到表单freqs
    check_rules: dataset.validation_rule || '', // 后端字段validation_rule映射到表单check_rules
    comment: dataset.remark || '' // 后端字段remark映射到表单comment
  });
  
  addDatasetModalVisible.value = true;
};

const handleDelete = (dataset: any) => {
  Modal.warning({
    title: '确认删除',
    content: `确定要删除数据集"${dataset.dataset_name}"吗？删除后将无法恢复。`,
    hideCancel: false,
    onOk: async () => {
      try {
        const deleteParams = {
          proj_id: Number(route.params.projectId),
          dataset_id: dataset.dataset_id
        };
        
        await deleteDataset(deleteParams);
        Message.success('数据集删除成功！');
        
        // 重新获取项目列表以刷新数据
        await fetchProjects();
      } catch (error) {
        console.error('删除数据集失败:', error);
        Message.error('删除数据集失败');
      }
    }
  });
};

// 重置表单
const resetDatasetForm = () => {
  Object.assign(datasetForm, {
    dataset_name: '',
    data_type: 1,
    freqs: '',
    check_rules: '',
    comment: ''
  });
};

// 提交数据集表单
const handleSubmitDataset = async () => {
  try {
    const valid = await datasetFormRef.value?.validate();
    if (valid) {
      return; // 表单验证失败
    }
    
    if (isEditMode.value && currentEditDataset.value) {
      // 编辑模式 - 更新数据集
      const updateParams = {
        proj_id: Number(route.params.projectId),
        dataset_id: currentEditDataset.value.dataset_id,
        dataset_name: datasetForm.dataset_name,
        check_rules: datasetForm.check_rules,
        comment: datasetForm.comment
      };
      
      await updateDataset(updateParams);
      Message.success('数据集更新成功！');
    } else {
      // 添加模式 - 创建数据集
      const createParams = {
        proj_id: Number(route.params.projectId),
        dataset_name: datasetForm.dataset_name,
        data_type: datasetForm.data_type,
        freqs: datasetForm.freqs,
        check_rules: datasetForm.check_rules,
        comment: datasetForm.comment
      };
      
      await createDataset(createParams);
      Message.success('数据集创建成功！');
    }
    
    addDatasetModalVisible.value = false;
    resetDatasetForm();
    
    // 重新获取项目列表以刷新数据
    await fetchProjects();
  } catch (error) {
    console.error(isEditMode.value ? '更新数据集失败:' : '创建数据集失败:', error);
    Message.error(isEditMode.value ? '更新数据集失败' : '创建数据集失败');
  }
};

// 取消添加数据集
const handleCancelAddDataset = () => {
  addDatasetModalVisible.value = false;
  isEditMode.value = false;
  currentEditDataset.value = null;
  resetDatasetForm();
  datasetFormRef.value?.resetFields();
};

onMounted(() => {
  fetchProjects();
});
</script>

<style lang="scss" scoped>
.dataset-layout-container {
  padding: 24px;
  background-color: #f5f5f5;
  min-height: 100vh;
}

.cards-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(320px, 1fr));
  gap: 20px;
  max-width: 1400px;
  margin: 0 auto;
}

/* 添加数据集卡片样式 */
.add-card {
  border: 2px dashed #d9d9d9;
  background-color: #fafafa;
  transition: all 0.3s ease;
  cursor: pointer;
  height: 240px;
  box-sizing: border-box;
}

.add-card:hover {
  border-color: #1890ff;
  background-color: #f0f8ff;
}

.add-content {
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  padding: 40px 20px;
  text-align: center;
  height: 100%;
  box-sizing: border-box;
}

.add-icon {
  font-size: 48px;
  color: #8c8c8c;
  margin-bottom: 16px;
}

.add-text {
  font-size: 16px;
  color: #595959;
  font-weight: 500;
}

/* 数据集卡片样式 */
.dataset-card {
  background-color: white;
  border: 1px solid #e8e8e8;
  transition: all 0.3s ease;
  height: 240px;
  display: flex;
  flex-direction: column;
  padding: 20px 18px 14px 18px;
  box-sizing: border-box;
}

.dataset-card:hover {
  box-shadow: 0 4px 12px rgba(0, 0, 0, 0.1);
  border-color: #1890ff;
}

/* 确保卡片标题区域有足够空间 */
.dataset-card :deep(.arco-card-header) {
  padding: 6px 0 8px 0 !important;
  border-bottom: none !important;
}

.dataset-card :deep(.arco-card-body) {
  padding: 0 !important;
}

.card-header {
  font-size: 12px;
  color: #8c8c8c;
  font-weight: normal;
  margin-bottom: 8px;
  padding: 2px 0 6px 0;
  line-height: 1.5;
  border-bottom: 1px solid #d9d9d9;
}

.card-content {
  padding: 0 20px;
  flex: 1;
  display: flex;
  flex-direction: column;
  justify-content: center;
  overflow: hidden;
  min-height: 0;
  max-width: calc(100% - 40px);
  margin: 0 auto;
}

.card-title {
  font-size: 16px;
  font-weight: 600;
  color: #262626;
  margin: 0 0 6px 0;
  line-height: 1.3;
}

.card-subtitle {
  font-size: 13px;
  color: #8c8c8c;
  margin: 0 0 10px 0;
  line-height: 1.4;
  display: -webkit-box;
  -webkit-line-clamp: 2;
  -webkit-box-orient: vertical;
  overflow: hidden;
  text-overflow: ellipsis;
}

.dataset-info {
  flex: 1;
  overflow: hidden;
  display: flex;
  flex-direction: column;
  justify-content: flex-start;
  
  .info-item {
    display: flex;
    justify-content: space-between;
    align-items: center;
    margin-bottom: 4px;
    
    &:last-child {
      margin-bottom: 0;
    }
  }
  
  .info-label {
    font-size: 11px;
    color: #8c8c8c;
    flex-shrink: 0;
  }
  
  .info-value {
    font-size: 11px;
    color: #262626;
    text-align: right;
    font-weight: 500;
  }
}

.card-actions {
  display: flex;
  justify-content: space-between;
  align-items: center;
  padding: 8px 20px 0 20px;
  border-top: 1px solid #d9d9d9;
  margin-top: auto;
  flex-shrink: 0;
  max-width: calc(100% - 40px);
  margin-left: auto;
  margin-right: auto;
}

.card-actions .arco-btn {
  flex: 1;
  margin: 0 4px;
  display: flex;
  align-items: center;
  justify-content: center;
  font-size: 12px;
}

.card-actions .arco-btn:first-child {
  margin-left: 0;
}

.card-actions .arco-btn:last-child {
  margin-right: 0;
}

.empty-state {
  margin-top: 60px;
}

/* 响应式设计 */
@media (max-width: 768px) {
  .cards-grid {
    grid-template-columns: 1fr;
    gap: 16px;
  }
  
  .dataset-layout-container {
    padding: 16px;
  }
}

@media (max-width: 480px) {
  .card-actions {
    flex-direction: column;
    gap: 8px;
  }
  
  .card-actions .arco-btn {
    width: 100%;
    margin: 0;
  }
}
</style>
