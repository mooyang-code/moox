<template>
  <a-watermark :content="watermark" v-bind="watermarkConfig">
    <a-layout-content class="layout-main-content">
      <Tabs v-if="isTabs" />
      <!-- 全局路由加载状态 -->
      <div v-if="loadingStore.routeLoading" class="global-loading-container">
        <a-spin :size="32" tip="页面切换中..." />
      </div>

      <!-- 路由内容 -->
      <router-view v-else v-slot="{ Component, route }">
        <MainTransition>
          <keep-alive :include="cacheRoutes" :exclude="excludeRoutes" @vue:updated="handleKeepAliveUpdate">
            <Suspense>
              <template #default>
                <component :is="Component" :key="route.fullPath" v-if="refreshPage" />
              </template>
              <template #fallback>
                <div class="loading-container">
                  <a-spin :size="32" tip="页面加载中..." />
                </div>
              </template>
            </Suspense>
          </keep-alive>
        </MainTransition>
      </router-view>
    </a-layout-content>
  </a-watermark>
</template>

<script setup lang="ts">
import { ref, computed, watch } from "vue";
import Tabs from "@/layout/components/Tabs/index.vue";
import MainTransition from "@/components/main-transition/index.vue";
import { storeToRefs } from "pinia";
import { useThemeConfig } from "@/store/modules/theme-config";
import { useRoutesConfigStore } from "@/store/modules/route-config";
import { useLoadingStore } from "@/store/modules/loading";
const themeStore = useThemeConfig();
let { refreshPage, isTabs, watermark, watermarkStyle, watermarkRotate, watermarkGap } = storeToRefs(themeStore);
const routerStore = useRoutesConfigStore();
const { cacheRoutes } = storeToRefs(routerStore);
const loadingStore = useLoadingStore();

  // 排除不需要缓存的路由组件名
  const excludeRoutes = ref(['CreateProject', 'StepForm']);
  
  // 清理特定组件的DOM残留
  const cleanupComponentDOM = (componentName: string) => {
    if (componentName === 'CreateProject') {
      // 清理新建项目组件可能的DOM残留
      setTimeout(() => {
        const elementsToRemove = document.querySelectorAll('[data-v-152e326b]');
        elementsToRemove.forEach(element => {
          console.log('Main组件清理残留DOM元素:', element);
          element.remove();
        });
      }, 50);
    }
  };
  
  // 处理keep-alive组件更新
  const handleKeepAliveUpdate = () => {
    // 检查是否有被排除的组件需要清理
    excludeRoutes.value.forEach(componentName => {
      cleanupComponentDOM(componentName);
    });
  };
  
// 水印配置
const watermarkConfig = computed(() => {
  return {
    font: watermarkStyle.value,
    rotate: watermarkRotate.value,
    gap: watermarkGap.value
  };
});

watch(watermarkConfig, newv => {
  console.log(newv);
});
</script>

<style lang="scss" scoped>
.layout-main-content {
  display: flex;
  flex-direction: column;
  height: 100%;
}

.loading-container,
.global-loading-container {
  display: flex;
  justify-content: center;
  align-items: center;
  height: 200px;
  width: 100%;
}

.global-loading-container {
  height: 100%;
  min-height: 400px;
}

// 修改左侧滚动条宽度-主要针对main窗口内的滚动条
:deep(.arco-scrollbar-thumb-direction-vertical .arco-scrollbar-thumb-bar) {
  width: 4px;
  margin-left: 8px;
}
</style>
