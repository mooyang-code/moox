<template>
  <a-menu
    :breakpoint="layoutType != 'layoutHead' ? 'xl' : undefined"
    :mode="'vertical'"
    :theme="asideDark ? 'dark' : 'light'"
    :collapsed="collapsed"
    :auto-scroll-into-view="true"
    :accordion="isAccordion"
    v-model:open-keys="openKeys"
    :selected-keys="selectedKeys"
    @menu-item-click="onMenuItem"
  >
    <MenuItem :route-tree="props.routeTree" />
  </a-menu>
</template>

<script setup lang="ts">
import MenuItem from "@/layout/components/Menu/menu-item.vue";
import { storeToRefs } from "pinia";
import { useThemeConfig } from "@/store/modules/theme-config";
import { useRoutingMethod } from "@/hooks/useRoutingMethod";
import { computed, ref, watch } from "vue";
import { useRoute, useRouter } from "vue-router";
import { resolveOpenKeys, resolveSelectedKeys } from "@/layout/components/Menu/menu-selected";

const router = useRouter();
const route = useRoute();
const themeStore = useThemeConfig();
const { collapsed, isAccordion, layoutType, asideDark } = storeToRefs(themeStore);

// 计算菜单选中的keys
const selectedKeys = computed(() => resolveSelectedKeys(route));

interface Props {
  routeTree: Menu.MenuOptions[];
}
// props的数据类型
// type类型参考：https://cn.vuejs.org/guide/typescript/composition-api.html#typing-component-props
const props = withDefaults(defineProps<Props>(), {
  routeTree: () => []
});

// 展开的父菜单。Arco 的 auto-open-selected 只在菜单挂载时展开一次，之后从页面内链接、标签页、浏览器前进后退
// 打开别的分组下的子菜单页面时父菜单仍是收起的；这里每次路由变化（以及菜单树加载完）都把选中项的整条父链展开。
// 手风琴模式只保留这一条父链；用户手动收起的分组在路由再次变化前保持收起。
const openKeys = ref<string[]>([]);
watch(
  [selectedKeys, () => props.routeTree],
  () => {
    const parents = resolveOpenKeys(props.routeTree, selectedKeys.value);
    if (!parents.length) return;
    openKeys.value = isAccordion.value ? parents : [...new Set([...openKeys.value, ...parents])];
  },
  { immediate: true }
);

/**
 * @description 菜单点击事件
 * @param {String} key
 */
const onMenuItem = (key: string) => {
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
