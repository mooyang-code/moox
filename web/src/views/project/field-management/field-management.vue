<template>
  <div class="moox-page">
    <a-spin :loading="loading">
      <template v-if="currentProject">
        <div class="moox-inner">
          <a-space wrap>
            <a-input v-model="form.fieldName" placeholder="请输入字段中文名" allow-clear />
            <a-input v-model="form.fieldNameEn" placeholder="请输入字段英文名" allow-clear />
            <a-select placeholder="字段主要类型" v-model="form.primaryFormat" style="width: 150px" allow-clear>
              <a-option v-for="item in primaryTypes" :key="item.value" :value="item.value">{{ item.name }}</a-option>
            </a-select>
            <a-select placeholder="是否必填" v-model="form.required" style="width: 120px" allow-clear>
              <a-option v-for="item in requiredOptions" :key="item.value" :value="item.value">{{ item.name }}</a-option>
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
                <span>批量删除</span>
              </a-button>
            </a-space>
          </a-row>

          <a-table
            row-key="id"
            :data="fieldList"
            :bordered="{ cell: true }"
            :loading="loading"
            :scroll="{ x: '100%', y: '100%', minWidth: 1200 }"
            :pagination="pagination"
            :row-selection="{ type: 'checkbox', showCheckedAll: true }"
            :selected-keys="selectedKeys"
            @select="select"
            @select-all="selectAll"
            @page-change="onPageChange"
          >
            <template #columns>
              <a-table-column title="序号" :width="64">
                <template #cell="cell">{{ (pagination.current - 1) * pagination.pageSize + cell.rowIndex + 1 }}</template>
              </a-table-column>
              <a-table-column title="字段中文名" data-index="fieldName" :width="120"></a-table-column>
              <a-table-column title="字段英文名" data-index="fieldNameEn" :width="120"></a-table-column>
              <a-table-column title="字段主要类型" data-index="primaryFormatText" :width="120"></a-table-column>
              <a-table-column title="字段次要类型" data-index="secondaryFormatText" :width="120"></a-table-column>
              <a-table-column title="是否必填" :width="100" align="center">
                <template #cell="{ record }">
                  <a-tag bordered size="small" color="red" v-if="record.isRequired">必填</a-tag>
                  <a-tag bordered size="small" color="arcoblue" v-else>非必填</a-tag>
                </template>
              </a-table-column>
              <a-table-column title="值是否唯一" :width="100" align="center">
                <template #cell="{ record }">
                  <a-tag bordered size="small" color="orange" v-if="record.isUnique">唯一</a-tag>
                  <a-tag bordered size="small" color="gray" v-else>非唯一</a-tag>
                </template>
              </a-table-column>
              <a-table-column title="是否为元数据" :width="110" align="center">
                <template #cell="{ record }">
                  <a-tag bordered size="small" color="purple" v-if="record.isMetadata">元数据</a-tag>
                  <a-tag bordered size="small" color="gray" v-else>普通字段</a-tag>
                </template>
              </a-table-column>
              <a-table-column title="字段描述" data-index="fieldDescription" :ellipsis="true" :tooltip="true" :width="150"></a-table-column>
              <a-table-column title="写入示例" data-index="writeExample" :ellipsis="true" :tooltip="true" :width="120"></a-table-column>
              <a-table-column title="创建时间" data-index="createTime" :width="180"></a-table-column>
              <a-table-column title="操作" :width="200" align="center" :fixed="'right'">
                <template #cell="{ record }">
                  <a-space>
                    <a-button type="primary" size="mini" @click="onUpdate(record)">
                      <template #icon><icon-edit /></template>
                      <span>修改</span>
                    </a-button>
                    <a-popconfirm type="warning" content="确定删除该字段吗?">
                      <a-button type="primary" status="danger" size="mini" @click="handleDelete(record)">
                        <template #icon><icon-delete /></template>
                        <span>删除</span>
                      </a-button>
                    </a-popconfirm>
                  </a-space>
                </template>
              </a-table-column>
            </template>
          </a-table>
        </div>
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

    <a-modal v-model:visible="open" @close="afterClose" @ok="handleOk" @cancel="afterClose" width="600px">
      <template #title> {{ title }} </template>
      <div>
        <a-form ref="formRef" auto-label-width :rules="rules" :model="addForm">
          <a-form-item field="fieldName" label="字段中文名" validate-trigger="blur">
            <a-input v-model="addForm.fieldName" placeholder="请输入字段中文名" allow-clear />
          </a-form-item>
          <a-form-item field="fieldNameEn" label="字段英文名" validate-trigger="blur">
            <a-input v-model="addForm.fieldNameEn" placeholder="请输入字段英文名" allow-clear />
          </a-form-item>
          <a-form-item field="fieldDescription" label="字段描述" validate-trigger="blur">
            <a-textarea v-model="addForm.fieldDescription" placeholder="请输入字段描述" allow-clear />
          </a-form-item>
          <a-form-item field="primaryFormat" label="字段主要类型" validate-trigger="blur">
            <a-select v-model="addForm.primaryFormat" placeholder="请选择字段主要类型">
              <a-option v-for="item in primaryTypes" :key="item.value" :value="item.value">{{ item.name }}</a-option>
            </a-select>
          </a-form-item>
          <a-form-item field="secondaryFormat" label="字段次要类型" validate-trigger="blur">
            <a-select v-model="addForm.secondaryFormat" placeholder="请选择字段次要类型">
              <a-option v-for="item in secondaryTypes" :key="item.value" :value="item.value">{{ item.name }}</a-option>
            </a-select>
          </a-form-item>
          <a-form-item field="isRequired" label="是否必填" validate-trigger="blur">
            <a-switch type="round" :checked-value="true" :unchecked-value="false" v-model="addForm.isRequired">
              <template #checked> 必填 </template>
              <template #unchecked> 非必填 </template>
            </a-switch>
          </a-form-item>
          <a-form-item field="isUnique" label="值是否唯一" validate-trigger="blur">
            <a-switch type="round" :checked-value="true" :unchecked-value="false" v-model="addForm.isUnique">
              <template #checked> 唯一 </template>
              <template #unchecked> 非唯一 </template>
            </a-switch>
          </a-form-item>
          <a-form-item field="isMetadata" label="是否为元数据字段" validate-trigger="blur">
            <a-switch type="round" :checked-value="true" :unchecked-value="false" v-model="addForm.isMetadata">
              <template #checked> 元数据 </template>
              <template #unchecked> 普通字段 </template>
            </a-switch>
          </a-form-item>
          <a-form-item field="fieldValidationRules" label="数据校验规则" validate-trigger="blur">
            <a-textarea v-model="addForm.fieldValidationRules" placeholder="请输入JSON格式的数据校验规则（选填）" allow-clear />
          </a-form-item>
          <a-form-item field="writeExample" label="写入示例" validate-trigger="blur">
            <a-input v-model="addForm.writeExample" placeholder="请输入写入示例（选填）" allow-clear />
          </a-form-item>
          <a-form-item field="remark" label="备注" validate-trigger="blur">
            <a-textarea v-model="addForm.remark" placeholder="请输入备注" allow-clear />
          </a-form-item>
        </a-form>
      </div>
    </a-modal>
  </div>
