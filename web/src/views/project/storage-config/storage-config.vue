<template>
  <div class="moox-page">
    <div class="container">
      <!-- 总体说明 -->
      <a-card :bordered="false" class="overview-card">
        <a-descriptions title="存储配置总览" :data="overviewData" :column="3" :align="{ label: 'right' }" />
        <a-alert type="info" style="margin-top: 16px;" :show-icon="false">
          <div>存储配置管理系统用于管理数据存储的各种配置，包括存储节点、存储设备以及数据路由配置。</div>
          <div>通过合理配置可以实现数据的高效存储和快速访问。</div>
        </a-alert>
      </a-card>

      <!-- Tab切换区域 -->
      <a-card class="margin-top" :bordered="false">
        <a-tabs :type="type" :size="size" v-model:active-key="activeTab">
          <a-tab-pane key="storage-node" title="存储节点配置">
            <StorageNodeConfig :nodes="storageNodes" :loading="loading" @refresh="loadStorageNodes" />
          </a-tab-pane>
          <a-tab-pane key="storage-device" title="存储设备配置">
            <StorageDeviceConfig :devices="storageDevices" :loading="loading" @refresh="loadStorageDevices" />
          </a-tab-pane>
          <a-tab-pane key="object-route" title="数据对象-路由配置">
            <ObjectRouteConfig :routes="objectRoutes" :loading="loading" @refresh="loadObjectRoutes" />
          </a-tab-pane>
          <a-tab-pane key="field-route" title="数据字段-路由配置">
            <FieldRouteConfig :routes="fieldRoutes" :loading="loading" @refresh="loadFieldRoutes" />
          </a-tab-pane>
        </a-tabs>
      </a-card>
    </div>
  </div>
</template>

<script lang="ts" setup>
import { ref, onMounted, watch, computed } from 'vue';
import { useRoute } from 'vue-router';
import { Message } from '@arco-design/web-vue';
import StorageNodeConfig from './components/storage-node-config.vue';
import StorageDeviceConfig from './components/storage-device-config.vue';
import ObjectRouteConfig from './components/object-route-config.vue';
import FieldRouteConfig from './components/field-route-config.vue';

// 导入API接口
import { 
  listStorageNodes, 
  listStorageDevices, 
  listObjectRoutes, 
  listFieldRoutes,
  type StorageNode,
  type StorageDevice,
  type ObjectRoute,
  type FieldRoute
} from '@/api/storage-config';

// 路由信息
const route = useRoute();

// 获取当前项目ID
const currentProjectId = computed(() => {
  const projectId = route.params.projectId;
  return projectId ? Number(projectId) : null;
});

// Tab配置
const type = ref("rounded");
const size = ref("medium");
const activeTab = ref("storage-node");

// 数据状态
const loading = ref(false);
const storageNodes = ref<StorageNode[]>([]);
const storageDevices = ref<StorageDevice[]>([]);
const objectRoutes = ref<ObjectRoute[]>([]);
const fieldRoutes = ref<FieldRoute[]>([]);

// 总览数据
const overviewData = ref([
  {
    label: "存储节点数量：",
    value: "0"
  },
  {
    label: "存储设备数量：",
    value: "0"
  },
  {
    label: "对象路由配置：",
    value: "0"
  },
  {
    label: "字段路由配置：",
    value: "0"
  },
  {
    label: "存储服务状态：",
    value: "加载中..."
  },
  {
    label: "最后更新时间：",
    value: "加载中..."
  }
]);

// 加载存储节点列表
const loadStorageNodes = async () => {
  try {
    loading.value = true;
    const response = await listStorageNodes();
    storageNodes.value = response.nodes || [];
    
    // 更新总览数据
    overviewData.value[0].value = storageNodes.value.length.toString();
    console.log('存储节点列表加载成功:', storageNodes.value);
  } catch (error: any) {
    console.error('加载存储节点列表失败:', error);
    Message.error(error.message || '获取存储节点列表失败');
    storageNodes.value = [];
  } finally {
    loading.value = false;
  }
};

