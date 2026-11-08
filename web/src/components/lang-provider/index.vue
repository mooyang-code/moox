<template>
  <div>
    <a-config-provider :global="true" :locale="zhCN">
      <slot></slot>
    </a-config-provider>
  </div>
</template>

<script setup lang="ts">
// 界面只有中文，组件库文案固定用中文语言包
import zhCN from "@arco-design/web-vue/es/locale/lang/zh-cn";
</script>