</template>

<script setup lang="ts">
import { ref, onMounted, computed } from 'vue';
import { useRoute, useRouter } from 'vue-router';
import { Message } from '@arco-design/web-vue';
import { IconPlus, IconEdit, IconDelete, IconSearch, IconRefresh } from '@arco-design/web-vue/es/icon';
import { listProjects, type Project } from '@/api/project';
import { FIELD_SECONDARY_FORMAT_OPTIONS, getFieldSecondaryFormatName } from '@/typings/field-format';
import { 
  searchFields, 
  createField, 
  updateField, 
  deleteField,
  type FieldDetailInfo,
  type SearchFieldReq,
  type CreateFieldReq,
  type UpdateFieldReq,
  type AuthInfo
} from '@/api/field';

const route = useRoute();
const router = useRouter();

// 获取认证信息
const getAuthInfo = (): AuthInfo => {
  return {
    app_id: 'moox_frontend',
    app_key: '2521e0d21b6be0347b72bca93904a0dd'
  };
};

// 项目列表
const projects = ref<Project[]>([]);

// 获取当前项目
const currentProject = computed(() => {
  const projectId = Number(route.params.projectId);
  return projects.value.find(p => Number(p.id) === projectId);
});

// 获取项目列表
const fetchProjects = async () => {
  try {
    projects.value = await listProjects();
  } catch (error) {
    console.error('获取项目列表失败:', error);
    Message.error('获取项目列表失败');
  }
};

// 字段类型定义，映射后台API返回的字段
interface FieldRecord {
  id: number;
  fieldName: string;
  fieldNameEn: string;
  fieldDescription: string;
  primaryFormat: string;
  primaryFormatText: string;
  secondaryFormat: string;
  secondaryFormatText: string;
  isRequired: boolean;
  isUnique: boolean;
  isMetadata: boolean;
  fieldValidationRules: string;
  writeExample: string;
  remark: string;
  createTime: string;
}

// 搜索表单
const form = ref({
  fieldName: "",
  fieldNameEn: "",
  primaryFormat: "",
  required: null as boolean | null
});

