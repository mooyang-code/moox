<template>
  <div class="moox-page">
    <a-spin :loading="loading">
      <template v-if="currentProject">
        <div class="moox-inner">
          <a-space wrap>
            <a-input v-model="form.fieldName" placeholder="请输入字段中文名" allow-clear />
            <a-input v-model="form.fieldNameEn" placeholder="请输入字段英文名" allow-clear />
            <a-select placeholder="字段类型" v-model="form.dataCategory" style="width: 150px" allow-clear>
              <a-option v-for="item in dataCategoryOptions" :key="item.value" :value="item.value">{{ item.name }}</a-option>
            </a-select>
            <a-select placeholder="关联数据集" v-model="form.relatedDataset" style="width: 180px" allow-clear>
              <a-option v-for="item in datasetList" :key="item.dataset_id" :value="item.dataset_id">{{ item.dataset_name }}</a-option>
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
              <a-button type="primary" status="warning" @click="onBatchImport">
                <template #icon>
                  <svg viewBox="0 0 48 48" fill="none" xmlns="http://www.w3.org/2000/svg" stroke="currentColor" class="arco-icon arco-icon-download" stroke-width="4" stroke-linecap="butt" stroke-linejoin="miter">
                    <path d="m33.072 22.071-9.07 9.071-9.072-9.07M24 5v26m16 4v6H8v-6"></path>
                  </svg>
                </template>
                <span>批量导入</span>
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
            :pagination="paginationConfig"
            :row-selection="{ type: 'checkbox', showCheckedAll: true }"
            :selected-keys="selectedKeys"
            @select="select"
            @select-all="selectAll"
            @page-change="onPageChange"
            @page-size-change="onPageSizeChange"
          >
            <template #columns>
              <a-table-column title="字段ID" data-index="id" :width="80"></a-table-column>
              <a-table-column title="字段中文名" data-index="fieldName" :width="120"></a-table-column>
              <a-table-column title="字段英文名" data-index="fieldNameEn" :width="120"></a-table-column>
              <a-table-column title="字段类型" :width="120" align="center">
                <template #cell="{ record }">
                  <a-tag bordered size="small" color="blue" v-if="record.dataCategory === 1">静态数据字段</a-tag>
                  <a-tag bordered size="small" color="green" v-else-if="record.dataCategory === 2">时序数据字段</a-tag>
                  <a-tag bordered size="small" color="gray" v-else>未知类型</a-tag>
                </template>
              </a-table-column>
              <a-table-column title="字段格式" data-index="fieldFormatText" :width="150"></a-table-column>
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
              <a-table-column title="关联数据集" data-index="relatedDatasets" :ellipsis="true" :tooltip="true" :width="150"></a-table-column>
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
            <a-input v-model="addForm.fieldNameEn" placeholder="请输入字段英文名" allow-clear :disabled="title === '修改字段'" />
            <template v-if="title === '修改字段'">
              <div style="font-size: 12px; color: #999; margin-top: 4px;">注：字段英文名创建后不可修改</div>
            </template>
          </a-form-item>
          <a-form-item field="fieldDescription" label="字段描述" validate-trigger="blur">
            <a-textarea v-model="addForm.fieldDescription" placeholder="请输入字段描述" allow-clear />
          </a-form-item>
          <a-form-item field="primaryFormat" label="字段主要类型" validate-trigger="blur">
            <a-select v-model="addForm.primaryFormat" placeholder="请选择字段主要类型" :disabled="title === '修改字段'">
              <a-option v-for="item in primaryTypes" :key="item.value" :value="item.value">{{ item.name }}</a-option>
            </a-select>
            <template v-if="title === '修改字段'">
              <div style="font-size: 12px; color: #999; margin-top: 4px;">注：字段类型创建后不可修改</div>
            </template>
          </a-form-item>
          <a-form-item field="secondaryFormat" label="字段次要类型" validate-trigger="blur">
            <a-select v-model="addForm.secondaryFormat" placeholder="请选择字段次要类型" :disabled="title === '修改字段'">
              <a-option v-for="item in secondaryTypes" :key="item.value" :value="item.value">{{ item.name }}</a-option>
            </a-select>
            <template v-if="title === '修改字段'">
              <div style="font-size: 12px; color: #999; margin-top: 4px;">注：字段类型创建后不可修改</div>
            </template>
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
            <a-textarea
              v-model="addForm.fieldValidationRules"
              placeholder="请输入JSON格式的数据校验规则（选填），如：{&quot;string_rule&quot;:{&quot;min_length&quot;:3,&quot;max_length&quot;:20,&quot;pattern&quot;:&quot;^[A-Z]+$&quot;}}"
              allow-clear
              :auto-size="{ minRows: 3, maxRows: 8 }"
              style="font-family: 'Monaco', 'Menlo', 'Ubuntu Mono', monospace; font-size: 13px;"
            />
          </a-form-item>
          <a-form-item field="writeExample" label="写入示例" validate-trigger="blur">
            <a-input v-model="addForm.writeExample" placeholder="请输入写入示例（选填）" allow-clear />
          </a-form-item>
          <a-form-item field="remark" label="备注" validate-trigger="blur">
            <a-textarea v-model="addForm.remark" placeholder="请输入备注" allow-clear />
          </a-form-item>
          <a-form-item field="relatedDatasets" label="关联数据集" validate-trigger="blur">
            <a-select 
              v-model="addForm.relatedDatasets"
              placeholder="请选择关联数据集（可多选）"
              multiple
              :allow-clear="true"
              :allow-search="true"
              :max-tag-count="3"
            >
              <a-option v-for="item in datasetList" :key="item.dataset_id" :value="item.dataset_id">{{ item.dataset_name }}</a-option>
            </a-select>
          </a-form-item>
        </a-form>
      </div>
    </a-modal>

    <!-- 批量导入对话框 -->
    <a-modal 
      v-model:visible="importOpen" 
      @close="afterImportClose" 
      @ok="handleImportOk" 
      @cancel="afterImportClose" 
      width="800px"
      :ok-loading="loading"
      ok-text="开始导入"
      cancel-text="取消"
    >
      <template #title>批量导入字段</template>
      <div>
        <div style="margin-bottom: 20px;">
          <h4 style="margin-bottom: 10px;">字段配置导入</h4>
          <div style="display: flex; gap: 20px; align-items: flex-start;">
            <!-- 导入说明 -->
            <div style="flex: 0 0 520px;">
              <a-alert type="info" style="padding: 12px 16px;">
                <div style="line-height: 1.4;">在配置项中，interface_name 的值将决定数据操作的类型（新增或修改）。</div>
                <div style="margin-top: 6px; line-height: 1.4;">
                  <div>• 若数据库中<strong>不存在</strong>该字段，将在数据库中<strong>新建</strong>该字段数据</div>
                  <div style="margin-top: 2px;">• 若数据库中<strong>已存在</strong>该字段，将在数据库中<strong>更新</strong>该字段数据</div>
                </div>
              </a-alert>
            </div>
            
            <!-- 文件上传区域 -->
            <div style="flex: 1; display: flex; justify-content: flex-end;">
              <div style="width: 240px;">
                <a-upload
                  ref="uploadRef"
                  :custom-request="handleUpload"
                  :show-file-list="true"
                  :limit="1"
                  accept=".yaml,.yml"
                  :before-upload="beforeUpload"
                  class="right-align-upload"
                >
                  <template #upload-button>
                    <div style="
                      border: 2px dashed #d9d9d9;
                      border-radius: 6px;
                      padding: 16px 20px;
                      text-align: center;
                      cursor: pointer;
                      transition: border-color 0.3s;
                      height: 66px;
                      display: flex;
                      flex-direction: column;
                      justify-content: center;
                      width: 100%;
                    ">
                      <div>
                        <svg viewBox="0 0 48 48" fill="none" xmlns="http://www.w3.org/2000/svg" stroke="currentColor" class="arco-icon arco-icon-upload" stroke-width="4" stroke-linecap="butt" stroke-linejoin="miter" style="font-size: 32px; color: #999; margin-bottom: 8px; width: 32px; height: 32px;">
                          <path d="M14.93 17.071 24.001 8l9.071 9.071m-9.07 16.071v-25M40 35v6H8v-6"></path>
                        </svg>
                      </div>
                      <div style="font-size: 14px; margin-bottom: 4px;">点击上传文件</div>
                      <div style="font-size: 12px; color: #999;">支持 .yaml、.yml 格式</div>
                    </div>
                  </template>
                </a-upload>
              </div>
            </div>
          </div>
        </div>

        <div>
          <div style="display: flex; justify-content: space-between; align-items: center; margin-bottom: 10px;">
            <h4 style="margin: 0;">导入格式参考</h4>
            <a-space>
              <a-button size="small" type="outline" @click="showFieldFormatTable">
                <template #icon>
                  <svg viewBox="0 0 48 48" fill="none" xmlns="http://www.w3.org/2000/svg" stroke="currentColor" stroke-width="4" stroke-linecap="butt" stroke-linejoin="miter" style="width: 14px; height: 14px;">
                    <path d="M24 44c11.046 0 20-8.954 20-20S35.046 4 24 4 4 12.954 4 24s8.954 20 20 20Z"/>
                    <path d="M24 28.625v-6.25"/>
                    <path d="M24 15.125V15"/>
                  </svg>
                </template>
                <span>字段格式说明</span>
              </a-button>
              <a-button size="small" type="primary" status="success" @click="copyYamlContent">
                <template #icon>
                  <svg viewBox="0 0 48 48" fill="none" xmlns="http://www.w3.org/2000/svg" stroke="currentColor" stroke-width="4" stroke-linecap="butt" stroke-linejoin="miter" style="width: 14px; height: 14px;">
                    <path d="M13 12.4v-4.8c0-1.773 1.14-3.2 2.55-3.2h16.9c1.41 0 2.55 1.427 2.55 3.2v4.8M13 12.4h22M13 12.4v26.8c0 1.773 1.14 3.2 2.55 3.2h16.9c1.41 0 2.55-1.427 2.55-3.2V12.4M21 20.4h6m-6 8h6"/>
                  </svg>
                </template>
                <span>点我复制</span>
              </a-button>
            </a-space>
          </div>
          <codemirror
            v-model="yamlCode"
            placeholder="YAML 配置..."
            :style="{ height: '400px', fontSize: '14px' }"
            :autofocus="false"
            :indent-with-tab="true"
            :tab-size="2"
            :extensions="extensions"
            :readonly="true"
            :line-wrapping="true"
          />
        </div>
      </div>
    </a-modal>

    <!-- 导入结果展示对话框 -->
    <a-modal 
      v-model:visible="importResultOpen" 
      @close="afterImportResultClose" 
      @cancel="afterImportResultClose" 
      width="900px"
      :footer="false"
    >
      <template #title>
        <div style="display: flex; align-items: center; gap: 8px;">
          <span>批量导入结果</span>
          <a-tag v-if="importResult.successList.length > 0" color="green" size="small">
            成功: {{ importResult.successList.length }}
          </a-tag>
          <a-tag v-if="importResult.failList.length > 0" color="red" size="small">
            失败: {{ importResult.failList.length }}
          </a-tag>
        </div>
      </template>
      <div>
        <!-- 成功列表 -->
        <div v-if="importResult.successList.length > 0" style="margin-bottom: 20px;">
          <h4 style="color: #52c41a; margin-bottom: 12px;">
            <icon-check-circle style="margin-right: 6px;" />
            导入成功的字段 ({{ importResult.successList.length }})
          </h4>
          <div style="max-height: 200px; overflow-y: auto; border: 1px solid #d9d9d9; border-radius: 4px; padding: 12px; background-color: #f6ffed;">
            <div 
              v-for="(item, index) in importResult.successList" 
              :key="index"
                             style="padding: 4px 0; border-bottom: 1px solid #e8f5e8;"
            >
              <div style="font-weight: 500; color: #389e0d;">{{ item.interface_name }}</div>
              <div style="font-size: 12px; color: #666; margin-left: 12px;">{{ item.field_name }} (ID: {{ item.field_id }})</div>
            </div>
          </div>
        </div>

        <!-- 失败列表 -->
        <div v-if="importResult.failList.length > 0">
          <h4 style="color: #ff4d4f; margin-bottom: 12px;">
            <icon-close-circle style="margin-right: 6px;" />
            导入失败的字段 ({{ importResult.failList.length }})
          </h4>
          <div style="max-height: 300px; overflow-y: auto; border: 1px solid #d9d9d9; border-radius: 4px; padding: 12px; background-color: #fff2f0;">
            <div 
              v-for="(item, index) in importResult.failList" 
              :key="index"
                             style="padding: 8px 0; border-bottom: 1px solid #ffccc7;"
            >
              <div style="font-weight: 500; color: #cf1322;">{{ item.interface_name }}</div>
              <div style="font-size: 12px; color: #666; margin-left: 12px;">{{ item.field_name }}</div>
              <div style="font-size: 12px; color: #cf1322; margin-left: 12px; margin-top: 2px;">
                <strong>失败原因：</strong>{{ item.error_message }}
              </div>
            </div>
          </div>
        </div>

        <!-- 总结信息 -->
        <div style="margin-top: 20px; padding: 12px; background-color: #f5f5f5; border-radius: 4px; text-align: center;">
          <div style="font-size: 14px; color: #666;">
            本次导入共处理 {{ importResult.successList.length + importResult.failList.length }} 个字段，
            成功 {{ importResult.successList.length }} 个，失败 {{ importResult.failList.length }} 个
          </div>
        </div>
      </div>
    </a-modal>

    <!-- 字段格式说明对话框 -->
    <a-modal
      v-model:visible="fieldFormatModalOpen"
      @close="afterFieldFormatClose"
      width="1000px"
      :footer="false"
    >
      <template #title>字段格式枚举值释义表</template>
      <div>
        <!-- 字段主要格式说明 -->
        <div style="margin-bottom: 20px;">
          <h4 style="margin-bottom: 10px; color: #1890ff;">字段主要格式 (field_primary_format)</h4>
          <a-table
            :data="primaryFormatTableData"
            :bordered="{ cell: true }"
            :pagination="false"
            size="small"
          >
            <template #columns>
              <a-table-column title="枚举值" data-index="value" :width="80" align="center">
                <template #cell="{ record }">
                  <a-tag color="blue" size="small">{{ record.value }}</a-tag>
                </template>
              </a-table-column>
              <a-table-column title="类型名称" data-index="name" :width="150"></a-table-column>
              <a-table-column title="英文名称" data-index="englishName" :width="150"></a-table-column>
              <a-table-column title="说明" data-index="description"></a-table-column>
            </template>
          </a-table>
        </div>

        <!-- 字段次要格式说明 -->
        <div>
          <h4 style="margin-bottom: 10px; color: #52c41a;">字段次要格式 (field_secondary_format)</h4>
          <a-table
            :data="secondaryFormatTableData"
            :bordered="{ cell: true }"
            :pagination="false"
            size="small"
          >
            <template #columns>
              <a-table-column title="枚举值" data-index="value" :width="80" align="center">
                <template #cell="{ record }">
                  <a-tag color="green" size="small">{{ record.value }}</a-tag>
                </template>
              </a-table-column>
              <a-table-column title="类型名称" data-index="name" :width="150"></a-table-column>
              <a-table-column title="英文名称" data-index="englishName" :width="150"></a-table-column>
              <a-table-column title="格式示例" data-index="formatExample" :width="200"></a-table-column>
              <a-table-column title="说明" data-index="description"></a-table-column>
            </template>
          </a-table>
        </div>

        <!-- 使用说明 -->
        <div style="margin-top: 20px; padding: 12px; background-color: #f5f5f5; border-radius: 4px;">
          <h4 style="margin-bottom: 8px; color: #666;">使用说明</h4>
          <div style="font-size: 14px; color: #666; line-height: 1.6;">
            <div>• <strong>字段主要格式</strong>：定义字段的基本数据类型，如字符串、整型、时间等</div>
            <div>• <strong>字段次要格式</strong>：用于约束字段值的具体格式，如日期格式、布尔值格式等</div>
            <div>• 在YAML配置中，使用 <code>field_primary_format</code> 和 <code>field_secondary_format</code> 来指定字段格式</div>
            <div>• 不同的主要格式对应不同的次要格式选项，请根据实际需求选择合适的组合</div>
          </div>
        </div>
      </div>
    </a-modal>
  </div>
