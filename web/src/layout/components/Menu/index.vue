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
import { ref, computed, onMounted, watch } from "vue";
import { useRouter, useRoute } from "vue-router";
import { Message } from '@arco-design/web-vue';
import { listProjects, type Project } from '@/api/project';

const router = useRouter();
const route = useRoute();
const routerStore = useRoutesConfigStore();
const { currentRoute } = storeToRefs(routerStore);
const themeStore = useThemeConfig();
const { collapsed, isAccordion, layoutType, asideDark } = storeToRefs(themeStore);

// 调试信息开关
const showDebugInfo = ref(false);

// 项目列表状态
const projects = ref<Project[]>([]);
const loading = ref(false);

// 当前选中的项目ID（用于记住用户的选择）
const lastSelectedProjectId = ref<number | null>(null);

// 获取当前应该使用的项目ID
const getCurrentProjectId = (): number | null => {
  // 优先使用当前路由的项目ID
  const routeProjectId = route.params.projectId;
  if (routeProjectId) {
    const projectId = Number(routeProjectId);
    lastSelectedProjectId.value = projectId; // 记住这个选择
    return projectId;
  }
  
  // 如果当前路由没有项目ID，但之前记住了用户的选择，使用记住的项目ID
  if (lastSelectedProjectId.value && projects.value.find(p => p.id === lastSelectedProjectId.value)) {
    return lastSelectedProjectId.value;
  }
  
  // 如果没有记住的选择，但有项目列表，使用第一个项目
  if (projects.value.length > 0) {
    const firstProjectId = projects.value[0].id;
    lastSelectedProjectId.value = firstProjectId; // 记住这个选择
    return firstProjectId;
  }
  
  return null;
};

// 获取项目列表
const fetchProjects = async () => {
  if (loading.value) return;
  
  loading.value = true;
  try {
    projects.value = await listProjects();
    console.log('菜单组件 - 获取项目列表:', projects.value);
  } catch (error) {
    console.error('菜单组件 - 获取项目列表失败:', error);
  } finally {
    loading.value = false;
  }
};

// 监听路由变化，更新选中的项目ID
watch(() => route.params.projectId, (newProjectId) => {
  if (newProjectId) {
    lastSelectedProjectId.value = Number(newProjectId);
    console.log('菜单组件 - 更新选中项目ID:', lastSelectedProjectId.value);
  }
});

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
  
  // 处理项目管理菜单项（直接使用路由name）
  if (key === 'dataset' || key === 'field-management' || key === 'storage-config') {
    console.log('处理项目管理菜单项:', key);
    
    const projectId = getCurrentProjectId();
    console.log('获取到的项目ID:', projectId);
    
    if (projectId) {
      const targetPath = `/project/${projectId}/${key}`;
      console.log('目标路径:', targetPath);
      router.push(targetPath);
      return;
    } else {
      // 没有任何项目时，给出提示
      console.log('没有可用项目，显示提示信息');
      Message.warning('请先在顶部下拉框中选择一个项目');
      return;
    }
  }
  
  // 处理数据管理菜单项（直接使用路由name）
  if (key === 'data-overview' || key === 'data-sync' || key === 'data-object-list' || key === 'data-data-list') {
    console.log('处理数据管理菜单项:', key);

    const projectId = getCurrentProjectId();
    console.log('获取到的项目ID:', projectId);

    if (projectId) {
      let targetPath: string;
      if (key === 'data-overview') {
        targetPath = `/data-management/${projectId}/overview`;
      } else if (key === 'data-sync') {
        targetPath = `/data-management/${projectId}/sync`;
      } else if (key === 'data-object-list') {
        targetPath = `/data-management/${projectId}/object-list`;
      } else {
        targetPath = `/data-management/${projectId}/data-list`;
      }

      console.log('跳转数据管理页面:', targetPath);
      router.push(targetPath);
      return;
    } else {
      // 没有任何项目时，给出提示
      console.log('没有可用项目，显示提示信息');
      Message.warning('请先在顶部下拉框中选择一个项目');
      return;
    }
  }

  // 处理容器管理菜单项（直接使用路由name）
  const containerKeys = [
    'resource-monitor', 'service-status',
    'ssh-hosts', 'ssh-terminal', 'ssh-sessions'
  ];
  if (containerKeys.includes(key)) {
    console.log('处理容器管理菜单项:', key);
    const targetPath = `/container-management/${key}`;
    console.log('跳转容器管理页面:', targetPath);
    router.push(targetPath);
    return;
  }
  
  // 原有的路由处理逻辑
  const { findLinearArray } = useRoutingMethod();
  const find = findLinearArray(key);
  // 路由存在则存入并跳转，不存在则跳404
  if (find) {
    router.push(find.path);
  } else {
    router.push("/404");
  }
};

onMounted(() => {
  fetchProjects();
});
</script>

<style lang="scss" scoped></style>
