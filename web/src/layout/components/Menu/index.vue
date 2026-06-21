<template>
  <a-menu
    :breakpoint="layoutType != 'layoutHead' ? 'xl' : undefined"
    :mode="'vertical'"
    :theme="asideDark ? 'dark' : 'light'"
    :collapsed="collapsed"
    :auto-scroll-into-view="true"
    :auto-open-selected="true"
    :accordion="isAccordion"
    :selected-keys="selectedKeys"
    @menu-item-click="onMenuItem"
  >
    <MenuItem :route-tree="props.routeTree" />
  </a-menu>
  <!-- 简化的调试信息 -->
  <div v-if="showDebugInfo" style="position: fixed; top: 10px; right: 10px; background: rgba(0,0,0,0.8); color: white; padding: 10px; border-radius: 4px; font-size: 12px; z-index: 9999;">
    <div>当前路由名: {{ currentRoute.name }}</div>
    <div>选中菜单Keys: {{ JSON.stringify(selectedKeys) }}</div>
  </div>
</template>

<script setup lang="ts">
import MenuItem from "@/layout/components/Menu/menu-item.vue";
import { storeToRefs } from "pinia";
import { useThemeConfig } from "@/store/modules/theme-config";
import { useRoutesConfigStore } from "@/store/modules/route-config";
import { useRoutingMethod } from "@/hooks/useRoutingMethod";
import { ref, computed } from "vue";
import { useRouter } from "vue-router";

const router = useRouter();
const routerStore = useRoutesConfigStore();
const { currentRoute } = storeToRefs(routerStore);
const themeStore = useThemeConfig();
const { collapsed, isAccordion, layoutType, asideDark } = storeToRefs(themeStore);

// 调试信息开关
const showDebugInfo = ref(false);

// 计算菜单选中的keys
const selectedKeys = computed(() => {
  const keys = [currentRoute.value.name];
  console.log('计算选中keys:', keys, '当前路由:', currentRoute.value);
  return keys;
});

// 按 Ctrl+D 切换调试信息显示
if (typeof window !== 'undefined') {
  window.addEventListener('keydown', (e) => {
    if (e.ctrlKey && e.key === 'd') {
      e.preventDefault();
      showDebugInfo.value = !showDebugInfo.value;
    }
  });
}

interface Props {
  routeTree: Menu.MenuOptions[];
}
// props的数据类型
// type类型参考：https://cn.vuejs.org/guide/typescript/composition-api.html#typing-component-props
const props = withDefaults(defineProps<Props>(), {
  routeTree: () => []
});

/**
 * @description 菜单点击事件
 * @param {String} key
 */
const onMenuItem = (key: string) => {
  console.log('菜单点击:', key);
  console.log('当前路由名:', currentRoute.value.name);

  const { findLinearArray } = useRoutingMethod();
  const find = findLinearArray(key);
  // 路由存在则存入并跳转，不存在则跳404
  if (find) {
    router.push(find.path);
  } else {
    router.push("/404");
  }
};
</script>

<style lang="scss" scoped></style>