</template>

<script setup lang="ts">
import { ref, onMounted, computed, nextTick, watch } from 'vue';
import { useRoute, useRouter } from 'vue-router';
import { Message } from '@arco-design/web-vue';
import { IconPlus, IconEdit, IconDelete, IconSearch, IconRefresh, IconCheckCircle, IconCloseCircle } from '@arco-design/web-vue/es/icon';
import { listProjects, type Project } from '@/api/project';
import { FIELD_SECONDARY_FORMAT_OPTIONS, getFieldSecondaryFormatName } from '@/typings/field-format';
import { 
  searchFields, 
  createField, 
  updateField, 
  deleteField,
  upsertField,
  type FieldDetailInfo,
  type SearchFieldReq,
  type CreateFieldReq,
  type UpdateFieldReq,
  type UpsertFieldReq,
  type AuthInfo
} from '@/api/field';
import * as YAML from 'js-yaml';
import { Codemirror } from 'vue-codemirror';
import { yaml } from '@codemirror/lang-yaml';
import { oneDark } from '@codemirror/theme-one-dark';


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
  fieldFormatText: string; // 字段格式（主要类型+次要类型组合）
  dataCategory: number; // 字段数据类型（1静态，2时序）
  isRequired: boolean;
  isUnique: boolean;
  isMetadata: boolean;
  fieldValidationRules: string;
  writeExample: string;
  remark: string;
  createTime: string;
  relatedDatasets: string; // 关联数据集名称（多个用逗号分隔）
  datasetIds: number[]; // 关联数据集ID列表
}

