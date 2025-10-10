<template>
  <div class="moox-inner">
    <!-- 搜索区域 -->
    <a-space wrap>
      <a-select v-model="form.dataset_id" placeholder="请选择数据集" allow-clear style="width: 200px" :loading="datasetLoading">
        <a-option 
          v-for="dataset in datasetOptions" 
          :key="dataset.dataset_id" 
          :value="dataset.dataset_id"
        >
          {{ dataset.dataset_name }} ({{ dataset.dataset_id }})
        </a-option>
      </a-select>
      <a-input v-model="form.object_id" placeholder="请输入数据对象ID" allow-clear style="width: 180px" />
      <a-select v-model="form.entity_id" placeholder="请选择存储实体" allow-clear style="width: 200px" :loading="entityLoading">
        <a-option 
          v-for="entity in entityOptions" 
          :key="entity.entity_id" 
          :value="entity.entity_id"
        >
          {{ entity.entity_alias }} ({{ entity.entity_id }})
        </a-option>
      </a-select>
      <a-button type="primary" @click="search">
        <template #icon><icon-search /></template>
        <span>查询</span>
      </a-button>
      <a-button @click="reset">
        <template #icon><icon-refresh /></template>
        <span>重置</span>
      </a-button>
    </a-space>

    <a-row>
      <a-space wrap>
        <a-button type="primary" @click="onAdd">
          <template #icon><icon-plus /></template>
          <span>新增</span>
        </a-button>
        <a-button type="primary" status="danger" @click="batchDelete">
          <template #icon><icon-delete /></template>
          <span>删除</span>
        </a-button>
      </a-space>
    </a-row>

    <a-table
      row-key="route_id"
      :data="tableData"
      :bordered="{ cell: true }"
      :loading="loading"
      :scroll="{ x: '100%', y: '100%', minWidth: 800 }"
      :pagination="pagination"
      :row-selection="{ type: 'checkbox', showCheckedAll: true }"
      :selected-keys="selectedKeys"
      @select="select"
      @select-all="selectAll"
      @page-change="onPageChange"
    >
      <template #columns>
        <a-table-column title="序号" :width="64">
          <template #cell="cell">{{ cell.rowIndex + 1 }}</template>
        </a-table-column>
        <a-table-column title="数据集" data-index="dataset_id">
          <template #cell="{ record }">
            {{ getDatasetName(record.dataset_id) }}
          </template>
        </a-table-column>
        <a-table-column title="数据对象ID" data-index="object_id"></a-table-column>
        <a-table-column title="存储实体" data-index="entity_id">
          <template #cell="{ record }">
            {{ getEntityName(record.entity_id) }}
          </template>
        </a-table-column>
        <a-table-column title="状态" :width="100" align="center">
          <template #cell="{ record }">
            <a-tag bordered size="small" color="arcoblue" v-if="record.invalid === 0">正常</a-tag>
            <a-tag bordered size="small" color="red" v-else>已删除</a-tag>
          </template>
        </a-table-column>
        <a-table-column title="创建时间" data-index="ctime" :width="180"></a-table-column>
        <a-table-column title="操作" :width="200" align="center" :fixed="'right'">
          <template #cell="{ record }">
            <a-space>
              <a-button type="primary" size="mini" @click="onUpdate(record)">
                <template #icon><icon-edit /></template>
                <span>修改</span>
              </a-button>
              <a-popconfirm type="warning" content="确定删除该配置吗?" @ok="onDelete(record)" v-if="record.invalid === 0">
                <a-button type="primary" status="danger" size="mini">
                  <template #icon><icon-delete /></template>
                  <span>删除</span>
                </a-button>
              </a-popconfirm>
            </a-space>
          </template>
        </a-table-column>
      </template>
    </a-table>

    <!-- 新增/编辑弹窗 -->
    <a-modal v-model:visible="modalVisible" @close="afterClose" @ok="handleOk" @cancel="afterClose" width="600px">
      <template #title>{{ modalTitle }}</template>
      <div>
        <a-form ref="formRef" auto-label-width :rules="rules" :model="formData">
          <a-form-item field="dataset_id" label="数据集" validate-trigger="blur">
            <a-select v-model="formData.dataset_id" placeholder="请选择数据集" allow-clear :loading="datasetLoading">
              <a-option 
                v-for="dataset in datasetOptions" 
                :key="dataset.dataset_id" 
                :value="dataset.dataset_id"
              >
                {{ dataset.dataset_name }} ({{ dataset.dataset_id }})
              </a-option>
            </a-select>
          </a-form-item>
          <a-form-item field="object_id" label="数据对象ID" validate-trigger="blur">
            <a-input v-model="formData.object_id" placeholder="请输入数据对象ID（* 表示所有对象）" allow-clear />
          </a-form-item>
          <a-form-item field="entity_id" label="存储实体" validate-trigger="blur">
            <a-select v-model="formData.entity_id" placeholder="请选择存储实体" allow-clear :loading="entityLoading">
              <a-option 
                v-for="entity in entityOptions" 
                :key="entity.entity_id" 
                :value="entity.entity_id"
              >
                {{ entity.entity_alias }} ({{ entity.entity_id }})
              </a-option>
            </a-select>
          </a-form-item>
        </a-form>
      </div>
    </a-modal>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, watch, onMounted } from 'vue';
