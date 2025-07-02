<template>
  <div class="page-wrapper">
    <!-- 加载状态 -->
    <div v-if="loading" class="loading-container">
      <a-spin size="large" :tip="loadingText" />
    </div>
    
    <!-- 错误状态 -->
    <div v-else-if="error" class="error-container">
      <a-result status="error" :title="errorTitle" :subtitle="errorMessage">
        <template #extra>
          <a-space>
            <a-button type="primary" @click="retry">重试</a-button>
            <a-button @click="goBack">返回</a-button>
          </a-space>
        </template>
      </a-result>
    </div>
    
    <!-- 正常内容 -->
    <div v-else class="page-content">
      <slot />
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, onErrorCaptured } from 'vue';
import { useRouter } from 'vue-router';

interface Props {
  loading?: boolean;
  loadingText?: string;
  errorTitle?: string;
  errorMessage?: string;
}

const props = withDefaults(defineProps<Props>(), {
  loading: false,
  loadingText: '页面加载中...',
  errorTitle: '页面加载失败',
  errorMessage: '请稍后重试或联系管理员'
});

const emit = defineEmits<{
  retry: [];
}>();

const router = useRouter();
const error = ref(false);
const internalErrorMessage = ref('');

// 捕获子组件错误
onErrorCaptured((err: Error, instance, info) => {
  console.error('页面组件错误:', err);
  console.error('错误实例:', instance);
  console.error('错误信息:', info);
  
  error.value = true;
  internalErrorMessage.value = err.message || '未知错误';
  return false; // 阻止错误继续传播
});

// 重试
const retry = () => {
  error.value = false;
  internalErrorMessage.value = '';
  emit('retry');
};

// 返回
const goBack = () => {
  router.back();
};

// 计算错误消息
const errorMessage = computed(() => {
  return internalErrorMessage.value || props.errorMessage;
});
</script>

<style lang="scss" scoped>
.page-wrapper {
  width: 100%;
  height: 100%;
  min-height: 400px;
}

.loading-container,
.error-container {
  display: flex;
  justify-content: center;
  align-items: center;
  height: 100%;
  min-height: 400px;
}

.page-content {
  width: 100%;
  height: 100%;
}
</style>