// 搜索表单
const form = ref({
  fieldName: "",
  fieldNameEn: "",
  dataCategory: null as number | null,
  relatedDataset: null as number | null,
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

// 数据类型选项
const dataCategoryOptions = ref([
  { value: 1, name: "静态数据字段" },
  { value: 2, name: "时序数据字段" }
]);

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
    dataCategory: null,
    relatedDataset: null,
    required: null
  };
  getFieldList();
};

// 表格相关
const loading = ref(false);
const pagination = ref({
  current: 1,
  pageSize: 15,
  total: 0,
  showPageSize: true,
  pageSizeOptions: [10, 15, 20, 50, 100]
});

// 分页配置，用于表格组件
const paginationConfig = computed(() => ({
  ...pagination.value,
  showTotal: true,
  showJumper: true,
  showPageSize: true,
  pageSizeOptions: [10, 15, 20, 50, 100],
  pageSizeProps: {
    style: { minWidth: '120px' } // 增加分页选择框宽度
  }
}));
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

// 每页条目数改变
const onPageSizeChange = (pageSize: number) => {
  pagination.value.pageSize = pageSize;
  pagination.value.current = 1; // 重置到第一页
  getFieldList();
};

// 获取当前项目的数据集列表
const datasetList = computed(() => {
  return currentProject.value?.datasets || [];
});

// 根据数据集ID获取数据集名称
const getDatasetNames = (datasetIds: number[]): string => {
  if (!datasetIds || datasetIds.length === 0) {
    return '';
  }
  
  const names = datasetIds.map(id => {
    const dataset = datasetList.value.find(d => d.dataset_id === id);
    return dataset ? dataset.dataset_name : '';
  }).filter(name => name);
  
  return names.join(', ');
};

// 将后台API字段转换为前端显示字段
const convertApiFieldToRecord = (apiField: FieldDetailInfo): FieldRecord => {
  // dataset_ids已经是number[]类型，直接使用
  const datasetIds = apiField.dataset_ids || [];

  const primaryFormatText = getTypeText(String(apiField.field_format_type.field_primary_format), primaryTypes.value);
  const secondaryFormatText = getFieldSecondaryFormatName(apiField.field_format_type.field_secondary_format);
  const fieldFormatText = `${primaryFormatText} - ${secondaryFormatText}`;

  return {
    id: apiField.field_id,
    fieldName: apiField.field_name,
    fieldNameEn: apiField.interface_name,
    fieldDescription: apiField.desc,
    primaryFormat: String(apiField.field_format_type.field_primary_format),
    primaryFormatText: primaryFormatText,
    secondaryFormat: String(apiField.field_format_type.field_secondary_format),
    secondaryFormatText: secondaryFormatText,
    fieldFormatText: fieldFormatText,
    dataCategory: apiField.data_category || 1, // 字段数据类型，默认为静态数据
    isRequired: apiField.required_flag === 1, // 1表示必填，-1表示非必填
    isUnique: apiField.unique_flag === 1, // 1表示唯一，-1表示非唯一
    isMetadata: apiField.metadata_flag === 1, // 1表示元数据，-1表示普通字段
    fieldValidationRules: apiField.validation_rule ? JSON.stringify(apiField.validation_rule) : '',
    writeExample: apiField.write_example || '',
    remark: apiField.remark || '',
    createTime: apiField.ctime || '',
    relatedDatasets: getDatasetNames(datasetIds),
    datasetIds: datasetIds
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

    const response = await searchFields(searchParams);

    // 转换API数据为前端展示格式
    fieldList.value = response.field_detail_infos.map(convertApiFieldToRecord);

    // 根据前端筛选条件进行过滤（如果后台API不支持某些筛选条件）
    let filteredList = fieldList.value;

    // 按数据类型筛选
    if (form.value.dataCategory !== null) {
      filteredList = filteredList.filter(item =>
        item.dataCategory === form.value.dataCategory
      );
    }

    // 按关联数据集筛选
    if (form.value.relatedDataset !== null) {
      filteredList = filteredList.filter(item =>
        item.datasetIds.includes(form.value.relatedDataset!)
      );
    }

    // 按是否必填筛选
    if (form.value.required !== null) {
      filteredList = filteredList.filter(item =>
        item.isRequired === form.value.required
      );
    }

    fieldList.value = filteredList;

    // 更新分页信息
    pagination.value.current = response.cur_page;
    // 如果有前端筛选，使用筛选后的数量，否则使用后台返回的总数
    pagination.value.total = (form.value.dataCategory !== null ||
                             form.value.relatedDataset !== null ||
                             form.value.required !== null)
                             ? filteredList.length
                             : response.total_num;
    
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
  remark: "",
  relatedDatasets: [] as number[] // 关联数据集ID列表
});

