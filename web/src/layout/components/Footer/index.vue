<template>
  <a-layout-footer class="footer">
    <div class="footer_title" @click="onFooter">Copyright © 2025 Moo. All rights reserved.</div>
  </a-layout-footer>
</template>

<script setup lang="ts">
const onFooter = () => {
  window.open("", "_blank");
};
</script>

<style lang="scss" scoped>
.footer {
  box-sizing: border-box;
  display: flex;
  align-items: center;
  height: 30px;
  border-top: $border-1 solid $color-border-2;
}
.footer_title {
  margin: 0 auto;
  color: $color-text-4;
  text-align: center;
  cursor: pointer;
}
</style>
