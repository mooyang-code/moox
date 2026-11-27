<template>
  <div class="logo_head">
    <div class="logo_box" :class="isCollapsed && 'padding-unset'">
      <SvgIcon name="moox-logo" :size="30" />
      <span class="logo_title" v-if="!isCollapsed">{{ title }}</span>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed } from "vue";
import { storeToRefs } from "pinia";
import { useThemeConfig } from "@/store/modules/theme-config";

const props = withDefaults(
  defineProps<{
    /** 显式覆盖折叠态（例如移动端抽屉固定以展开态渲染，不读取桌面端 collapsed） */
    collapsed?: boolean;
  }>(),
  { collapsed: undefined }
);

const themeStore = useThemeConfig();
const { collapsed: storeCollapsed } = storeToRefs(themeStore);
const isCollapsed = computed(() => props.collapsed ?? storeCollapsed.value);

// 全局title
const title = import.meta.env.VITE_GLOB_APP_TITLE + " 量化平台";
</script>

<style lang="scss" scoped>
// 头部
.logo_head {
  box-sizing: border-box;
  display: flex;
  align-items: center;
  justify-content: space-around;
  height: 60px;
  border-right: $border-1 solid $color-border-2;
  .logo_box {
    display: flex;
    column-gap: $padding;
    align-items: center;
    width: 100%;
    padding: 0 $padding;
    overflow: hidden;
  }

  // 折叠时去掉 padding，logo 居中
  .padding-unset {
    justify-content: space-around;
    padding: unset;
  }
  .logo_title {
    box-sizing: border-box;
    max-width: 140px;
    overflow: hidden;
    font-size: $font-size-title-2;
    font-weight: bold;
    text-align: left;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
}
</style>
