<template>
  <template v-for="item in props.items" :key="item.name">
    <a-sub-menu v-if="item.children.length" :key="item.name">
      <template #icon v-if="item.svgIcon">
        <MenuItemIcon :svg-icon="item.svgIcon" />
      </template>
      <template #title>{{ item.title }}</template>
      <MenuItem :items="item.children" />
    </a-sub-menu>
    <a-menu-item v-else :key="item.name">
      <template #icon v-if="item.svgIcon">
        <MenuItemIcon :svg-icon="item.svgIcon" />
      </template>
      <span>{{ item.title }}</span>
    </a-menu-item>
  </template>
</template>

<script setup lang="ts">
import MenuItem from "@/layout/components/Menu/menu-item.vue";
import MenuItemIcon from "@/layout/components/Menu/menu-item-icon.vue";
import type { MenuNode } from "@/layout/components/Menu/menu-selected";

defineOptions({ name: "MenuItem", inheritAttrs: false });

const props = defineProps<{ items: MenuNode[] }>();
</script>
