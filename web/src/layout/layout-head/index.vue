<template>
  <div class="layout-head">
    <div class="layout-head-top">
      <a-layout-header class="header">
        <div class="header-logo" v-if="!isMobile">
          <Logo />
        </div>
        <div class="layout-head-menu" v-if="!isMobile">
          <a-menu
            v-if="drawing"
            mode="horizontal"
            :selected-keys="[currentRoute.name]"
            @menu-item-click="onMenuItem"
            :popup-max-height="600"
          >
            <template v-for="item in routeTree" :key="item.name">
              <a-sub-menu v-if="menuShow(item)" :key="item.name" :popup-max-height="600">
                <template #icon v-if="item.meta.svgIcon || item.meta.icon">
                  <MenuItemIcon :svg-icon="item.meta.svgIcon" :icon="item.meta.icon" />
                </template>
                <template #title>{{ $t(`menu.${item.meta.title}`) }}</template>
                <MenuItem :route-tree="item.children" />
              </a-sub-menu>
              <a-menu-item v-else-if="aMenuShow(item)" :key="item?.name">
                <template #icon v-if="item.meta.svgIcon || item.meta.icon">
                  <MenuItemIcon :svg-icon="item.meta.svgIcon" :icon="item.meta.icon" />
                </template>
                <span>{{ $t(`menu.${item.meta.title}`) }}</span>
              </a-menu-item>
            </template>
          </a-menu>
        </div>
        <ButtonCollapsed v-else />

        <div class="space-switcher" v-if="!isMobile">
          <span class="space-label">当前空间</span>
          <a-select
            class="space-select"
            :model-value="selectedSpaceId"
            :loading="spaceLoading"
            size="small"
            placeholder="请选择空间"
            @change="onSpaceChange"
          >
            <a-option v-for="space in spaces" :key="space.space_id" :value="space.space_id">
              {{ space.name || space.space_id }}
            </a-option>
          </a-select>
          <a-button class="space-setting-button" type="text" size="small" title="新建空间" @click="openCreate">
            <template #icon><icon-plus /></template>
          </a-button>
          <a-button class="space-setting-button" type="text" size="small" title="空间管理" @click="goSpaceSettings">
            <template #icon><icon-settings /></template>
          </a-button>
        </div>

        <HeaderRight />
      </a-layout-header>
      <Main />
      <Footer v-if="isFooter" />

      <a-modal
        v-model:visible="createVisible"
        title="新建空间"
        :on-before-ok="submitCreate"
        @cancel="resetCreate"
      >
        <a-form :model="createForm" layout="vertical">
          <a-form-item label="空间 ID" required>
            <a-input v-model="createForm.space_id" placeholder="如 hk_stock" />
          </a-form-item>
          <a-form-item label="名称" required>
            <a-input v-model="createForm.name" placeholder="空间名称" />
          </a-form-item>
          <a-form-item label="描述">
            <a-input v-model="createForm.description" />
          </a-form-item>
          <a-form-item label="负责人">
            <a-input v-model="createForm.owner" />
          </a-form-item>
          <a-form-item label="市场">
            <a-input v-model="createForm.market" placeholder="如 HK / US / CN" />
          </a-form-item>
          <a-form-item label="时区">
            <a-input v-model="createForm.timezone" placeholder="如 Asia/Shanghai" />
          </a-form-item>
        </a-form>
      </a-modal>
    </div>
  </div>
</template>

<script setup lang="ts">
import Logo from "@/layout/components/Logo/index.vue";
import HeaderRight from "@/layout/components/Header/components/header-right/index.vue";
import Main from "@/layout/components/Main/index.vue";
import Footer from "@/layout/components/Footer/index.vue";
import MenuItem from "@/layout/components/Menu/menu-item.vue";
import MenuItemIcon from "@/layout/components/Menu/menu-item-icon.vue";
import ButtonCollapsed from "@/layout/components/Header/components/button-collapsed/index.vue";
import { storeToRefs } from "pinia";
import { useRoutesConfigStore } from "@/store/modules/route-config";
import { useRoutingMethod } from "@/hooks/useRoutingMethod";
import { useThemeConfig } from "@/store/modules/theme-config";
import { useSpaceStore } from "@/store/modules/space";
import { createSpace } from "@/api/admin/spaces";
import { useMenuMethod } from "@/hooks/useMenuMethod";
import { useDevicesSize } from "@/hooks/useDevicesSize";
import { Message } from "@arco-design/web-vue";
defineOptions({ name: "LayoutHead" });
const router = useRouter();
const routerStore = useRoutesConfigStore();
const themeStore = useThemeConfig();
const spaceStore = useSpaceStore();
const { routeTree, currentRoute } = storeToRefs(routerStore);
const { isFooter, language } = storeToRefs(themeStore);
const { spaces, selectedSpaceId, loading: spaceLoading } = storeToRefs(spaceStore);
const { isMobile } = useDevicesSize();
const { menuShow, aMenuShow } = useMenuMethod();

