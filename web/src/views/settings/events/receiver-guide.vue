<template>
  <div class="receiver-guide">
    <template v-if="full">
      <h4 class="section-title">请求长什么样</h4>
      <p class="hint">
        事件发生时，向规则里的通知地址发一条 POST 请求，请求体是 JSON。“发一条测试”发的也是这个格式，内容是示例。
      </p>
      <CodeBlock :code="SAMPLE_REQUEST" language="http" />

      <h4 class="section-title">请求头</h4>
      <a-table :data="REQUEST_HEADERS" row-key="name" size="small" :pagination="false" :bordered="{ cell: true }">
        <template #columns>
          <a-table-column title="名称" data-index="name" :width="180" />
          <a-table-column title="说明" data-index="desc" />
        </template>
      </a-table>

      <h4 class="section-title">请求体字段</h4>
      <a-table :data="BODY_FIELDS" row-key="name" size="small" :pagination="false" :bordered="{ cell: true }">
        <template #columns>
          <a-table-column title="字段" data-index="name" :width="180" />
          <a-table-column title="说明" data-index="desc" />
        </template>
      </a-table>
    </template>

    <h4 class="section-title">怎样确认请求来自 MooX</h4>
    <p class="hint">
      每个请求都带有 X-MooX-Timestamp（发送时的毫秒时间戳）和 X-MooX-Signature。签名是对「时间戳 + 英文句号 +
      原始请求体」用规则的验证密钥做 HMAC-SHA256，取十六进制，前面加
      sha256=。验证密钥只在新建规则时显示一次，规则列表里能看到它的末 4 位。
    </p>
    <a-tabs size="small">
      <a-tab-pane v-for="example in examples" :key="example.language" :title="example.language">
        <CodeBlock :code="example.code" :language="example.language" />
      </a-tab-pane>
    </a-tabs>

    <h4 class="section-title">成功、重试与去重</h4>
    <p v-for="note in RECEIVER_NOTES" :key="note" class="hint">{{ note }}</p>
  </div>
</template>

<script setup lang="ts">
import CodeBlock from "@/components/code-block/index.vue";
import { BODY_FIELDS, RECEIVER_NOTES, REQUEST_HEADERS, SAMPLE_REQUEST, signatureExamples } from "./event-center-utils";

defineOptions({ name: "EventReceiverGuide" });

/** full：常驻说明页，额外列出请求格式、请求头和请求体字段；新建规则后的密钥弹窗只显示验签部分。 */
defineProps<{ full?: boolean }>();

const examples = signatureExamples();
</script>

<style scoped>
.section-title {
  margin: 16px 0 8px;
  font-size: 14px;
  font-weight: 600;
}

.receiver-guide > .section-title:first-child {
  margin-top: 0;
}

.hint {
  margin: 0 0 8px;
  color: var(--color-text-2);
  font-size: 13px;
  line-height: 1.7;
}
</style>