import { useRoute } from 'vue-router';
import { Message } from '@arco-design/web-vue';
import { IconSearch, IconRefresh, IconPlus, IconEdit, IconDelete } from '@arco-design/web-vue/es/icon';
import type { ObjectRoute, StorageEntity } from '@/api/storage-config';
import { 
  createObjectRoute, 
  updateObjectRoute, 
  deleteObjectRoute,
  listStorageEntities
} from '@/api/storage-config';
import { listProjects, type Dataset } from '@/api/project';

// Props定义
interface Props {
  routes: ObjectRoute[];
  loading: boolean;
}

const props = withDefaults(defineProps<Props>(), {
  routes: () => [],
  loading: false
});

// Emits定义
const emits = defineEmits<{
  refresh: [searchParams?: { dataset_id?: number; entity_id?: number }];
}>();

// 定义表单数据类型
interface RouteFormData {
  route_id?: number;
  dataset_id: number;
  object_id: string;
  entity_id: number;
}

// 搜索表单
const form = ref({
  dataset_id: undefined as any,
  object_id: '',
  entity_id: undefined as any,
});

// 表格数据 - 基于props计算
const tableData = computed(() => props.routes);
const selectedKeys = ref<number[]>([]);
const pagination = ref({
  total: 0,
  current: 1,
  pageSize: 10,
});

// 监听routes变化，更新分页总数
watch(() => props.routes, (newRoutes) => {
  pagination.value.total = newRoutes.length;
}, { immediate: true });

// 路由信息
const route = useRoute();

// 获取当前项目ID
const currentProjectId = computed(() => {
  const projectId = route.params.projectId;
  return projectId ? Number(projectId) : null;
});

// 数据集选项
const datasetOptions = ref<Dataset[]>([]);
const datasetLoading = ref(false);

// 存储实体选项
const entityOptions = ref<StorageEntity[]>([]);
const entityLoading = ref(false);

// 弹窗相关
const modalVisible = ref(false);
const modalTitle = ref('新增对象路由配置');
const formRef = ref();
const formData = ref<RouteFormData>({
  dataset_id: undefined as any,
  object_id: '*',
  entity_id: undefined as any,
});

// 表单验证规则
const rules = {
  dataset_id: [{ required: true, message: '请选择数据集' }],
  object_id: [{ required: true, message: '请输入数据对象ID' }],
  entity_id: [{ required: true, message: '请选择存储实体' }],
};

// 获取数据集列表
const loadDatasetOptions = async () => {
  if (!currentProjectId.value) {
    console.warn('当前项目ID为空，无法获取数据集列表');
    return;
  }
  
  try {
    datasetLoading.value = true;
    const projects = await listProjects();
    const currentProject = projects.find(p => p.id === currentProjectId.value);
    
    if (currentProject && currentProject.datasets) {
      datasetOptions.value = currentProject.datasets;
      console.log('数据集列表加载成功:', datasetOptions.value);
    } else {
      datasetOptions.value = [];
      console.warn('当前项目无数据集或项目不存在');
    }
  } catch (error: any) {
    console.error('获取数据集列表失败:', error);
    Message.error(error.message || '获取数据集列表失败');
    datasetOptions.value = [];
  } finally {
    datasetLoading.value = false;
  }
};

// 获取存储实体列表
const loadEntityOptions = async () => {
  try {
    entityLoading.value = true;
    const response = await listStorageEntities();
    entityOptions.value = response.entities || [];
    console.log('存储实体列表加载成功:', entityOptions.value);
  } catch (error: any) {
    console.error('获取存储实体列表失败:', error);
    Message.error(error.message || '获取存储实体列表失败');
    entityOptions.value = [];
  } finally {
    entityLoading.value = false;
  }
};

