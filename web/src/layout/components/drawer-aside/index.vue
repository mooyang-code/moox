<template>
  <a-drawer
    :width="220"
    class="drawer-aside"
    placement="left"
    :header="false"
    :footer="false"
    :visible="drawerVisible"
    @cancel="handleCancel"
    unmount-on-close
  >
    <div class="aside">
      <Logo :collapsed="false" />
      <a-layout-sider class="layout_side" :width="220">
        <a-scrollbar style="height: 100%; overflow: auto" outer-class="scrollbar"><Menu :collapsed="false" /></a-scrollbar>
      </a-layout-sider>
    </div>
  </a-drawer>
</template>

<script setup lang="ts">
import Logo from "@/layout/components/Logo/index.vue";
import Menu from "@/layout/components/Menu/index.vue";
import { storeToRefs } from "pinia";
import { useThemeConfig } from "@/store/modules/theme-config";
const themeStore = useThemeConfig();
const { drawerVisible } = storeToRefs(themeStore);

const handleCancel = () => {
  themeStore.setDrawerVisible(false);
};
</script>

<style lang="scss" scoped>
.aside {
  display: flex;
  flex-direction: column;
  height: 100vh;
}
.layout_side {
  flex: 1;
  overflow: hidden;
  .scrollbar {
    height: 100%;
  }
}

// 修改左侧滚动条宽度
:deep(.arco-scrollbar-thumb-direction-vertical .arco-scrollbar-thumb-bar) {
  width: 4px;
  margin-left: var(--moox-space-2);
}

// 去掉右侧阴影并替换为边线
:deep(.arco-layout-sider-light) {
  border-right: $border-1 solid $color-border-2;
  box-shadow: unset;
}

// 去掉sider背景
.arco-layout-sider {
  background: unset;
}
</style>
<style lang="scss">
.drawer-aside {
  .arco-drawer .arco-drawer-body {
    padding: 0;
    overflow: hidden;
  }
}
</style>
