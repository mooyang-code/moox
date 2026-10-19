<template>
  <a-modal
    :visible="visible"
    width="680px"
    title="HTTPS 根证书"
    hide-cancel
    ok-text="关闭"
    @ok="emit('update:visible', false)"
    @cancel="emit('update:visible', false)"
    @close="emit('update:visible', false)"
  >
    <a-alert type="info" class="ca-alert">
      换了主机登录管理台时，浏览器提示“不安全”或页面定时请求报 Network Error，是因为这台主机还没有信任控制台的 HTTPS 根证书。
      下载并安装一次即可，之后换证书不会再影响浏览器。
    </a-alert>

    <div class="ca-row">
      <a-button type="primary" :href="ROOT_CA_BUNDLE_URL" :download="ROOT_CA_BUNDLE_FILE_NAME">
        <template #icon><icon-download /></template>
        下载安装包
      </a-button>
      <a-button :href="ROOT_CA_CERT_URL" :download="ROOT_CA_FILE_NAME">仅下载证书</a-button>
    </div>
    <p class="ca-hint ca-bundle-hint">
      安装包（{{ ROOT_CA_BUNDLE_FILE_NAME }}）含根证书、指纹、各系统的安装脚本和 <code>AGENT-INSTALL.md</code>：
      解压后把这个文件交给 Agent，它会先让你核对指纹，再按说明自动安装（需要你输入本机管理员密码）。
    </p>

    <div class="ca-row ca-fingerprint">
      <strong>SHA-256 指纹</strong>
      <a-spin v-if="loading" />
      <code v-else-if="fingerprint">{{ fingerprint }}</code>
      <span v-else class="ca-hint">当前环境没有提供根证书（未使用内置 CA，或页面不是由控制台代理提供）。</span>
    </div>
    <a-alert type="warning" class="ca-alert">
      这个页面和安装包是经由尚未被信任的连接下载的，包里的一切都可能被替换，包内指纹只能防误操作。请以运维人员或另一台已信任主机上看到的指纹为准
      （有 SSH 权限的机器直接运行
      <code>moox-cli setup trust-browser</code> 最稳妥）。安装时优先用下面的系统命令，不要不加检查地运行包内脚本。
    </a-alert>

    <a-tabs default-active-key="macos" size="small">
      <a-tab-pane v-for="guide in guides" :key="guide.key" :title="guide.label">
        <pre v-for="command in guide.commands" :key="command" class="ca-command">{{ command }}</pre>
        <p v-if="guide.note" class="ca-note">{{ guide.note }}</p>
      </a-tab-pane>
    </a-tabs>
  </a-modal>
</template>

<script setup lang="ts">
import { ref, watch } from "vue";
import {
  fetchRootCaFingerprint,
  ROOT_CA_BUNDLE_FILE_NAME,
  ROOT_CA_BUNDLE_URL,
  ROOT_CA_CERT_URL,
  ROOT_CA_FILE_NAME,
  rootCaGuides
} from "./root-ca";

const props = defineProps<{ visible: boolean }>();
const emit = defineEmits<{ (event: "update:visible", value: boolean): void }>();

const guides = rootCaGuides();
const loading = ref(false);
const fingerprint = ref<string | null>(null);

// 每次打开都重新读取：根证书轮换后指纹会变，不能沿用上次的结果。
watch(
  () => props.visible,
  async visible => {
    if (!visible) return;
    loading.value = true;
    fingerprint.value = null;
    try {
      fingerprint.value = await fetchRootCaFingerprint();
    } finally {
      loading.value = false;
    }
  },
  { immediate: true }
);
</script>

<style scoped lang="scss">
.ca-alert {
  margin-bottom: 12px;
}

.ca-row {
  display: flex;
  align-items: center;
  gap: 12px;
  margin-bottom: 12px;
}

.ca-fingerprint {
  flex-wrap: wrap;

  code {
    word-break: break-all;
  }
}

.ca-bundle-hint {
  margin: -4px 0 12px;
}

.ca-hint,
.ca-note {
  color: var(--color-text-3);
  font-size: 12px;
}

.ca-command {
  margin: 0 0 8px;
  padding: 8px 12px;
  overflow-x: auto;
  border-radius: 4px;
  background: var(--color-fill-2);
  font-size: 12px;
  user-select: all;
}
</style>
