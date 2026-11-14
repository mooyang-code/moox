<template>
  <div>
    <router-view />
  </div>
</template>

<script setup lang="ts">
import { useThemeMethods } from "@/hooks/useThemeMethods";

// 按保存的偏好应用明暗主题
useThemeMethods().setDarkMode();
</script>

<style lang="scss" scoped></style>
