<template>
  <div class="moox-page">
    <div class="container">
      <!-- 总体说明 -->
      <a-card :bordered="false" class="overview-card">
        <a-descriptions title="存储配置总览" :data="overviewData" :column="3" :align="{ label: 'right' }" />
        <a-alert type="info" style="margin-top: 16px;" :show-icon="false">
          <div>存储配置管理系统用于管理数据存储的各种配置，包括存储实体、存储设备以及数据路由配置。</div>
          <div>通过合理配置可以实现数据的高效存储和快速访问。</div>
        </a-alert>
      </a-card>

      <!-- Tab切换区域 -->
      <a-card class="margin-top" :bordered="false">
        <a-tabs :type="type" :size="size" v-model:active-key="activeTab">
          <a-tab-pane key="storage-entity" title="存储实体配置">
            <StorageEntityConfig />
          </a-tab-pane>
          <a-tab-pane key="storage-device" title="存储设备配置">
            <StorageDeviceConfig />
          </a-tab-pane>
          <a-tab-pane key="object-route" title="数据对象-路由配置">
            <ObjectRouteConfig />
          </a-tab-pane>
          <a-tab-pane key="field-route" title="数据字段-路由配置">
            <FieldRouteConfig />
          </a-tab-pane>
        </a-tabs>
      </a-card>
    </div>
  </div>
</template>

<script lang="ts" setup>
import { ref, onMounted } from 'vue';
import StorageEntityConfig from './components/storage-entity-config.vue';
import StorageDeviceConfig from './components/storage-device-config.vue';
import ObjectRouteConfig from './components/object-route-config.vue';
import FieldRouteConfig from './components/field-route-config.vue';

// Tab配置
const type = ref("rounded");
const size = ref("medium");
const activeTab = ref("storage-entity");

// 总览数据
const overviewData = ref([
  {
    label: "存储实体数量：",
    value: "2"
  },
  {
    label: "存储设备数量：",
    value: "3"
  },
  {
    label: "对象路由配置：",
    value: "5"
  },
  {
    label: "字段路由配置：",
    value: "8"
  },
  {
    label: "存储服务状态：",
    value: "正常运行"
  },
  {
    label: "最后更新时间：",
    value: "2025-03-20 15:30:00"
  }
]);

onMounted(() => {
  // 初始化数据
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