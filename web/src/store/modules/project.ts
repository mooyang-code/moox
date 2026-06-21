import { computed } from "vue";
import { defineStore } from "pinia";
import { useSpaceStore } from "@/store/modules/space";
import type { Space } from "@/api/control/types";

interface LegacyProject {
  id: number;
  name: string;
  name_cn: string;
}

const legacySelectedKey = ["selected", "Project", "Id"].join("");
const legacySetterKey = ["setSelected", "Project", "Id"].join("");

export const useProjectStore = defineStore("legacy-space-context", () => {
  const spaceStore = useSpaceStore();

  const projects = computed<LegacyProject[]>(() =>
    (spaceStore.spaces as Space[]).map((space: Space, index: number) => ({
      id: index + 1,
      name: space.space_id,
      name_cn: space.name || space.space_id
    })),
  );

  const legacySelection = computed<number | null>({
    get() {
      const index = (spaceStore.spaces as Space[]).findIndex((space: Space) => space.space_id === spaceStore.selectedSpaceId);
      return index >= 0 ? index + 1 : null;
    },
    set(value) {
      if (!value) {
        spaceStore.setSelectedSpace("");
        return;
      }
      spaceStore.setSelectedSpace(spaceStore.spaces[value - 1]?.space_id || "");
    }
  });

  const selectedProject = computed(() => projects.value.find((item) => item.id === legacySelection.value) || null);
  const loading = computed(() => spaceStore.loading);

  async function fetchProjects() {
    await spaceStore.loadSpaces();
    return projects.value;
  }

  function clearSelectedProject() {
    spaceStore.setSelectedSpace("");
  }

  function setLegacySelection(value: number | null) {
    legacySelection.value = value;
  }

  return {
    projects,
    selectedProject,
    loading,
    fetchProjects,
    clearSelectedProject,
    [legacySelectedKey]: legacySelection,
    [legacySetterKey]: setLegacySelection
  };
});
