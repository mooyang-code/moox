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

  <!-- 主机管理菜单项 -->
  <a-menu-item key="ssh-hosts">
    <template #icon>
      <MenuItemIcon icon="icon-menu" />
    </template>
    <span>{{ $t('menu.ssh-hosts') }}</span>
  </a-menu-item>

  <!-- SSH终端菜单项 -->
  <a-menu-item key="ssh-terminal">
    <template #icon>
      <MenuItemIcon icon="icon-menu" />
    </template>
    <span>{{ $t('menu.ssh-terminal') }}</span>
  </a-menu-item>

  <!-- 会话管理菜单项 -->
  <a-menu-item key="ssh-sessions">
    <template #icon>
      <MenuItemIcon icon="icon-menu" />
    </template>
    <span>{{ $t('menu.ssh-sessions') }}</span>
  </a-menu-item>
</template>

<script setup lang="ts">
import MenuItemIcon from './menu-item-icon.vue';
</script>

<style scoped>
</style>
