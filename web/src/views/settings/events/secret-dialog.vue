<template>
  <a-modal
    :visible="visible"
    width="720px"
    title="验证密钥"
    :mask-closable="false"
    :footer="false"
    @update:visible="(value: boolean) => emit('update:visible', value)"
  >
    <a-alert type="warning" class="secret-alert">
      规则「{{ ruleName }}」的验证密钥只在这里显示一次，关闭后无法再查看。请现在复制并保存到你的接口里；
      要换密钥，请删掉这条规则重新创建。
    </a-alert>
    <div class="secret-row">
      <code class="secret-value">{{ secret }}</code>
      <a-button size="small" @click="copySecret">复制</a-button>
    </div>

    <h4 class="section-title">怎样确认请求来自 MooX</h4>
    <p class="hint">
      每个请求都带有 X-MooX-Timestamp（发送时的毫秒时间戳）和 X-MooX-Signature。签名是对「时间戳 + 英文句号 +
      请求体」用上面的密钥做 HMAC-SHA256，取十六进制，前面加 sha256=。
    </p>
    <a-tabs size="small">
      <a-tab-pane v-for="example in examples" :key="example.language" :title="example.language">
        <CodeBlock :code="example.code" :language="example.language" />
      </a-tab-pane>
    </a-tabs>
    <ul class="notes">
      <li v-for="note in notes" :key="note">{{ note }}</li>
    </ul>
    <div class="actions">
      <a-button type="primary" @click="emit('update:visible', false)">我已保存密钥</a-button>
    </div>
  </a-modal>
</template>

<script setup lang="ts">
import { Message } from "@arco-design/web-vue";
import CodeBlock from "@/components/code-block/index.vue";
import { RECEIVER_NOTES, signatureExamples } from "./event-center-utils";

defineOptions({ name: "EventSecretDialog" });

const props = defineProps<{ visible: boolean; ruleName: string; secret: string }>();
const emit = defineEmits<{ "update:visible": [value: boolean] }>();

const examples = signatureExamples();
const notes = RECEIVER_NOTES;

async function copySecret() {
  if (!navigator.clipboard) {
    Message.warning("当前浏览器不支持复制，请手动选中密钥复制");
    return;
  }
  await navigator.clipboard.writeText(props.secret);
  Message.success("已复制");
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

.section-title {
  margin: 8px 0;
  font-size: 14px;
  font-weight: 600;
}

.hint {
  margin: 0 0 8px;
  color: var(--color-text-2);
  font-size: 13px;
}

.notes {
  margin: 8px 0 0;
  padding-left: 18px;
  color: var(--color-text-3);
  font-size: 13px;
  line-height: 1.8;
}

.actions {
  margin-top: 16px;
  text-align: right;
}
</style>
