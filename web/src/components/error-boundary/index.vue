<template>
  <div class="error-boundary">
    <slot v-if="!hasError" />
    <div v-else class="error-content">
      <a-result status="error" title="页面加载失败">
        <template #subtitle>
          {{ errorMessage }}
        </template>
        <template #extra>
          <a-space>
            <a-button type="primary" @click="retry">重试</a-button>
            <a-button @click="goHome">返回首页</a-button>
          </a-space>
        </template>
      </a-result>
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, onErrorCaptured } from 'vue';
import { useRouter } from 'vue-router';

const router = useRouter();
const hasError = ref(false);
const errorMessage = ref('');

// 捕获子组件错误
onErrorCaptured((error: Error) => {
  console.error('组件错误:', error);
  hasError.value = true;
  errorMessage.value = error.message || '未知错误';
  return false; // 阻止错误继续传播
});

// 重试
const retry = () => {
  hasError.value = false;
  errorMessage.value = '';
  // 强制重新渲染
  window.location.reload();
};

// 返回首页
const goHome = () => {
  hasError.value = false;
  errorMessage.value = '';
  router.push('/home');
};
</script>

<style lang="scss" scoped>
.error-boundary {
  width: 100%;
  height: 100%;
}

.error-content {
  display: flex;
  justify-content: center;
  align-items: center;
  height: 100%;
  min-height: 400px;
}
</style>
