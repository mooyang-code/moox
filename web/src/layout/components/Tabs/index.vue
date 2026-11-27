<template>
  <div class="tabs">
    <a-tabs
      :editable="true"
      :hide-content="true"
      :active-key="currentRoute?.name"
      size="medium"
      type="line"
      @tab-click="onTabs"
      @delete="onDelete"
    >
      <a-tab-pane v-for="item of tabsList" :key="item.name" :title="item.meta?.title" :closable="!item.meta?.affix" />
    </a-tabs>
    <div class="tabs_setting">
      <a-space>
        <a-tooltip content="刷新" position="bottom" mini>
          <span ref="refreshRef" id="system-tabs-refresh" :class="rotateOpen && 'refresh'">
            <icon-refresh :size="18" @click="refresh" />
          </span>
        </a-tooltip>
        <a-dropdown trigger="hover" :popup-max-height="false">
          <div class="setting" id="system-tabs-setting"><icon-apps :size="18" /></div>
          <template #content>
            <a-doption @click="closeCurrent">
              <template #icon><icon-close /></template>
              <template #default>关闭当前</template>
            </a-doption>
            <a-doption @click="closeSides('left')">
              <template #icon><icon-left /></template>
              <template #default>关闭左侧</template>
            </a-doption>
            <a-doption @click="closeSides('right')">
              <template #icon><icon-right /></template>
              <template #default>关闭右侧</template>
            </a-doption>
            <a-doption @click="closeOther('other')">
              <template #icon><icon-close-circle /></template>
              <template #default>关闭其它</template>
            </a-doption>
            <a-doption @click="closeOther('all')">
              <template #icon><icon-folder-delete /></template>
              <template #default>关闭全部</template>
            </a-doption>
          </template>
        </a-dropdown>
      </a-space>
    </div>
  </div>
</template>

<script setup lang="ts">
import { nextTick, ref } from "vue";
import { useRouter } from "vue-router";
import { storeToRefs } from "pinia";
import { useRoutesConfigStore } from "@/store/modules/route-config";
import { useRoutingMethod } from "@/hooks/useRoutingMethod";
import { useThemeConfig } from "@/store/modules/theme-config";
const router = useRouter();
const routerStore = useRoutesConfigStore();
const { tabsList, currentRoute } = storeToRefs(routerStore);

// 统一处理跳转
const routerPush = (route: any) => {
  const { isDynamicRoute } = useRoutingMethod();
  // 区分动态路由和普通路由
  if (isDynamicRoute(route.path)) {
    router.push({ name: route.name, params: route.params });
  } else {
    router.push({ path: route.path, query: route.query });
  }
};

// 点击标签页，如果标签页存在，则跳转
const onTabs = (key: string) => {
  const { findTagsList } = useRoutingMethod();
  const find = findTagsList(key);
  if (find == undefined) return;
  routerPush(find);
};

// 删除当前标签页并跳转到最后一个标签页
const onDelete = (key: string) => {
  if (!routerStore.removeTabsList(key)) return;
  if (tabsList.value.length === 0) return;
  if (currentRoute.value?.name !== key) return;
  const last = tabsList.value.at(-1);
  if (last) routerPush(last);
};

// 刷新当前页
const rotateOpen = ref(false);
const refresh = () => {
  rotateOpen.value = true;
  setTimeout(() => {
    rotateOpen.value = false;
  }, 500);
  const themeStore = useThemeConfig();
  // 先卸载再挂载当前页，重建它
  themeStore.setRefreshPage(false);
  nextTick(() => themeStore.setRefreshPage(true));
};

// 关闭当前
const closeCurrent = () => {
  if (!currentRoute.value?.name || currentRoute.value.meta?.affix) return;
  onDelete(currentRoute.value.name);
};

// 关闭右侧&关闭左侧
const closeSides = (type: string) => {
  // 获得当前index；当前处于未进入标签栏的隐藏路由时不做侧边关闭
  const currentIndex = tabsList.value.findIndex((item: Menu.MenuOptions) => item.name === currentRoute.value?.name);
  if (currentIndex === -1) return;
  // 过滤出两侧可关闭的 affix: false 表示可关闭
  const removeNames = tabsList.value
    .filter((item: Menu.MenuOptions, index: number) => {
      if (item.meta?.affix) return false;
      return type === "right" ? index > currentIndex : index < currentIndex;
    })
    .map((item: Menu.MenuOptions) => item.name);
  tabsList.value = tabsList.value.filter((item: Menu.MenuOptions) => !removeNames.includes(item.name));
};

// 关闭其它&关闭全部
const closeOther = (type: string) => {
  const currentName = currentRoute.value?.name;
  // 过滤出可关闭项 affix: false 表示可关闭
  const removeNames = tabsList.value
    .filter((item: Menu.MenuOptions) => {
      if (item.meta?.affix) return false;
      return type === "other" ? item.name !== currentName : true;
    })
    .map((item: Menu.MenuOptions) => item.name);
  // 删除可关闭项
  tabsList.value = tabsList.value.filter((item: Menu.MenuOptions) => !removeNames.includes(item.name));
  // 关闭全部，若当前不在固定页上（含直接打开隐藏路由导致 currentRoute 为空）则跳转最后一个剩余标签页
  const last = tabsList.value.at(-1);
  if (type === "all" && last && !currentRoute.value?.meta?.affix) {
    routerPush(last);
  }
};
</script>

<style lang="scss" scoped>
.tabs {
  box-sizing: border-box;
  display: flex;
  align-items: center;
  justify-content: space-between;
  height: 40px;
  border-bottom: $border-1 solid $color-border-2;
  :deep(.arco-tabs) {
    flex: 1 1 auto;
    min-width: 0;
  }
  .tabs_setting {
    margin: 0 0 0 $margin;
    .setting {
      margin-right: $margin;
      color: $color-text-2;
    }
    .refresh {
      transition: transform 0.5s;
      transform: rotate(360deg);
    }
  }
}
:deep(.arco-tabs-nav-tab) {
  // 移入展示关闭icon
  .arco-tabs-tab-closable {
    svg {
      width: 0;
      transition: all 0.2s;
    }
    &:hover {
      svg {
        width: 1em;
      }
    }
  }

  // 消除tab移入的背景色
  &:hover .arco-tabs-tab-title::before {
    background: unset;
  }
}

// 消除tabs底部边线
:deep(.arco-tabs-nav) {
  &::before {
    background: unset;
  }
}
</style>
