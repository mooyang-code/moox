<template>
  <div>
    <LangProvider>
      <LayoutDefaults />
    </LangProvider>
  </div>
</template>

<script setup lang="ts">
import { defineAsyncComponent } from "vue";
import LangProvider from "@/components/lang-provider/index.vue";

// 必须异步加载：布局里的组件会引用 router，而 router/route.ts 又静态引用本文件，同步导入会形成循环依赖
const LayoutDefaults = defineAsyncComponent(() => import("@/layout/layout-defaults/index.vue"));
</script>
