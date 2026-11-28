<template>
  <a-layout-content class="layout-main-content">
    <Tabs />
    <!-- 路由内容：跨路径导航时旧页面停留到新页面就绪，期间顶部有进度条；Suspense 的 fallback 只在新页面首次异步加载时出现 -->
    <router-view v-slot="{ Component, route }">
      <MainTransition>
        <Suspense>
          <template #default>
            <component :is="Component" :key="routeViewKey(route, viewSpaceGeneration)" v-if="refreshPage && !switchingSpace" />
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
</template>

<script setup lang="ts">
import { ref, watch } from "vue";
import { useRoute, useRouter } from "vue-router";
import { storeToRefs } from "pinia";
import { selectedSpaceGeneration } from "@/api/admin/space-header";
import Tabs from "@/layout/components/Tabs/index.vue";
import MainTransition from "@/components/main-transition/index.vue";
import { useSpaceStore } from "@/store/modules/space";
import { useThemeConfig } from "@/store/modules/theme-config";
import { routeViewKey, spaceSwitchNavigation } from "./route-view-key";

const themeStore = useThemeConfig();
const { refreshPage } = storeToRefs(themeStore);

const spaceStore = useSpaceStore();
const route = useRoute();
const router = useRouter();
const viewSpaceGeneration = ref(selectedSpaceGeneration());
// 空间内页面在切空间且需要清 query 或跳回列表时，先卸载旧页、等地址换好后再按新代次挂载一次，避免新页带旧参数或挂载两次。
const switchingSpace = ref(false);
let spaceSwitchToken = 0;

function nextSpaceGeneration(): number {
  const current = selectedSpaceGeneration();
  return current !== viewSpaceGeneration.value ? current : viewSpaceGeneration.value + 1;
}

// 用 flush: "sync" 保证在程序化跨空间跳转（如 setSelectedSpace 后立刻 router.push）时，判断发生在原路由上，不会误清目标路由的参数。
watch(
  () => spaceStore.selectedSpaceId,
  (next, previous) => {
    if (next === previous) return;
    const token = ++spaceSwitchToken;
    const nextGen = nextSpaceGeneration();
    // 只有真正的空间切换（A → B）才清 URL 参数或从详情跳回列表；首次空间初始化（无 → 有）保留深链接参数并按新代次重挂载。
    if (previous && next) {
      const target = spaceSwitchNavigation(route);
      if (target) {
        switchingSpace.value = true;
        viewSpaceGeneration.value = nextGen;
        void Promise.resolve(router.replace(target)).finally(() => {
          if (token === spaceSwitchToken) switchingSpace.value = false;
        });
        return;
      }
    }
    viewSpaceGeneration.value = nextGen;
    switchingSpace.value = false;
  },
  { flush: "sync" }
);
</script>

<style lang="scss" scoped>
.layout-main-content {
  position: relative;
  display: flex;
  flex-direction: column;
  width: 100%;
  height: 100%;
  min-width: 0;
  min-height: 0;
  overflow: hidden;
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
