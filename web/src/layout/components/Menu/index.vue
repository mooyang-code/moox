<template>
  <a-menu
    :breakpoint="props.collapsed === undefined ? 'xl' : undefined"
    :mode="'vertical'"
    :collapsed="isCollapsed"
    :auto-scroll-into-view="true"
    accordion
    v-model:open-keys="openKeys"
    :selected-keys="selectedKeys"
    @menu-item-click="onMenuItem"
  >
    <MenuItem :items="menuTree" />
  </a-menu>
</template>

<script setup lang="ts">
import MenuItem from "@/layout/components/Menu/menu-item.vue";
import { storeToRefs } from "pinia";
import { useThemeConfig } from "@/store/modules/theme-config";
import { computed, ref, watch } from "vue";
import { useRoute, useRouter } from "vue-router";
import { layoutRoutes } from "@/router/route";
import { buildMenuTree, resolveOpenKeys, resolveSelectedKeys } from "@/layout/components/Menu/menu-selected";

const props = withDefaults(
  defineProps<{
    /** 显式覆盖折叠态（例如移动端抽屉固定以展开态渲染，不读取桌面端 collapsed） */
    collapsed?: boolean;
  }>(),
  { collapsed: undefined }
);

const router = useRouter();
const route = useRoute();
const themeStore = useThemeConfig();
const { collapsed: storeCollapsed } = storeToRefs(themeStore);
const isCollapsed = computed(() => props.collapsed ?? storeCollapsed.value);

// 计算菜单选中的keys
const selectedKeys = computed(() => resolveSelectedKeys(route));

// 菜单由路由表生成，不另存一份
const menuTree = buildMenuTree(layoutRoutes);

// 展开的父菜单。Arco 的 auto-open-selected 只在菜单挂载时展开一次，之后从页面内链接、标签页、浏览器前进后退
// 打开别的分组下的子菜单页面时父菜单仍是收起的；这里每次路由变化都把选中项的整条父链展开。
// 菜单是手风琴模式，只保留这一条父链；用户手动收起的分组在路由再次变化前保持收起。
const openKeys = ref<string[]>([]);
watch(
  selectedKeys,
  () => {
    const parents = resolveOpenKeys(menuTree, selectedKeys.value);
    if (!parents.length) return;
    openKeys.value = parents;
  },
  { immediate: true }
);

/**
 * @description 菜单点击事件
 * @param {String} key
 */
const onMenuItem = (key: string) => {
  void router.push({ name: key });
};
</script>

<style lang="scss" scoped></style>
