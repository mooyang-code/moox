<template>
  <a-drawer :width="340" :visible="props.systemOpen" @ok="handleCancel" @cancel="handleCancel" unmount-on-close>
    <template #title> 布局设置 </template>
    <div>
      <div class="title">界面设置</div>
      <div class="flex-row">
        <div>菜单折叠</div>
        <a-switch v-model="collapsed" />
      </div>
      <div class="flex-row">
        <div>菜单手风琴</div>
        <a-switch v-model="isAccordion" />
      </div>
      <div class="flex-row">
        <div>标签栏</div>
        <a-switch v-model="isTabs" @change="tabsChange" />
      </div>
    </div>
  </a-drawer>
</template>

<script setup lang="ts">
import { storeToRefs } from "pinia";
import { useRoutesConfigStore } from "@/store/modules/route-config";
import { useThemeConfig } from "@/store/modules/theme-config";
import { currentlyRoute } from "@/router/route-output";
const themeStore = useThemeConfig();
const routerStore = useRoutesConfigStore();
const { collapsed, isAccordion, isTabs } = storeToRefs(themeStore);
const { tabsList } = storeToRefs(routerStore);
const route = useRoute();
const props = defineProps({
  systemOpen: {
    type: Boolean,
    default: false
  }
});

/*
  是否关闭tabs栏
  如果关闭，那么所有tabs全部取消
  如果开启，那么添加当前路由到tabs
*/
const tabsChange = (value: string | number | boolean) => {
  if (!value) {
    tabsList.value = [];
  } else {
    currentlyRoute(route);
  }
};

const emits = defineEmits(["systemCancel"]);
const handleCancel = () => {
  emits("systemCancel");
};
</script>

<style lang="scss" scoped>
.title {
  margin-bottom: $margin;
  font-size: $font-size-title-1;
  font-weight: bold;
  color: $color-text-1;
}
.flex-row {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: $margin;
}
</style>
