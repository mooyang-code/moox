<template>
  <Teleport v-if="isTabs && isActive" defer to="#page-tab-actions">
    <div class="page-tab-actions page-tab-actions--teleported">
      <slot />
    </div>
  </Teleport>
  <div v-else-if="!isTabs" class="page-tab-actions page-tab-actions--inline">
    <slot />
  </div>
</template>

<script setup lang="ts">
import { storeToRefs } from "pinia";
import { onActivated, onDeactivated, ref } from "vue";
import { useThemeConfig } from "@/store/modules/theme-config";

defineOptions({ name: "PageTabActions" });

const themeStore = useThemeConfig();
const { isTabs } = storeToRefs(themeStore);
const isActive = ref(true);

onActivated(() => {
  isActive.value = true;
});

onDeactivated(() => {
  isActive.value = false;
});
</script>

<style scoped>
.page-tab-actions {
  display: flex;
  align-items: center;
  min-width: 0;
}

.page-tab-actions--inline {
  justify-content: flex-end;
  margin-bottom: 14px;
}
</style>
