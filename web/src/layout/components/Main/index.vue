<template>
  <a-watermark :content="watermark" v-bind="watermarkConfig">
    <a-layout-content class="layout-main-content">
      <Tabs v-if="isTabs" />
      <!-- 路由内容：跨路径导航时旧页面停留到新页面就绪，期间顶部有进度条；Suspense 的 fallback 只在新页面首次异步加载时出现 -->
      <router-view v-slot="{ Component, route }">
        <MainTransition>
          <Suspense>
            <template #default>
              <component :is="Component" :key="routeViewKey(route)" v-if="refreshPage" />
            </template>
            <template #fallback>
              <div class="loading-container">
                <a-spin :size="32" tip="页面加载中..." />
              </div>
            </template>
          </Suspense>
        </MainTransition>
      </router-view>
    </a-layout-content>
  </a-watermark>
</template>

<script setup lang="ts">
import { computed } from "vue";
import Tabs from "@/layout/components/Tabs/index.vue";
import { routeViewKey } from "./route-view-key";
import MainTransition from "@/components/main-transition/index.vue";
import { storeToRefs } from "pinia";
import { useThemeConfig } from "@/store/modules/theme-config";
const themeStore = useThemeConfig();
let { refreshPage, isTabs, watermark, watermarkStyle, watermarkRotate, watermarkGap } = storeToRefs(themeStore);

// 水印配置
const watermarkConfig = computed(() => {
  return {
    font: watermarkStyle.value,
    rotate: watermarkRotate.value,
    gap: watermarkGap.value
  };
});
</script>

<style lang="scss" scoped>
.layout-main-content {
  display: flex;
  flex-direction: column;
  width: 100%;
  height: 100%;
  min-width: 0;
}

.loading-container {
  display: flex;
  justify-content: center;
  align-items: center;
  height: 200px;
  width: 100%;
}

// 修改左侧滚动条宽度-主要针对main窗口内的滚动条
:deep(.arco-scrollbar-thumb-direction-vertical .arco-scrollbar-thumb-bar) {
  width: 4px;
  margin-left: var(--moox-space-2);
}
</style>