const drawing = ref<boolean>(true);
watch(language, () => {
  drawing.value = false;
  nextTick(() => (drawing.value = true));
});

onMounted(async () => {
  try {
    await spaceStore.loadSpaces();
    if (spaces.value.length === 0) {
      Message.info("暂无空间，请先创建空间");
    }
  } catch (error) {
    console.error("加载空间列表失败:", error);
    Message.error("加载空间列表失败");
  }
});

const onSpaceChange = (value: string | number | boolean | Record<string, unknown> | undefined) => {
  spaceStore.setSelectedSpace(typeof value === "string" ? value : "");
};

const goSpaceSettings = () => {
  router.push("/settings/spaces");
};

const createVisible = ref(false);
const createForm = reactive({
  space_id: "",
  name: "",
  description: "",
  owner: "",
  market: "",
  timezone: ""
});

const resetCreate = () => {
  createForm.space_id = "";
  createForm.name = "";
  createForm.description = "";
  createForm.owner = "";
  createForm.market = "";
  createForm.timezone = "";
};

const openCreate = () => {
  resetCreate();
  createVisible.value = true;
};

const submitCreate = async (): Promise<boolean> => {
  const spaceId = createForm.space_id.trim();
  const name = createForm.name.trim();
  if (!spaceId || !name) {
    Message.warning("请填写空间 ID 和名称");
    return false;
  }
  try {
    await createSpace({ ...createForm, space_id: spaceId, name, status: "active" });
    await spaceStore.loadSpaces();
    spaceStore.setSelectedSpace(spaceId);
    Message.success("空间创建成功");
    resetCreate();
    return true;
  } catch (error) {
    Message.error(error instanceof Error ? error.message : "创建空间失败");
    return false;
  }
};

/**
 * @description 菜单点击事件
 * @param {String} key
 */
const onMenuItem = (key: string) => {
  const { findLinearArray } = useRoutingMethod();
  const find = findLinearArray(key);
  // 路由存在则存入并跳转，不存在则跳404
  if (find) {
    router.push(find.path);
  } else {
    router.push("/404");
  }
};
</script>

<style lang="scss" scoped>
.layout-head {
  height: 100vh;
  &-top {
    position: relative;
    display: grid;
    grid-template-rows: auto 1fr auto;
    height: 100%;
  }
}
.header {
  position: relative;
  box-sizing: border-box;
  display: flex;
  align-items: center;
  width: 100%;
  height: 60px;
  padding: 0 $padding;
  overflow: hidden;
  border-bottom: $border-1 solid $color-border-2;
  .header-logo {
    max-width: 180px;
  }
  .layout-head-menu {
    display: flex;
    flex: 1;
    min-width: 0;
    overflow: hidden;
  }
  .space-switcher {
    display: flex;
    align-items: center;
    flex-shrink: 0;
    gap: 8px;
    min-width: 260px;
    max-width: 340px;
    margin-left: 16px;
  }
  .space-label {
    flex-shrink: 0;
    font-size: 13px;
    color: $color-text-2;
    white-space: nowrap;
  }
  .space-select {
    flex: 1;
    min-width: 150px;
  }
  .space-setting-button {
    flex-shrink: 0;
  }
}
:deep(.arco-menu-pop) {
  white-space: nowrap;
}

// 横向菜单样式修改
:deep(.arco-menu-horizontal) {
  flex: 1;
  overflow: hidden;
  .arco-menu-inner {
    padding-left: 0; // 横向排列，禁用左padding
    .arco-menu-overflow-wrap {
      white-space: nowrap; // 禁用换行，否则会导致菜单换行闪烁
    }
  }
}
</style>
