<template>
  <!-- 窄屏只留用户菜单：明暗、全屏收起，不盖住左边的空间选择 -->
  <div class="header_setting" :class="isMobile && 'head-absolute-fix'">
    <!-- 切换黑夜模式 -->
    <a-tooltip v-if="!isMobile" :content="darkMode ? '切换白天模式' : '切换黑夜模式'">
      <a-button size="mini" type="text" class="icon_btn" id="system-dark" @click="onNightMode">
        <template #icon>
          <icon-sun-fill :size="18" v-if="!darkMode" />
          <icon-moon-fill :size="18" v-else />
        </template>
      </a-button>
    </a-tooltip>
    <!-- 全屏 -->
    <a-tooltip v-if="!isMobile" :content="fullScreen ? '全屏' : '退出全屏'">
      <a-button size="mini" type="text" class="icon_btn" id="system-fullscreen" @click="onFullScreen">
        <template #icon>
          <icon-fullscreen :size="18" v-if="fullScreen" />
          <icon-fullscreen-exit :size="18" v-else />
        </template>
      </a-button>
    </a-tooltip>
    <!-- 我的 -->
    <a-dropdown trigger="hover">
      <div class="my_setting" id="system-my-setting">
        <!-- 头像不用图片：显示用户名首字，没有用户名时显示图标 -->
        <a-avatar :size="32" class="my_avatar">
          <template v-if="avatarText">{{ avatarText }}</template>
          <icon-user v-else />
        </a-avatar>
        <span class="user-nickname">{{ account.user.userName }}</span>
        <div class="icon_down">
          <icon-down style="stroke-width: 3" />
        </div>
      </div>
      <template #content>
        <!-- 退出登录 -->
        <a-doption @click="logOut">
          <template #default>
            <SvgIcon :name="'exit'" :size="18" />
            <span class="margin-left-text">退出登录</span>
          </template>
        </a-doption>
      </template>
    </a-dropdown>
  </div>
</template>

<script setup lang="ts">
import { Modal } from "@arco-design/web-vue";
import { useRouter } from "vue-router";
import { storeToRefs } from "pinia";
import { useUserInfoStore } from "@/store/modules/user-info";
import { useThemeConfig } from "@/store/modules/theme-config";
import { useThemeMethods } from "@/hooks/useThemeMethods";
import { useDevicesSize } from "@/hooks/useDevicesSize";
import { useRoutesConfigStore } from "@/store/modules/route-config";

const router = useRouter();
const { isMobile } = useDevicesSize();
const userStore = useUserInfoStore();
const themeStore = useThemeConfig();
const { account } = storeToRefs(userStore);
const { darkMode } = storeToRefs(themeStore);

const avatarText = computed(() => (account.value.user.userName || "").trim().charAt(0).toUpperCase());

// 全屏
const fullScreen = ref(true);
const onFullScreen = () => {
  if (!document.fullscreenElement) {
    document.documentElement.requestFullscreen();
    fullScreen.value = false;
  } else {
    if (document.exitFullscreen) {
      document.exitFullscreen();
      fullScreen.value = true;
    }
  }
};

// 黑暗模式
const onNightMode = () => {
  darkMode.value = !darkMode.value;
  useThemeMethods().setDarkMode();
};

// 退出登录
const logOut = () => {
  Modal.warning({
    title: "提示",
    content: "确定退出登录？",
    hideCancel: false,
    closable: true,
    onBeforeOk: async () => {
      try {
        // 用户退出
        await userStore.logOut();
        router.replace("/login");
        // 清除路由数据
        useRoutesConfigStore().resetRoute();
        return true;
      } catch {
        return false;
      }
    }
  });
};
</script>

<style lang="scss" scoped>
.head-absolute-fix {
  position: absolute;
  top: 0;
  right: $padding;
}
.header_setting {
  display: flex;
  align-items: center;
  justify-content: space-between;
  height: 100%;
  background-color: $color-bg-2;
  > .icon_btn {
    box-sizing: border-box;
    display: flex;
    align-items: center;
    justify-content: space-around;
    width: $icon-box;
    height: $icon-box;
    margin-left: $margin;
    color: $color-text-1;
    border-radius: $radius-box-1;
  }
  .my_setting {
    display: flex;
    align-items: center;
    justify-content: space-between;
    height: 32px;
    margin-left: $margin;
    overflow: hidden;
    .my_avatar {
      flex-shrink: 0;
      margin-right: var(--moox-space-2);
      // 跟随主题主色，不用默认的灰底
      color: #fff;
      background-color: rgb(var(--primary-6));
    }
    .user-nickname {
      white-space: nowrap;
    }
    .icon_down {
      margin: 0 0 0 5px;
      transition: transform 0.2s;
      transform: rotate(0deg);
    }
  }
}
:deep(.arco-dropdown-open) {
  .icon_down {
    transform: rotate(180deg) !important;
  }
}
.margin-left-text {
  margin-left: $margin-text;
}
</style>
