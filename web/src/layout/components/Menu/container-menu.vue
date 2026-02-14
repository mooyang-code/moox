<template>
  <!-- 容器列表菜单项 -->
  <a-menu-item key="container-list">
    <template #icon>
      <MenuItemIcon icon="icon-menu" />
    </template>
    <span>{{ $t('menu.container-list') }}</span>
  </a-menu-item>

  <!-- 容器监控菜单项 -->
  <a-menu-item key="resource-monitor">
    <template #icon>
      <MenuItemIcon icon="icon-menu" />
    </template>
    <span>{{ $t('menu.resource-monitor') }}</span>
  </a-menu-item>
</template>

<script setup lang="ts">
import MenuItemIcon from './menu-item-icon.vue';
</script>

<style scoped>
</style>