// 批量导入相关
const importOpen = ref<boolean>(false);
const uploadRef = ref();
const uploadedFileContent = ref<string>(''); // 存储上传的YAML文件内容

// 导入结果展示相关
const importResultOpen = ref<boolean>(false);

// 定义导入结果的类型接口
interface ImportSuccessItem {
  interface_name: string;
  field_name: string;
  field_id: number;
}

interface ImportFailItem {
  interface_name: string;
  field_name: string;
  error_message: string;
}

interface ImportResult {
  successList: ImportSuccessItem[];
  failList: ImportFailItem[];
}

const importResult = ref<ImportResult>({
  successList: [],
  failList: []
});

// CodeMirror YAML 配置
const yamlCode = ref(`fields:
  - interface_name: "symbol"  # 字段英文名，在项目维度下必须唯一
    field_name: "交易标的ID"   # 字段中文显示名称
    dataset_ids: [100,101]    # 关联的数据集ID列表，指定该字段在哪些数据集下生效（具体数据集ID请参考数据集列表）
    desc: "交易对标识符，如BTCUSDT，使用全大写字母格式" # 字段功能描述
    field_type: 1             # 字段数据类型分类（1=静态数据字段；2=时序数据字段）
    required_flag: 1          # 必填标记（-1非必填；1必填）
    unique_flag: 1            # 唯一约束标记（-1否；1是）
    metadata_flag: 1          # 元数据字段标记（-1否；1是）
    field_primary_format: 1   # 字段主要数据类型：1=字符串，2=整型，3=双精度浮点数，4=时间类型，5=选项类型，6=Set类型，7=Map类型k-v，8=Map类型k-list
    field_secondary_format: 3 # 字段次要数据类型，用于进一步限定数据格式
    validation_rule: '{"string_rule":{"min_length":3,"max_length":10,"pattern":"^[A-Z0-9]+$"}}' # 字符串类型校验规则（下划线格式）
    write_example: "BTCUSDT"  # 数据写入时的标准示例
    remark: "交易对标识符，必须使用全大写字母和数字组合" # 补充说明和注意事项

  - interface_name: "candle_begin_time" # 字段英文名，在项目维度下必须唯一
    field_name: "K线开始时间"   # 字段中文显示名称
    dataset_ids: [100,101]    # 关联的数据集ID列表，指定该字段在哪些数据集下生效（具体数据集ID请参考数据集列表）
    desc: "K线图表周期的起始时间戳" # 字段功能描述
    field_type: 2             # 字段数据类型分类（1=静态数据字段；2=时序数据字段）
    required_flag: 1          # 必填标记（-1非必填；1必填）
    unique_flag: -1           # 唯一约束标记（-1否；1是）
    metadata_flag: -1         # 元数据字段标记（-1否；1是）
    field_primary_format: 4   # 字段主要数据类型：4=时间类型
    field_secondary_format: 7 # 字段次要数据类型，用于进一步限定数据格式
    validation_rule: '{"format": "YYYY-MM-DD HH:mm:ss", "pattern": "^\\d{4}-\\d{2}-\\d{2} \\d{2}:\\d{2}:\\d{2}$"}' # 时间类型校验规则
    write_example: "2024-03-21 10:00:00" # 数据写入时的标准示例
    remark: "K线周期的精确起始时间点，采用标准日期时间格式" # 补充说明和注意事项

  - interface_name: "open_price"
    field_name: "开盘价"
    dataset_ids: [100,101]
    desc: "K线周期开盘价格"
    field_type: 2             # 字段数据类型分类（1=静态数据字段；2=时序数据字段）
    required_flag: 1
    unique_flag: -1
    metadata_flag: -1
    field_primary_format: 3   # 字段主要数据类型：3=双精度浮点数
    field_secondary_format: 2
    validation_rule: '{"min": 0, "max": 999999999}'  # 浮点数类型校验规则
    write_example: "42789.50"
    remark: "K线周期的开盘价格"

  - interface_name: "volume"
    field_name: "成交量"
    dataset_ids: [100,101]
    desc: "K线周期成交量"
    field_type: 2             # 字段数据类型分类（1=静态数据字段；2=时序数据字段）
    required_flag: 1
    unique_flag: -1
    metadata_flag: -1
    field_primary_format: 2   # 字段主要数据类型：2=整型
    field_secondary_format: 1
    validation_rule: '{"min": 0, "max": 999999999999}'  # 整型校验规则
    write_example: "1000000"
    remark: "K线周期的成交量"

  - interface_name: "exchange_type"
    field_name: "交易所类型"
    dataset_ids: [100,101]
    desc: "交易所类型选项"
    field_type: 1             # 字段数据类型分类（1=静态数据字段；2=时序数据字段）
    required_flag: 1
    unique_flag: -1
    metadata_flag: -1
    field_primary_format: 5   # 字段主要数据类型：5=选项类型
    field_secondary_format: 11
    validation_rule: '{"lib_id": 1}'  # 选项类型校验规则，需要提供值库ID
    write_example: "1"
    remark: "交易所类型，关联属性值库"`);

const extensions = [yaml(), oneDark];

// 动态验证规则
const rules = computed(() => {
  const baseRules = {
    fieldName: [{ required: true, message: "请输入字段中文名" }],
    fieldDescription: [{ required: true, message: "请输入字段描述" }],
    relatedDatasets: [{ 
      required: true, 
      message: "请选择关联数据集",
      validator: (value: any, callback: any) => {
        if (!value || (Array.isArray(value) && value.length === 0)) {
          callback("请选择关联数据集");
        } else {
          callback();
        }
      }
    }]
  };
  
  // 新增时需要验证所有必填字段
  if (title.value === "新增字段") {
    return {
      ...baseRules,
      fieldNameEn: [{ required: true, message: "请输入字段英文名" }],
      primaryFormat: [{ required: true, message: "请选择字段主要类型" }],
      secondaryFormat: [{ required: true, message: "请选择字段次要类型" }]
    };
  }
  
  // 修改时验证可修改的字段（包括字段中文名和关联数据集）
  return baseRules;
});

