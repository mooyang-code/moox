<template>
  <div class="header_crumb">
    <ButtonCollapsed />
    <div class="project-selector">
      <a-select
        v-model="selectedProjectId"
        :options="projectOptions"
        @change="onProjectChange"
        placeholder="请选择项目"
        style="width: 220px; margin-left: 16px;"
        allow-search
        :loading="loading"
      >
        <template #empty>
          <div style="text-align: center; padding: 12px;">
            <div>暂无项目</div>
            <a-button type="text" size="small" @click="handleCreateProject" style="margin-top: 8px;">
              新建项目
            </a-button>
          </div>
        </template>
      </a-select>
    </div>
  </div>
</template>

<script setup lang="ts">
import { onMounted, computed, watch } from 'vue';
import { useRouter, useRoute } from 'vue-router';
import ButtonCollapsed from "@/layout/components/Header/components/button-collapsed/index.vue";
import { useProjectStore } from '@/store/modules/project';
import { storeToRefs } from 'pinia';
import type { Project } from '@/api/project';

const router = useRouter();
const route = useRoute();

// 使用项目状态管理
const projectStore = useProjectStore();
const { projects, selectedProjectId, loading } = storeToRefs(projectStore);

// 计算项目选项
const projectOptions = computed(() => {
  return projects.value.map((project: Project) => ({
    label: project.name_cn || project.name,
    value: project.id,
    disabled: false
  }));
});

// 项目切换事件
const onProjectChange = (projectId: string | number) => {
  console.log('切换项目:', projectId);
  
  // 更新全局状态
  projectStore.setSelectedProjectId(Number(projectId));
  
  // 获取当前路由信息，判断应该跳转到新项目的哪个页面
  const currentRouteName = route.name;
  const currentPath = route.path;
  
  console.log('当前路由名:', currentRouteName, '当前路径:', currentPath);
  
  let targetPath = '';
  
  // 根据当前路由类型决定跳转目标
  if (currentRouteName === 'dataset' || currentRouteName === 'field-management' || currentRouteName === 'storage-config') {
    // 如果当前在项目管理页面，跳转到新项目的相同页面
    targetPath = `/project/${projectId}/${currentRouteName}`;
  } else if (currentRouteName === 'data-overview' || currentRouteName === 'data-sync' || currentRouteName === 'data-object-list' || currentRouteName === 'data-data-list') {
    // 如果当前在数据管理页面，跳转到新项目的相同页面
    let pageType = '';
    if (currentRouteName === 'data-overview') {
      pageType = 'overview';
    } else if (currentRouteName === 'data-sync') {
      pageType = 'sync';
    } else if (currentRouteName === 'data-object-list') {
      pageType = 'object-list';
    } else {
      pageType = 'data-list';
    }
    targetPath = `/data-management/${projectId}/${pageType}`;
  } else if (currentRouteName === 'home') {
    // 如果当前在首页，保持在首页但会通过全局状态更新显示内容
    return;
  } else {
    // 其他情况默认跳转到数据集页面
    targetPath = `/project/${projectId}/dataset`;
  }
  
  console.log('目标路径:', targetPath);
  router.push(targetPath);
};

// 新建项目
const handleCreateProject = () => {
  router.push('/project/create-project');
};

// 监听路由变化，更新选中的项目
watch(() => route.params.projectId, (newProjectId) => {
  if (newProjectId) {
    projectStore.setSelectedProjectId(Number(newProjectId));
  }
});

onMounted(async () => {
  // 获取项目列表
  await projectStore.fetchProjects();
  
  // 如果当前路由有项目ID，同步到全局状态
  if (route.params.projectId) {
    projectStore.setSelectedProjectId(Number(route.params.projectId));
  }
});
</script>

<style lang="scss" scoped>
.header_crumb {
  display: flex;
  align-items: center;
  width: 100%;
}

.project-selector {
  display: flex;
  align-items: center;
}
</style>
