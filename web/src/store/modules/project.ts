import { defineStore } from "pinia";
import persistedstateConfig from "@/store/config/index";
import { listProjects, type Project } from '@/api/project';

/**
 * 项目状态管理
 * @methods setSelectedProjectId 设置选中的项目ID
 * @methods getSelectedProject 获取当前选中的项目信息
 * @methods fetchProjects 获取项目列表
 */
const projectStore = () => {
  // 项目列表
  const projects = ref<Project[]>([]);
  // 当前选中的项目ID
  const selectedProjectId = ref<number | null>(null);
  // 加载状态
  const loading = ref(false);

  // 获取当前选中的项目
  const selectedProject = computed(() => {
    if (!selectedProjectId.value || !projects.value.length) return null;
    return projects.value.find(project => project.id === selectedProjectId.value) || null;
  });

  /**
   * 设置选中的项目ID
   * @param {number | null} projectId 项目ID
   */
  function setSelectedProjectId(projectId: number | null) {
    selectedProjectId.value = projectId;
  }

  /**
   * 获取项目列表
   */
  async function fetchProjects() {
    if (loading.value) return;
    
    loading.value = true;
    try {
      projects.value = await listProjects();
      console.log('项目store - 获取项目列表:', projects.value);
      
      // 如果还没有选中项目且有项目列表，自动选中第一个
      if (!selectedProjectId.value && projects.value.length > 0) {
        selectedProjectId.value = projects.value[0].id;
      }
    } catch (error) {
      console.error('项目store - 获取项目列表失败:', error);
    } finally {
      loading.value = false;
    }
  }

  /**
   * 清除选中的项目
   */
  function clearSelectedProject() {
    selectedProjectId.value = null;
  }

  return { 
    projects, 
    selectedProjectId, 
    selectedProject,
    loading,
    setSelectedProjectId,
    fetchProjects,
    clearSelectedProject
  };
};

/**
 * 项目信息状态管理
 */
export const useProjectStore = defineStore(
  "projectStore",
  projectStore,
  persistedstateConfig("projectStore", ["selectedProjectId"])
); 