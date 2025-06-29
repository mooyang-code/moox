<template>
  <!-- 容器管理子菜单 -->
  <a-sub-menu key="container-list">
    <template #icon>
      <MenuItemIcon icon="icon-menu" />
    </template>
    <template #title>{{ $t('menu.container-list') }}</template>
    
    <a-menu-item key="ssh-terminal">
      <template #icon>
        <MenuItemIcon icon="icon-menu" />
      </template>
      <span>{{ $t('menu.ssh-terminal') }}</span>
    </a-menu-item>
    
    <a-menu-item key="file-management">
      <template #icon>
        <MenuItemIcon icon="icon-menu" />
      </template>
      <span>{{ $t('menu.file-management') }}</span>
    </a-menu-item>
  </a-sub-menu>
  
  <a-sub-menu key="monitor-view">
    <template #icon>
      <MenuItemIcon icon="icon-menu" />
    </template>
    <template #title>监控视图</template>
    
    <a-menu-item key="resource-monitor">
      <template #icon>
        <MenuItemIcon icon="icon-menu" />
      </template>
      <span>{{ $t('menu.resource-monitor') }}</span>
    </a-menu-item>
    
    <a-menu-item key="service-status">
      <template #icon>
        <MenuItemIcon icon="icon-menu" />
      </template>
      <span>{{ $t('menu.service-status') }}</span>
    </a-menu-item>
  </a-sub-menu>
</template>

<script setup lang="ts">
import MenuItemIcon from './menu-item-icon.vue';
</script>

<style scoped>
</style>