// 主要类型选项
const primaryTypes = ref([
  { value: "1", name: "字符串" },
  { value: "2", name: "整型" },
  { value: "3", name: "双精度浮点数" },
  { value: "4", name: "时间类型" },
  { value: "5", name: "整型向量" },
  { value: "6", name: "Set类型" },
  { value: "7", name: "Map类型k-v" },
  { value: "8", name: "Map类型k-list" }
]);

// 次要类型选项
const secondaryTypes = ref(FIELD_SECONDARY_FORMAT_OPTIONS);

// 是否必填选项
const requiredOptions = ref([
  { value: true, name: "必填" },
  { value: false, name: "非必填" }
]);

// 获取类型文本
const getTypeText = (value: string, types: any[]) => {
  const type = types.find(item => item.value === value);
  return type ? type.name : '';
};

// 搜索和重置
const search = () => {
  pagination.value.current = 1;
  getFieldList();
};

const reset = () => {
  form.value = {
    fieldName: "",
    fieldNameEn: "",
    primaryFormat: "",
    required: null
  };
  getFieldList();
};

// 表格相关
const loading = ref(false);
const pagination = ref({
  current: 1,
  pageSize: 10,
  total: 0,
  showPageSize: true
});
const selectedKeys = ref<number[]>([]);
const fieldList = ref<FieldRecord[]>([]);

const select = (list: number[]) => {
  selectedKeys.value = list;
};

const selectAll = (state: boolean) => {
  selectedKeys.value = state ? fieldList.value.map(el => el.id) : [];
};

// 页码改变
const onPageChange = (current: number) => {
  pagination.value.current = current;
  getFieldList();
};

// 将后台API字段转换为前端显示字段
const convertApiFieldToRecord = (apiField: FieldDetailInfo): FieldRecord => {
  return {
    id: apiField.field_id,
    fieldName: apiField.field_name,
    fieldNameEn: apiField.interface_name,
    fieldDescription: apiField.desc,
    primaryFormat: String(apiField.field_format_type.field_primary_format),
    primaryFormatText: getTypeText(String(apiField.field_format_type.field_primary_format), primaryTypes.value),
    secondaryFormat: String(apiField.field_format_type.field_secondary_format),
    secondaryFormatText: getFieldSecondaryFormatName(apiField.field_format_type.field_secondary_format),
    isRequired: apiField.is_required,
    isUnique: apiField.is_unique,
    isMetadata: !!apiField.parent_field_id, // 假设有父字段ID的为元数据字段
    fieldValidationRules: apiField.validation_rule ? JSON.stringify(apiField.validation_rule) : '',
    writeExample: apiField.write_example || '',
    remark: apiField.remark || '',
    createTime: apiField.ctime || ''
  };
};

// 获取字段列表
const getFieldList = async () => {
  if (!currentProject.value) {
    return;
  }
  
  loading.value = true;
  try {
    const searchParams: SearchFieldReq = {
      auth_info: getAuthInfo(),
      proj_id: Number(currentProject.value.id),
      field_name: form.value.fieldName || undefined,
      interface_name: form.value.fieldNameEn || undefined,
      page_info: {
        page_idx: pagination.value.current,
        size: pagination.value.pageSize
      }
    };

    // 添加主要类型筛选
    if (form.value.primaryFormat) {
      // 这里可能需要根据具体需求调整，proto中没有直接的primaryFormat搜索条件
    }

    const response = await searchFields(searchParams);
    
    // 转换API数据为前端展示格式
    fieldList.value = response.field_detail_infos.map(convertApiFieldToRecord);
    
    // 根据前端筛选条件再次过滤（如果后台API不支持某些筛选条件）
    if (form.value.primaryFormat) {
      fieldList.value = fieldList.value.filter(item => 
        item.primaryFormat === form.value.primaryFormat
      );
    }
    if (form.value.required !== null) {
      fieldList.value = fieldList.value.filter(item => 
        item.isRequired === form.value.required
      );
    }
    
    // 更新分页信息
    pagination.value.current = response.cur_page;
    pagination.value.total = response.total_num;
    
  } catch (error) {
    console.error('获取字段列表失败:', error);
    Message.error('获取字段列表失败');
  } finally {
    loading.value = false;
  }
};

// 弹窗相关
const open = ref<boolean>(false);
const title = ref<string>("");
const addForm = ref({
  fieldName: "",
  fieldNameEn: "",
  fieldDescription: "",
  primaryFormat: "",
  secondaryFormat: "",
  isRequired: false,
  isUnique: false,
  isMetadata: false,
  fieldValidationRules: "",
  writeExample: "",
  remark: ""
});

const rules = {
  fieldName: [{ required: true, message: "请输入字段中文名" }],
  fieldNameEn: [{ required: true, message: "请输入字段英文名" }],
  fieldDescription: [{ required: true, message: "请输入字段描述" }],
  primaryFormat: [{ required: true, message: "请选择字段主要类型" }],
  secondaryFormat: [{ required: true, message: "请选择字段次要类型" }]
};

