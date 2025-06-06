<template>
  <!-- 固定的数据管理子菜单，始终显示 -->
  <a-menu-item key="data-overview">
    <template #icon>
      <MenuItemIcon icon="icon-menu" />
    </template>
    <span>{{ $t('menu.data-overview') }}</span>
  </a-menu-item>
  <a-menu-item key="data-sync">
    <template #icon>
      <MenuItemIcon icon="icon-menu" />
    </template>
    <span>{{ $t('menu.data-sync') }}</span>
  </a-menu-item>
  <a-menu-item key="data-object-list">
    <template #icon>
      <MenuItemIcon icon="icon-menu" />
    </template>
    <span>{{ $t('menu.object-list') }}</span>
  </a-menu-item>
  <a-menu-item key="data-data-list">
    <template #icon>
      <MenuItemIcon icon="icon-menu" />
    </template>
    <span>{{ $t('menu.data-list') }}</span>
  </a-menu-item>
</template>

<script setup lang="ts">
import { ref, onMounted, computed } from 'vue';
import { useRoute } from 'vue-router';
import MenuItemIcon from './menu-item-icon.vue';
import { listProjects, type Project } from '@/api/project';

const route = useRoute();
const projects = ref<Project[]>([]);
const loading = ref(false);
let fetchPromise: Promise<Project[]> | null = null;

// 获取当前路由的项目ID
const currentProjectId = computed(() => {
  return route.params.projectId as string;
});

// 获取当前项目
const currentProject = computed(() => {
  if (!currentProjectId.value || !projects.value.length) return null;
  return projects.value.find(project => project.id.toString() === currentProjectId.value);
});

// 获取项目列表
const fetchProjects = async () => {
  // 如果已经在加载中，返回之前的Promise
  if (loading.value) {
    return fetchPromise;
  }
  
  // 如果有一个正在进行的请求，返回该请求的Promise
  if (fetchPromise) {
    return fetchPromise;
  }

  loading.value = true;
  try {
    fetchPromise = listProjects();
    projects.value = await fetchPromise;
    console.log('数据管理菜单 - 项目列表数据:', projects.value);
    console.log('数据管理菜单 - 当前项目:', currentProject.value);
  } catch (error) {
    console.error('获取数据管理项目列表失败:', error);
  } finally {
    loading.value = false;
    fetchPromise = null;
  }

  return projects.value;
};

// 暴露给父组件调用
defineExpose({
  fetchProjects,
  currentProject,
  projects
});

onMounted(() => {
  fetchProjects();
});
</script>

<style scoped>
.menu-dot {
  width: 4px;
  height: 4px;
  border-radius: 50%;
  background-color: var(--color-text-3);
  margin: 0 auto;
}
</style> 