<template>
  <div class="moox-page">
    <div class="moox-inner">
      <div class="my-avatar">
        <a-avatar :size="100">
          <img alt="avatar" :src="myImage" />
        </a-avatar>
        <div class="my-name">
          <div class="my-title title-size-1">Moo</div>
          <div class="my-local"><icon-location />广东深圳</div>
        </div>
      </div>
      <a-grid :cols="{ xs: 1, sm: 1, md: 1, lg: 2, xl: 3 }" :col-gap="16" :row-gap="16">
        <a-grid-item>
          <div class="title-size-1">👋关于我</div>
          <div class="type-row" v-for="item in list.aboutMe" :key="item.label">
            <div class="column-title-size-1">{{ item.label }}：</div>
            <div v-for="(type, index) in item.value" :key="index">
              <a-tag :color="item.theme">
                {{ type }}
              </a-tag>
            </div>
          </div>
        </a-grid-item>
        <a-grid-item>
          <div class="title-size-1">💬联系我</div>
          <div class="type-row" v-for="item in list.callMe" :key="item.label">
            <div class="column-title-size-2">{{ item.label }}：</div>
            <div :class="item.link ? 'link-hover' : ''" @click="onLink(item)">{{ item.value }}</div>
          </div>
        </a-grid-item>
        <a-grid-item>
          <div class="title-size-1">🐾其它</div>
          <div class="type-row" v-for="item in list.other" :key="item.label">
            <div class="column-title-size-2">{{ item.label }}：</div>
            <div>{{ item.value }}</div>
          </div>
        </a-grid-item>
        <a-grid-item>
          <a-image default-scale height="220" :src="otherImage" />
        </a-grid-item>
        <a-grid-item>
          <div class="title-size-2">✨Hello 量化探索者们✨</div>
          <div class="type-row" v-for="item in list.introduce" :key="item.label">
            <div class="text-ellipsis">{{ item.label }}</div>
          </div>
        </a-grid-item>
      </a-grid>
    </div>
  </div>
</template>

<script setup lang="ts">
import myImage from "@/assets/img/my-image.jpg";
import otherImage from "@/assets/img/other-image.jpg";

const list = reactive({
  aboutMe: [
    { label: "💫个性性格", value: ["射手座", "INFJ-T", "长期主义者"], theme: "blue" },
    { label: "💼职业工作", value: ["互联网", "后端架构师"], theme: "orangered" },
    { label: "💙兴趣爱好", value: ["看代码", "看美女👩🏻"], theme: "green" },
    { label: "🏷其他标签", value: ["web3小韭菜", "港股资深韭菜", "伪价投"], theme: "purple" }
  ],
  callMe: [
    { label: "📧邮箱", value: "702080167@qq.com", link: false },
    { label: "📠微信", value: "Maxwell-2025", link: false }
  ],
  other: [
    { label: "🌟个人主页", value: "" }
  ],
  introduce: [
    { label: "👋欢迎来到我的量化交易工作台，从数据到策略的完整实验室" },
    { label: "📊项目化管理交易策略，数据采集、因子构建、回测验证一站式流程" },
    { label: "💡实时监控策略表现，定时任务自动化执行，让系统为你工作" },
    { label: "🎯版本化管理因子和策略，每次优化都有迹可循，持续迭代进步" },
    { label: "🚀小而精的个人量化系统，专注策略研发和风险控制，Let's build & trade!" }
  ]
});

const onLink = (item: any) => {
  if (!item.link) return;
  window.open(item.url);
};
</script>

<style lang="scss" scoped>
.border {
  border: 1px solid red;
}
.my-avatar {
  display: flex;
  align-items: center;
  margin-bottom: calc($margin * 2);
  .my-name {
    height: 60px;
    margin-left: $margin;
    .my-title {
      height: 30px;
    }
    .my-local {
      display: flex;
      align-items: end;
      height: 30px;
      font-size: $font-size-body-3;
    }
  }
}
.type-row {
  display: flex;
  column-gap: $margin;
  align-items: center;
  height: 25px;
  margin-top: $margin;
  .column-title-size-1 {
    min-width: 90px;
  }
  .column-title-size-2 {
    min-width: 68px;
  }
  .text-ellipsis {
    white-space: nowrap;
  }
}
.title-size-1 {
  font-size: $font-size-title-1;
  font-weight: bold;
}
.title-size-2 {
  font-size: $font-size-title-2;
  font-weight: bold;
}
.link-hover {
  color: $color-primary;
  cursor: pointer;
}
</style>