const formRef = ref();

// 新增字段
const onAdd = () => {
  open.value = true;
  title.value = "新增字段";
};

// 当前正在编辑的字段ID
const currentEditingFieldId = ref<number | null>(null);

// 修改字段
const onUpdate = (record: FieldRecord) => {
  title.value = "修改字段";
  currentEditingFieldId.value = record.id;
  addForm.value = {
    fieldName: record.fieldName,
    fieldNameEn: record.fieldNameEn,
    fieldDescription: record.fieldDescription,
    primaryFormat: record.primaryFormat,
    secondaryFormat: record.secondaryFormat,
    isRequired: record.isRequired,
    isUnique: record.isUnique,
    isMetadata: record.isMetadata,
    fieldValidationRules: record.fieldValidationRules,
    writeExample: record.writeExample,
    remark: record.remark
  };
  open.value = true;
};

// 处理确定按钮
const handleOk = async () => {
  const state = await formRef.value.validate();
  if (state) return;
  
  if (!currentProject.value) {
    Message.error('项目信息不存在');
    return;
  }
  
  try {
    if (title.value === "新增字段") {
      // 新增字段
      const createParams: CreateFieldReq = {
        auth_info: getAuthInfo(),
        field_detail_info: {
          proj_id: Number(currentProject.value.id),
          field_name: addForm.value.fieldName,
          field_type: 1, // 默认为基础字段
          interface_name: addForm.value.fieldNameEn,
          desc: addForm.value.fieldDescription,
          is_required: addForm.value.isRequired,
          is_unique: addForm.value.isUnique,
          field_format_type: {
            field_primary_format: Number(addForm.value.primaryFormat),
            field_secondary_format: Number(addForm.value.secondaryFormat)
          },
          validation_rule: addForm.value.fieldValidationRules ? JSON.parse(addForm.value.fieldValidationRules) : undefined,
          write_example: addForm.value.writeExample,
          remark: addForm.value.remark
        }
      };
      
      await createField(createParams);
      Message.success("新增字段成功");
    } else {
      // 修改字段
      if (!currentEditingFieldId.value) {
        Message.error('找不到要修改的字段');
        return;
      }
      
      const updateParams: UpdateFieldReq = {
        auth_info: getAuthInfo(),
        proj_id: Number(currentProject.value.id),
        field_id: currentEditingFieldId.value,
        field_update_info: {
          desc: addForm.value.fieldDescription,
          is_required: addForm.value.isRequired,
          is_unique: addForm.value.isUnique,
          validation_rule: addForm.value.fieldValidationRules ? JSON.parse(addForm.value.fieldValidationRules) : undefined,
          write_example: addForm.value.writeExample,
          remark: addForm.value.remark
        }
      };
      
      await updateField(updateParams);
      Message.success("修改字段成功");
    }
    
    open.value = false;
    getFieldList();
  } catch (error) {
    console.error('操作失败:', error);
    Message.error('操作失败');
  }
};

// 关闭对话框
const afterClose = () => {
  formRef.value.resetFields();
  currentEditingFieldId.value = null;
  addForm.value = {
    fieldName: "",
    fieldNameEn: "",
    fieldDescription: "",
    primaryFormat: "",
    secondaryFormat: "",
    isRequired: false,
    isUnique: false,
    isMetadata: false,
    fieldValidationRules: "",
    writeExample: "",
    remark: ""
  };
};

// 删除字段
const handleDelete = async (record: FieldRecord) => {
  if (!currentProject.value) {
    Message.error('项目信息不存在');
    return;
  }
  
  try {
    await deleteField({
      auth_info: getAuthInfo(),
      proj_id: Number(currentProject.value.id),
      field_id: record.id
    });
    Message.success("删除成功");
    getFieldList();
  } catch (error) {
    console.error('删除字段失败:', error);
    Message.error("删除失败");
  }
};

// 批量删除
const batchDelete = async () => {
  if (selectedKeys.value.length === 0) {
    Message.warning('请选择要删除的字段');
    return;
  }
  
  if (!currentProject.value) {
    Message.error('项目信息不存在');
    return;
  }
  
  try {
    // 批量删除需要依次调用删除接口
    for (const fieldId of selectedKeys.value) {
      await deleteField({
        auth_info: getAuthInfo(),
        proj_id: Number(currentProject.value.id),
        field_id: fieldId
      });
    }
    
    Message.success("批量删除成功");
    selectedKeys.value = [];
    getFieldList();
  } catch (error) {
    console.error('批量删除字段失败:', error);
    Message.error("批量删除失败");
  }
};

onMounted(async () => {
  await fetchProjects();
  getFieldList();
});
</script>

<style lang="scss" scoped></style> 