const formRef = ref();

// 新增字段
const onAdd = async () => {
  // 确保表单重置为正确的初始状态
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
    remark: "",
    relatedDatasets: [] as number[] // 明确初始化为空数组
  };
  
  title.value = "新增字段";
  open.value = true;
  
  // 确保DOM更新后再进行任何操作
  await nextTick();
};

// 当前正在编辑的字段ID
const currentEditingFieldId = ref<number | null>(null);

// 修改字段
const onUpdate = async (record: FieldRecord) => {
  title.value = "修改字段";
  currentEditingFieldId.value = record.id;
  
  // 确保 relatedDatasets 始终是数组
  const relatedDatasets = Array.isArray(record.datasetIds) ? record.datasetIds : [];
  
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
    remark: record.remark,
    relatedDatasets: [...relatedDatasets] // 使用扩展运算符确保是新数组
  };
  
  open.value = true;
  
  // 确保DOM更新后再进行任何操作
  await nextTick();
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
      // 确保 dataset_ids 始终是数组
      const datasetIds = Array.isArray(addForm.value.relatedDatasets) 
        ? addForm.value.relatedDatasets 
        : (addForm.value.relatedDatasets ? [addForm.value.relatedDatasets] : []);
      
      // 新增字段
      const createParams: CreateFieldReq = {
        auth_info: getAuthInfo(),
        operator: "web_frontend", // 添加操作者标识
        field_detail_info: {
          proj_id: Number(currentProject.value.id),
          dataset_ids: datasetIds,
          field_name: addForm.value.fieldName,
          field_type: 1, // 默认为基础字段
          interface_name: addForm.value.fieldNameEn,
          desc: addForm.value.fieldDescription,
          required_flag: addForm.value.isRequired ? 1 : -1, // 1必填，-1非必填
          unique_flag: addForm.value.isUnique ? 1 : -1, // 1唯一，-1非唯一
          metadata_flag: addForm.value.isMetadata ? 1 : -1, // 1元数据，-1普通字段
          field_format_type: {
            field_primary_format: Number(addForm.value.primaryFormat),
            field_secondary_format: Number(addForm.value.secondaryFormat)
          },
          validation_rule: addForm.value.fieldValidationRules ? JSON.parse(addForm.value.fieldValidationRules) : undefined,
          write_example: addForm.value.writeExample,
          remark: addForm.value.remark
        }
      };
      
      const result = await createField(createParams);
      if (result.ret_info?.code === 0) {
        Message.success("新增字段成功");
      } else {
        Message.error(`新增字段失败[${result.ret_info?.code}]: ${result.ret_info?.msg || '未知错误'}`);
        return;
      }
    } else {
      // 修改字段
      if (!currentEditingFieldId.value) {
        Message.error('找不到要修改的字段');
        return;
      }
      
      // 确保 dataset_ids 始终是数组
      const updateDatasetIds = Array.isArray(addForm.value.relatedDatasets) 
        ? addForm.value.relatedDatasets 
        : (addForm.value.relatedDatasets ? [addForm.value.relatedDatasets] : []);
      
      const updateParams: UpdateFieldReq = {
        auth_info: getAuthInfo(),
        proj_id: Number(currentProject.value.id),
        field_id: currentEditingFieldId.value,
        field_update_info: {
          dataset_ids: updateDatasetIds,
          field_name: addForm.value.fieldName,  // 允许修改字段中文名
          desc: addForm.value.fieldDescription,
          required_flag: addForm.value.isRequired ? 1 : -1, // 1必填，-1非必填
          unique_flag: addForm.value.isUnique ? 1 : -1, // 1唯一，-1非唯一
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
    const errorMsg = error instanceof Error ? error.message : '操作失败';
    Message.error(errorMsg);
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
    remark: "",
    relatedDatasets: [] as number[] // 明确类型
  };
};

// 删除字段
const handleDelete = async (record: FieldRecord) => {
  if (!currentProject.value) {
    Message.error('项目信息不存在');
    return;
  }
  
  try {
    const result = await deleteField({
      auth_info: getAuthInfo(),
      proj_id: Number(currentProject.value.id),
      field_id: record.id
    });
    
    if (result?.ret_info?.code === 0) {
      Message.success("删除成功");
      getFieldList();
    } else {
      Message.error(`删除失败[${result?.ret_info?.code}]: ${result?.ret_info?.msg || '未知错误'}`);
    }
  } catch (error) {
    console.error('删除字段失败:', error);
    const errorMsg = error instanceof Error ? error.message : '删除失败';
    Message.error(errorMsg);
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
  
  loading.value = true;
  let successCount = 0;
  let failCount = 0;
  const errors: string[] = [];
  
  try {
    // 批量删除需要依次调用删除接口
    for (const fieldId of selectedKeys.value) {
      try {
        const result = await deleteField({
          auth_info: getAuthInfo(),
          proj_id: Number(currentProject.value.id),
          field_id: fieldId
        });
        
        if (result.ret_info.code === 0) {
          successCount++;
        } else {
          failCount++;
          errors.push(`字段ID ${fieldId} 删除失败[${result.ret_info.code}]: ${result.ret_info.msg}`);
        }
      } catch (error) {
        failCount++;
        const errorMsg = error instanceof Error ? error.message : '未知错误';
        errors.push(`字段ID ${fieldId} 删除异常: ${errorMsg}`);
      }
    }
    
    // 显示删除结果
    if (successCount > 0 && failCount === 0) {
      Message.success(`批量删除成功！共删除${successCount}个字段`);
    } else if (successCount > 0 && failCount > 0) {
      Message.warning(`删除完成！成功${successCount}个，失败${failCount}个。失败详情：${errors.join('; ')}`);
    } else {
      Message.error(`批量删除失败！共${failCount}个字段删除失败。失败详情：${errors.join('; ')}`);
    }
    
    selectedKeys.value = [];
    getFieldList();
  } catch (error) {
    console.error('批量删除字段失败:', error);
    const errorMsg = error instanceof Error ? error.message : '批量删除失败';
    Message.error(errorMsg);
  } finally {
    loading.value = false;
  }
};

// 批量导入相关方法
const onBatchImport = () => {
  importOpen.value = true;
};

const afterImportClose = () => {
  importOpen.value = false;
  // 安全地清空上传组件的文件列表
  try {
    if (uploadRef.value && uploadRef.value.fileList) {
      uploadRef.value.fileList.splice(0);
    }
  } catch (error) {
    console.warn('清空上传文件列表失败:', error);
  }
  // 清空文件内容
  uploadedFileContent.value = '';
};

// 关闭导入结果对话框
const afterImportResultClose = () => {
  importResultOpen.value = false;
  // 清空导入结果
  importResult.value = {
    successList: [],
    failList: []
  };
};

// 解析validation_rule字符串为ValidationRule对象
// 只支持标准下划线格式: {"string_rule":{"min_length":3,"max_length":20}}
const parseValidationRule = (validationRuleStr: string, fieldPrimaryFormat: number): any => {
  if (!validationRuleStr) {
    return undefined;
  }

  try {
    const ruleObj = typeof validationRuleStr === 'string' ? JSON.parse(validationRuleStr) : validationRuleStr;

    // 检查是否已经是标准下划线格式
    if (ruleObj.string_rule || ruleObj.integer_rule || ruleObj.double_rule || ruleObj.option_rule) {
      // 已经是标准格式，直接返回
      return ruleObj;
    }

    // 根据字段一级格式构建相应的ValidationRule（使用下划线格式）
    switch (fieldPrimaryFormat) {
      case 1: // STRING - 字符串类型
        return {
          string_rule: {
            min_length: ruleObj.min_length,
            max_length: ruleObj.max_length,
            length: ruleObj.length,
            const: ruleObj.const,
            pattern: ruleObj.pattern,
            prefix: ruleObj.prefix,
            suffix: ruleObj.suffix,
            contains: ruleObj.contains,
            not_contains: ruleObj.not_contains,
            in: ruleObj.in,
            not_in: ruleObj.not_in,
            not_pattern: ruleObj.not_pattern,
            format: ruleObj.format
          }
        };

      case 2: // INTEGER - 整型
        return {
          integer_rule: {
            min: ruleObj.min,
            max: ruleObj.max,
            const: ruleObj.const,
            lt: ruleObj.lt,
            lte: ruleObj.lte,
            gt: ruleObj.gt,
            gte: ruleObj.gte,
            in: ruleObj.in,
            not_in: ruleObj.not_in
          }
        };

      case 3: // DOUBLE - 双精度浮点数
        return {
          double_rule: {
            min: ruleObj.min,
            max: ruleObj.max,
            const: ruleObj.const,
            lt: ruleObj.lt,
            lte: ruleObj.lte,
            gt: ruleObj.gt,
            gte: ruleObj.gte,
            in: ruleObj.in,
            not_in: ruleObj.not_in
          }
        };

      case 4: // TIME - 时间类型，使用字符串规则
        return {
          string_rule: {
            format: ruleObj.format,
            pattern: ruleObj.pattern,
            min_length: ruleObj.min_length,
            max_length: ruleObj.max_length
          }
        };

      case 5: // OPTION - 选项类型
        return {
          option_rule: {
            lib_id: ruleObj.lib_id
          }
        };

      default:
        // 对于其他类型，尝试智能判断
        if (ruleObj.lib_id) {
          return { option_rule: { lib_id: ruleObj.lib_id } };
        } else if (typeof ruleObj.min === 'number' && Number.isInteger(ruleObj.min)) {
          return { integer_rule: ruleObj };
        } else if (typeof ruleObj.min === 'number') {
          return { double_rule: ruleObj };
        } else {
          return { string_rule: ruleObj };
        }
    }
  } catch (error) {
    console.error('解析validation_rule失败:', error);
    return undefined;
  }
};

const handleImportOk = async () => {
  // 初始化导入结果
  importResult.value = {
    successList: [],
    failList: []
  };
  
  if (!uploadedFileContent.value) {
    importResult.value.failList.push({
      interface_name: '系统检查',
      field_name: '文件上传',
      error_message: '请先上传YAML文件'
    });
    importResultOpen.value = true;
    return;
  }
  
  if (!currentProject.value) {
    importResult.value.failList.push({
      interface_name: '系统检查',
      field_name: '项目信息',
      error_message: '项目信息不存在'
    });
    importResultOpen.value = true;
    return;
  }
  
  try {
    // 重新验证YAML格式
    const validationResult = validateYamlFormat(uploadedFileContent.value);
    if (!validationResult.isValid) {
      importResult.value.failList.push({
        interface_name: '系统检查',
        field_name: 'YAML格式验证',
        error_message: validationResult.errorMessage || 'YAML格式验证失败'
      });
      importResultOpen.value = true;
      return;
    }
    
    // 解析YAML文件
    const yamlData = YAML.load(uploadedFileContent.value) as any;
    
    if (!yamlData || !yamlData.fields || !Array.isArray(yamlData.fields)) {
      importResult.value.failList.push({
        interface_name: '系统检查',
        field_name: 'YAML解析',
        error_message: 'YAML文件格式错误：缺少fields字段或fields不是数组'
      });
      importResultOpen.value = true;
      return;
    }
    
    const fields = yamlData.fields;
    if (fields.length === 0) {
      importResult.value.failList.push({
        interface_name: '系统检查',
        field_name: '字段配置',
        error_message: 'YAML文件中没有字段配置'
      });
      importResultOpen.value = true;
      return;
    }
    
    // 显示导入进度
    loading.value = true;
    
    // 批量导入字段
    for (let i = 0; i < fields.length; i++) {
      const fieldConfig = fields[i];
      
      try {
        // 验证必要字段
        if (!fieldConfig.interface_name || !fieldConfig.field_name) {
          importResult.value.failList.push({
            interface_name: fieldConfig.interface_name || `字段${i + 1}`,
            field_name: fieldConfig.field_name || '未知字段名',
            error_message: '缺少必要的interface_name或field_name'
          });
          continue;
        }
        
        // 验证dataset_ids
        if (!fieldConfig.dataset_ids || !Array.isArray(fieldConfig.dataset_ids)) {
          importResult.value.failList.push({
            interface_name: fieldConfig.interface_name,
            field_name: fieldConfig.field_name,
            error_message: 'dataset_ids格式错误，必须为数组格式如: [100, 101]'
          });
          continue;
        }
        
        // 验证field_primary_format
        const fieldPrimaryFormat = Number(fieldConfig.field_primary_format) || 1;
        if (fieldPrimaryFormat < 1 || fieldPrimaryFormat > 8) {
          importResult.value.failList.push({
            interface_name: fieldConfig.interface_name,
            field_name: fieldConfig.field_name,
            error_message: `field_primary_format值无效: ${fieldPrimaryFormat}，有效范围：1-8`
          });
          continue;
        }
        
        // 解析validation_rule
        const validationRule = parseValidationRule(fieldConfig.validation_rule, fieldPrimaryFormat);
        
        // 处理字段类型：从配置中读取，如果没有则默认为1（静态数据字段）
        let fieldType = 1; // 默认为静态数据字段
        if (fieldConfig.field_type !== undefined && fieldConfig.field_type !== null) {
          fieldType = Number(fieldConfig.field_type);
          // 验证字段类型的有效性（1=静态数据字段，2=时序数据字段）
          if (fieldType !== 1 && fieldType !== 2) {
            importResult.value.failList.push({
              interface_name: fieldConfig.interface_name || '未知字段',
              field_name: fieldConfig.field_name || '未知字段名',
              error_message: `字段类型无效：field_type=${fieldType}，有效值为1（静态数据字段）或2（时序数据字段）`
            });
            continue;
          }
        }

        // 构建UpsertField请求参数
        const upsertParams: UpsertFieldReq = {
          auth_info: getAuthInfo(),
          proj_id: Number(currentProject.value.id),
          interface_name: fieldConfig.interface_name,
          operator: 'web_frontend',
                  field_detail_info: {
          proj_id: Number(currentProject.value.id),
          dataset_ids: fieldConfig.dataset_ids,
          field_name: fieldConfig.field_name,
          field_type: fieldType, // 使用从配置中读取的字段类型
          interface_name: fieldConfig.interface_name,
          desc: fieldConfig.desc || '',
          required_flag: fieldConfig.required_flag || (fieldConfig.is_required ? 1 : -1), // 1必填，-1非必填
          unique_flag: fieldConfig.unique_flag || (fieldConfig.is_unique ? 1 : -1), // 1唯一，-1非唯一
          metadata_flag: fieldConfig.metadata_flag || (fieldConfig.is_meta ? 1 : -1), // 1元数据，-1普通字段
          field_format_type: {
            field_primary_format: fieldPrimaryFormat,
            field_secondary_format: Number(fieldConfig.field_secondary_format) || 1
          },
          validation_rule: validationRule,
          write_example: fieldConfig.write_example || '',
          remark: fieldConfig.remark || ''
        }
        };
        
        // 调用UpsertField接口
        let result;
        try {
          result = await upsertField(upsertParams);
        } catch (apiError) {
          // API调用异常，尝试从异常中提取错误信息
          console.error(`API调用异常:`, apiError);
          
          let errorMessage = '网络异常或未知错误';
          
          // 尝试从异常中提取ret_info信息
          if (apiError && typeof apiError === 'object') {
            const errorObj = apiError as any;
            // 如果异常对象包含response数据
            if (errorObj.response && errorObj.response.data && errorObj.response.data.ret_info) {
              errorMessage = errorObj.response.data.ret_info.msg || errorMessage;
            }
            // 如果异常对象直接包含ret_info
            else if (errorObj.ret_info && errorObj.ret_info.msg) {
              errorMessage = errorObj.ret_info.msg;
            }
            // 如果异常有message属性
            else if (errorObj.message) {
              errorMessage = errorObj.message;
            }
          }
          
          importResult.value.failList.push({
            interface_name: fieldConfig.interface_name,
            field_name: fieldConfig.field_name,
            error_message: errorMessage
          });
          continue;
        }
        
        // 检查返回结果
        if (result && result.ret_info && result.ret_info.code === 0) {
          // 导入成功
          importResult.value.successList.push({
            interface_name: fieldConfig.interface_name,
            field_name: fieldConfig.field_name,
            field_id: result.field_id || 0
          });
          console.log(`字段(${fieldConfig.interface_name})导入成功, field_id: ${result.field_id}`);
        } else {
          // 后台返回了错误码
          const errorMsg = (result && result.ret_info && result.ret_info.msg) || '未知错误';
          importResult.value.failList.push({
            interface_name: fieldConfig.interface_name,
            field_name: fieldConfig.field_name,
            error_message: errorMsg // 直接显示后台返回的错误信息
          });
          console.error(`字段导入失败:`, result);
        }
        
      } catch (error) {
        // 这里应该只处理真正的意外异常，比如参数构造错误等
        console.error(`字段处理过程中发生意外异常:`, error);
        const errorMsg = error instanceof Error ? error.message : '意外异常';
        importResult.value.failList.push({
          interface_name: fieldConfig.interface_name || `字段${i + 1}`,
          field_name: fieldConfig.field_name || '未知字段名',
          error_message: `处理异常: ${errorMsg}`
        });
      }
    }
    
    // 显示导入结果
    const successCount = importResult.value.successList.length;
    const failCount = importResult.value.failList.length;
    const totalCount = successCount + failCount;
    
    // 先显示简单的消息通知
    if (successCount > 0 && failCount === 0) {
      Message.success(`批量导入成功！共导入${successCount}个字段`);
    } else if (successCount > 0 && failCount > 0) {
      Message.warning(`导入完成！成功${successCount}个，失败${failCount}个`);
    } else if (failCount > 0) {
      Message.error(`导入失败！共${failCount}个字段导入失败`);
    }
    
    // 刷新字段列表
    if (successCount > 0) {
      getFieldList();
    }
    
    // 显示详细结果对话框（如果有数据要显示）
    if (totalCount > 0) {
      importResultOpen.value = true;
      // 在显示结果对话框后再关闭导入对话框，避免引起异常
      setTimeout(() => {
        afterImportClose();
      }, 100);
    } else {
      // 如果没有结果要显示，直接关闭导入对话框
      afterImportClose();
    }
    
  } catch (error) {
    console.error('批量导入过程中发生系统异常:', error);
    const errorMsg = error instanceof Error ? error.message : '未知错误';
    
    // 只有在真正的系统异常时才添加到错误列表
    // 正常的业务错误应该在上面的循环中已经被处理了
    if (!importResult.value.successList.length && !importResult.value.failList.length) {
      // 如果没有任何结果，说明是在初始化阶段就发生了异常
      importResult.value.failList.push({
        interface_name: '系统异常',
        field_name: '批量导入',
        error_message: `批量导入失败: ${errorMsg}`
      });
    }
    
    // 显示详细结果对话框
    importResultOpen.value = true;
    
    // 发生异常时也要关闭导入对话框
    setTimeout(() => {
      afterImportClose();
    }, 100);
  } finally {
    loading.value = false;
  }
};

const beforeUpload = (file: File) => {
  const isValidType = file.type === 'application/x-yaml' ||
                     file.type === 'text/yaml' ||
                     file.type === 'text/x-yaml' ||
                     file.name.endsWith('.yaml') ||
                     file.name.endsWith('.yml');
  
  if (!isValidType) {
    Message.error('只支持上传 YAML 文件');
    return false;
  }
  
  const isLt10M = file.size / 1024 / 1024 < 10;
  if (!isLt10M) {
    Message.error('文件大小不能超过 10MB');
    return false;
  }
  
  return true;
};

const handleUpload = (option: any) => {
  const { fileItem } = option;
  const file = fileItem.file;
  
  // 检查文件是否存在
  if (!file) {
    Message.error('文件不存在');
    option.onError();
    return;
  }
  
  // 读取并解析YAML文件
  const reader = new FileReader();
  reader.onload = (e) => {
    try {
      const content = e.target?.result as string;
      
      // 验证YAML格式
      const validationResult = validateYamlFormat(content);
      
      if (validationResult.isValid) {
        // 保存文件内容
        uploadedFileContent.value = content;
        Message.success('YAML文件格式验证通过，上传成功');
        option.onSuccess();
      } else {
        // 文件格式验证失败，将错误信息保存起来，在导入时展示
        console.error('YAML文件验证失败:', validationResult.errorMessage);
        // 依然保存文件内容，让用户可以点击导入来查看详细错误
        uploadedFileContent.value = content;
        Message.warning('YAML文件格式存在问题，请点击导入查看详细错误信息');
        option.onSuccess();
      }
    } catch (error) {
      console.error('文件读取失败:', error);
      Message.error('文件读取失败');
      option.onError();
    }
  };
  
  reader.onerror = () => {
    Message.error('文件读取失败');
    option.onError();
  };
  
  reader.readAsText(file);
  
  return {
    abort: () => {
      reader.abort();
      console.log('上传中止');
    }
  };
};

// 验证YAML格式 - 返回验证结果和错误信息
const validateYamlFormat = (content: string): { isValid: boolean; errorMessage?: string } => {
  try {
    // 简单的YAML格式检查
    const lines = content.trim().split('\n');
    
    // 检查是否以 fields: 开头
    if (!lines[0].trim().startsWith('fields:')) {
      return { isValid: false, errorMessage: 'YAML文件必须以 "fields:" 开头' };
    }
    
    // 检查是否包含必要的字段
    const requiredFields = [
      'interface_name',
      'field_name', 
      'dataset_ids',
      'desc',
      'required_flag',
      'unique_flag',
      'metadata_flag',
      'field_primary_format',
      'field_secondary_format'
    ];
    
    const contentStr = content.toLowerCase();
    for (const field of requiredFields) {
      if (!contentStr.includes(field)) {
        return { isValid: false, errorMessage: `YAML文件缺少必要字段: ${field}` };
      }
    }
    
    // 检查是否包含字段项目（以 "- interface_name:" 开头的行）
    const hasFieldItems = lines.some(line => 
      line.trim().startsWith('- interface_name:')
    );
    
    if (!hasFieldItems) {
      return { isValid: false, errorMessage: 'YAML文件中没有找到有效的字段配置项' };
    }
    
    // 检查dataset_ids格式
    const datasetIdsPattern = /dataset_ids:\s*\[[\d\s,]+\]/;
    if (!datasetIdsPattern.test(content)) {
      return { isValid: false, errorMessage: 'dataset_ids 格式不正确，应为数组格式如: [100, 101]' };
    }
    
    return { isValid: true };
  } catch (error) {
    console.error('YAML格式验证失败:', error);
    return { isValid: false, errorMessage: 'YAML格式验证失败' };
  }
};

// 复制 YAML 内容到剪切板
const copyYamlContent = async () => {
  try {
    await navigator.clipboard.writeText(yamlCode.value);
    Message.success('YAML 配置已复制到剪切板');
  } catch (error) {
    // 如果现代剪切板 API 不可用，使用传统方法
    const textArea = document.createElement('textarea');
    textArea.value = yamlCode.value;
    textArea.style.position = 'fixed';
    textArea.style.opacity = '0';
    document.body.appendChild(textArea);
    textArea.select();
    try {
      document.execCommand('copy');
      Message.success('YAML 配置已复制到剪切板');
    } catch (fallbackError) {
      Message.error('复制失败，请手动复制');
    }
    document.body.removeChild(textArea);
  }
};

// 监控 addForm.relatedDatasets 确保始终为数组
watch(() => addForm.value.relatedDatasets, (newVal) => {
  if (!Array.isArray(newVal)) {
    addForm.value.relatedDatasets = newVal ? [newVal] : [];
  }
}, { deep: true });

// 字段格式说明相关
const fieldFormatModalOpen = ref<boolean>(false);

const afterFieldFormatClose = () => {
  fieldFormatModalOpen.value = false;
};

const showFieldFormatTable = () => {
  fieldFormatModalOpen.value = true;
};

// 字段主要格式表格数据
const primaryFormatTableData = ref([
  {
    value: 1,
    name: "字符串",
    englishName: "STRING",
    description: "用于存储文本数据，如姓名、标识符等"
  },
  {
    value: 2,
    name: "整型",
    englishName: "INTEGER",
    description: "用于存储整数数据，如数量、ID等"
  },
  {
    value: 3,
    name: "双精度浮点数",
    englishName: "DOUBLE",
    description: "用于存储小数数据，如价格、比率等"
  },
  {
    value: 4,
    name: "时间类型",
    englishName: "TIME",
    description: "用于存储时间相关数据，如日期、时间戳等"
  },
  {
    value: 5,
    name: "选项类型",
    englishName: "OPTION",
    description: "用于存储预定义选项值，关联选项值库"
  },
  {
    value: 6,
    name: "Set类型",
    englishName: "SET",
    description: "用于存储字符串集合，无重复值"
  },
  {
    value: 7,
    name: "Map类型k-v",
    englishName: "MAP_KV",
    description: "用于存储键值对数据"
  },
  {
    value: 8,
    name: "Map类型k-list",
    englishName: "MAP_KLIST",
    description: "用于存储键对应列表值的数据"
  }
]);

// 字段次要格式表格数据
const secondaryFormatTableData = ref([
  {
    value: 1,
    name: "普通文本类型",
    englishName: "TEXT",
    formatExample: "任意文本内容",
    description: "普通的文本字符串，无特殊格式约束"
  },
  {
    value: 2,
    name: "布尔类型",
    englishName: "BOOLEAN",
    formatExample: "true/false",
    description: "布尔值，只能为true或false"
  },
  {
    value: 3,
    name: "日期",
    englishName: "DATE",
    formatExample: "2021-02-03",
    description: "日期格式，YYYY-MM-DD"
  },
  {
    value: 4,
    name: "日期范围",
    englishName: "DATE_RANGE",
    formatExample: "2021-02-03 ~ 2022-03-02",
    description: "两个日期之间的范围"
  },
  {
    value: 5,
    name: "日期时间",
    englishName: "DATE_TIME",
    formatExample: "2021-02-03 08:00:00",
    description: "日期时间格式，YYYY-MM-DD HH:mm:ss"
  },
  {
    value: 6,
    name: "日期时间范围",
    englishName: "DATE_TIME_RANGE",
    formatExample: "2021-02-03 08:00:00 ~ 2022-03-02 09:00:01",
    description: "两个日期时间之间的范围"
  },
  {
    value: 7,
    name: "秒级时间戳",
    englishName: "TIMESTAMP",
    formatExample: "1661411887",
    description: "Unix时间戳，精确到秒"
  },
  {
    value: 8,
    name: "ISO8601格式日期",
    englishName: "DATE_ISO8601",
    formatExample: "2025-04-12T20:36:00+08:00",
    description: "ISO8601标准格式的日期时间"
  },
  {
    value: 9,
    name: "链接",
    englishName: "URI",
    formatExample: "http://puui.qpic.cn/emuczz1543346158",
    description: "URL链接格式"
  },
  {
    value: 10,
    name: "JSON",
    englishName: "JSON",
    formatExample: '{"key": "value"}',
    description: "JSON格式的字符串"
  },
  {
    value: 11,
    name: "选项值ID",
    englishName: "OPTION_VALUE",
    formatExample: "1",
    description: "选项值库中对应选项的ID"
  },
  {
    value: 12,
    name: "选项值中文文案",
    englishName: "OPTION_NAME",
    formatExample: "交易所A",
    description: "选项值库中对应选项的显示名称"
  }
]);

onMounted(async () => {
  await fetchProjects();
  getFieldList();
});
</script>

<style lang="scss" scoped>
.right-align-upload {
  :deep(.arco-upload-wrapper.arco-upload-wrapper-type-text) {
    width: 100% !important;
  }

  :deep(.arco-upload-list) {
    width: 100% !important;
  }
}

// 优化分页组件样式
:deep(.arco-pagination-size-selector) {
  .arco-select {
    min-width: 120px !important;
  }
}

:deep(.arco-pagination) {
  .arco-pagination-item-jumper {
    margin-left: 16px;
  }
}
</style>