// 加载存储设备列表
const loadStorageDevices = async () => {
  try {
    loading.value = true;
    const response = await listStorageDevices();
    storageDevices.value = response.devices || [];
    
    // 更新总览数据
    overviewData.value[1].value = storageDevices.value.length.toString();
    console.log('存储设备列表加载成功:', storageDevices.value);
  } catch (error: any) {
    console.error('加载存储设备列表失败:', error);
    Message.error(error.message || '获取存储设备列表失败');
    storageDevices.value = [];
  } finally {
    loading.value = false;
  }
};

// 加载数据对象路由列表
const loadObjectRoutes = async (searchParams?: { dataset_id?: number; node_id?: number }) => {
  if (!currentProjectId.value) {
    console.warn('当前项目ID为空，无法获取数据对象路由列表');
    return;
  }

  try {
    loading.value = true;
    const requestParams = {
      project_id: currentProjectId.value,
      ...searchParams
    };
    const response = await listObjectRoutes(requestParams);
    objectRoutes.value = response.routes || [];

    // 更新总览数据
    overviewData.value[2].value = objectRoutes.value.length.toString();
    console.log('数据对象路由列表加载成功:', objectRoutes.value);
  } catch (error: any) {
    console.error('加载数据对象路由列表失败:', error);
    Message.error(error.message || '获取数据对象路由列表失败');
    objectRoutes.value = [];
  } finally {
    loading.value = false;
  }
};

// 加载数据字段路由列表
const loadFieldRoutes = async (searchParams?: { node_id?: number; field_id?: number; data_category?: string; device_id?: number }) => {
  if (!currentProjectId.value) {
    console.warn('当前项目ID为空，无法获取数据字段路由列表');
    return;
  }

  try {
    loading.value = true;
    const requestParams = {
      project_id: currentProjectId.value,
      ...searchParams
    };
    const response = await listFieldRoutes(requestParams);
    fieldRoutes.value = response.routes || [];

    // 更新总览数据
    overviewData.value[3].value = fieldRoutes.value.length.toString();
    console.log('数据字段路由列表加载成功:', fieldRoutes.value);
  } catch (error: any) {
    console.error('加载数据字段路由列表失败:', error);
    Message.error(error.message || '获取数据字段路由列表失败');
    fieldRoutes.value = [];
  } finally {
    loading.value = false;
  }
};

// 加载所有数据
const loadAllData = async () => {
  try {
    loading.value = true;
    
    // 并行加载所有数据
    await Promise.allSettled([
      loadStorageNodes(),
      loadStorageDevices(),
      loadObjectRoutes(),
      loadFieldRoutes()
    ]);
    
    // 更新状态和时间
    overviewData.value[4].value = "正常运行";
    overviewData.value[5].value = new Date().toLocaleString('zh-CN');
    
  } catch (error: any) {
    console.error('加载数据失败:', error);
    overviewData.value[4].value = "加载失败";
    Message.error('加载存储配置数据失败');
  } finally {
    loading.value = false;
  }
};

// 监听tab切换，为每个tab切换时重新加载对应数据
watch(activeTab, (newTab, oldTab) => {
  // 避免初始化时触发
  if (oldTab === undefined) return;

  switch (newTab) {
    case 'storage-node':
      console.log('切换到存储节点配置tab，重新加载数据');
      loadStorageNodes();
      break;
    case 'storage-device':
      console.log('切换到存储设备配置tab，重新加载数据');
      loadStorageDevices();
      break;
    case 'object-route':
      console.log('切换到数据对象-路由配置tab，重新加载数据');
      loadObjectRoutes();
      break;
    case 'field-route':
      console.log('切换到数据字段-路由配置tab，重新加载数据');
      loadFieldRoutes();
      break;
  }
});

onMounted(() => {
  // 初始化加载数据
  loadAllData();
});
</script>

<style lang="scss" scoped>
.margin-top {
  margin-top: $padding;
}

.overview-card {
  background: var(--color-success-light-1);
  border: 1px solid var(--color-success-light-3);
}
</style> 
