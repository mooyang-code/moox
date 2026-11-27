<template>
  <div class="menu_fold">
    <a-button size="mini" type="text" class="menu_fold_icon" id="system-collapsed" @click="onCollapsed">
      <template #icon>
        <icon-menu-fold :size="18" v-if="!isFolded" />
        <icon-menu-unfold :size="18" v-if="isFolded" />
      </template>
    </a-button>
  </div>

  <DrawerAside v-if="!isPc" />
</template>

<script setup lang="ts">
import { computed, watch } from "vue";
import DrawerAside from "@/layout/components/drawer-aside/index.vue";
import { storeToRefs } from "pinia";
import { useThemeConfig } from "@/store/modules/theme-config";
import { useDevicesSize } from "@/hooks/useDevicesSize";

const themeStore = useThemeConfig();
const { collapsed, drawerVisible } = storeToRefs(themeStore);
const { isPc } = useDevicesSize();

// 桌面端反映持久化的侧栏折叠态；窄屏/移动端反映临时导航抽屉是否收起
const isFolded = computed(() => (isPc.value ? collapsed.value : !drawerVisible.value));

// 桌面端只修改持久化 collapsed，移动端只切换临时 drawerVisible，互不污染
const onCollapsed = () => {
  if (isPc.value) {
    themeStore.setCollapsed(!collapsed.value);
  } else {
    themeStore.setDrawerVisible(!drawerVisible.value);
  }
};

// 跨断点切换仅收起移动端临时抽屉，绝不改写桌面端持久化 collapsed
watch(isPc, () => {
  themeStore.setDrawerVisible(false);
});
</script>

<style lang="scss" scoped>
.menu_fold {
  display: flex;
  align-items: center;
  justify-content: space-around;
  width: 32px;
  height: 32px;
  .menu_fold_icon {
    color: $color-text-1;
    border-radius: $radius-box-1;
  }
}
</style>