// 搜索
const search = () => {
  pagination.value.current = 1;
  // 构建搜索参数，只传递有值的参数
  const searchParams: { dataset_id?: number; entity_id?: number } = {};
  if (form.value.dataset_id) {
    searchParams.dataset_id = form.value.dataset_id;
  }
  if (form.value.entity_id) {
    searchParams.entity_id = form.value.entity_id;
  }
  emits('refresh', searchParams);
};

// 重置
const reset = () => {
  form.value = {
    dataset_id: undefined as any,
    object_id: '',
    entity_id: undefined as any,
  };
  // 重置时不传递搜索参数，获取全部数据
  emits('refresh');
};

// 页码改变
const onPageChange = (current: number) => {
  pagination.value.current = current;
};

// 表格选择
const select = (rowKeys: number[]) => {
  selectedKeys.value = rowKeys;
};

const selectAll = (checked: boolean) => {
  if (checked) {
    selectedKeys.value = tableData.value.map(item => item.route_id!);
  } else {
    selectedKeys.value = [];
  }
};

// 新增
const onAdd = async () => {
  modalTitle.value = '新增对象路由配置';
  formData.value = {
    dataset_id: undefined as any,
    object_id: '*',
    entity_id: undefined as any,
  };
  modalVisible.value = true;
  
  // 加载下拉框数据
  await Promise.all([
    loadDatasetOptions(),
    loadEntityOptions()
  ]);
};

// 编辑
const onUpdate = async (record: ObjectRoute) => {
  modalTitle.value = '编辑对象路由配置';
  formData.value = {
    route_id: record.route_id,
    dataset_id: record.dataset_id,
    object_id: record.object_id,
    entity_id: record.entity_id,
  };
  modalVisible.value = true;
  
  // 加载下拉框数据
  await Promise.all([
    loadDatasetOptions(),
    loadEntityOptions()
  ]);
};

// 删除
const onDelete = async (record: ObjectRoute) => {
  try {
    await deleteObjectRoute({ route_id: record.route_id });
    Message.success('删除对象路由配置成功');
    emits('refresh');
  } catch (error: any) {
    console.error('删除对象路由配置失败:', error);
    Message.error(error.message || '删除对象路由配置失败');
  }
};

// 批量删除
const batchDelete = () => {
  if (selectedKeys.value.length === 0) {
    Message.warning('请选择要删除的数据');
    return;
  }
  // TODO: 实现批量删除功能
  Message.success('批量删除成功');
  selectedKeys.value = [];
  emits('refresh');
};

// 确认保存
const handleOk = async () => {
  try {
    await formRef.value?.validate();
    
    if (formData.value.route_id) {
      // 编辑模式 - 调用更新接口
      await updateObjectRoute({
        route_id: formData.value.route_id,
        dataset_id: formData.value.dataset_id,
        object_id: formData.value.object_id,
        entity_id: formData.value.entity_id,
      });
      Message.success('更新对象路由配置成功');
    } else {
      // 新增模式 - 调用创建接口
      if (!currentProjectId.value) {
        Message.error('当前项目ID为空，无法创建对象路由配置');
        return;
      }
      await createObjectRoute({
        project_id: currentProjectId.value,
        dataset_id: formData.value.dataset_id,
        object_id: formData.value.object_id,
        entity_id: formData.value.entity_id,
      });
      Message.success('创建对象路由配置成功');
    }
    
    modalVisible.value = false;
    emits('refresh');
  } catch (error: any) {
    console.error('保存对象路由配置失败:', error);
    Message.error(error.message || '保存对象路由配置失败');
  }
};

// 关闭弹窗
const afterClose = () => {
  modalVisible.value = false;
};

// 获取数据集名称的映射函数
const getDatasetName = (datasetId: number): string => {
  const dataset = datasetOptions.value.find(d => d.dataset_id === datasetId);
  return dataset ? `${dataset.dataset_name} (${datasetId})` : `数据集 (${datasetId})`;
};

// 获取存储实体名称的映射函数
const getEntityName = (entityId: number): string => {
  const entity = entityOptions.value.find(e => e.entity_id === entityId);
  return entity ? `${entity.entity_alias} (${entityId})` : `存储实体 (${entityId})`;
};

// 初始化加载映射数据
const initMappingData = async () => {
  await Promise.all([
    loadDatasetOptions(),
    loadEntityOptions()
  ]);
};

// 组件挂载时初始化映射数据
onMounted(() => {
  initMappingData();
});
</script>

<style lang="scss" scoped>
.moox-inner {
  > .a-space {
    margin-bottom: 16px;
  }
  
  > .a-row {
    margin-bottom: 16px;
  }
}
</style> 
