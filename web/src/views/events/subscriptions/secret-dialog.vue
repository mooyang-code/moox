<template>
  <!-- 密钥只显示这一次：不许按 Esc 或点遮罩误关，只能点“我已保存密钥”或右上角关闭 -->
  <a-modal
    :visible="visible"
    width="min(720px, 100vw)"
    title="验证密钥"
    :mask-closable="false"
    :esc-to-close="false"
    :footer="false"
    unmount-on-close
    @update:visible="(value: boolean) => emit('update:visible', value)"
  >
    <a-alert type="warning" class="secret-alert">
      订阅「{{ ruleName }}」的验证密钥只在这里显示一次，关闭后无法再查看。请现在复制并保存到你的接口里；
      要换密钥，请删掉这条订阅重新创建。
    </a-alert>
    <div class="secret-row">
      <code class="secret-value">{{ secret }}</code>
      <a-button size="small" :loading="copying" @click="copySecret">复制</a-button>
    </div>

    <ReceiverGuide />
    <p class="hint">请求头、请求体字段和这些说明在事件订阅页的「接收说明」里随时能看到。</p>
    <div class="actions">
      <a-button type="primary" @click="emit('update:visible', false)">我已保存密钥</a-button>
    </div>
  </a-modal>
</template>

<script setup lang="ts">
import { ref } from "vue";
import { Message } from "@arco-design/web-vue";
import ReceiverGuide from "../shared/receiver-guide.vue";

defineOptions({ name: "EventSecretDialog" });

const props = defineProps<{ visible: boolean; ruleName: string; secret: string }>();
const emit = defineEmits<{ "update:visible": [value: boolean] }>();

const copying = ref(false);

async function copySecret() {
  if (!navigator.clipboard) {
    Message.warning("当前浏览器不支持复制，请手动选中密钥复制");
    return;
  }
  copying.value = true;
  try {
    await navigator.clipboard.writeText(props.secret);
    Message.success("已复制");
  } catch {
    Message.error("复制失败，请手动选中密钥复制");
  } finally {
    copying.value = false;
  }
}
</script>

<style scoped>
.secret-alert {
  margin-bottom: 12px;
}

.secret-row {
  display: flex;
  align-items: center;
  gap: 12px;
  margin-bottom: 16px;
}

.secret-value {
  flex: 1;
  padding: 6px 10px;
  overflow-wrap: anywhere;
  background: var(--color-fill-2);
  border-radius: 4px;
  user-select: all;
}

.hint {
  margin: 8px 0 0;
  color: var(--color-text-3);
  font-size: 13px;
}

.actions {
  margin-top: 16px;
  text-align: right;
}
</style>
