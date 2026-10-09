<template>
  <a-modal :visible="visible" title="告警推送设置" @ok="save" @cancel="emit('update:visible', false)">
    <a-form layout="vertical">
      <a-form-item label="推送平台">
        <a-select v-model="form.channel_type">
          <a-option value="wecom">企业微信</a-option>
          <a-option value="feishu">飞书</a-option>
        </a-select>
      </a-form-item>
      <a-form-item label="机器人 Webhook URL">
        <a-input v-model="form.webhook_url" placeholder="输入新的 HTTPS Webhook URL" @input="form.url_changed = true" />
      </a-form-item>
      <a-checkbox v-model="form.clear_url">清空当前 URL，停止站外推送</a-checkbox>
      <div class="current-setting">当前配置：{{ form.masked || "未配置" }}</div>
    </a-form>
  </a-modal>
</template>

<script setup lang="ts">
import { reactive, watch } from "vue";
import { Message, Modal } from "@arco-design/web-vue";
import { monitorApi } from "@/api/monitor";

const props = defineProps<{ visible: boolean }>();
const emit = defineEmits<{ (event: "update:visible", value: boolean): void; (event: "saved"): void }>();

const form = reactive({ channel_type: "wecom", webhook_url: "", masked: "", clear_url: false, url_changed: false });
let initialType = "wecom";

watch(
  () => props.visible,
  async visible => {
    if (!visible) return;
    try {
      const setting = (await monitorApi.getNotification()).channel || {};
      form.channel_type = setting.channel_type || "wecom";
      initialType = form.channel_type;
      form.masked = setting.masked_url || "";
      form.webhook_url = "";
      form.clear_url = false;
      form.url_changed = false;
    } catch (err) {
      Message.error(err instanceof Error ? err.message : "推送配置加载失败");
      emit("update:visible", false);
    }
  }
);

async function save() {
  const clearing = form.clear_url || (form.url_changed && !form.webhook_url.trim());
  if (!form.url_changed && !form.clear_url && form.channel_type === initialType) {
    emit("update:visible", false);
    return;
  }
  if (!form.url_changed && !form.clear_url) {
    Message.warning("修改推送平台前，请重新输入 Webhook URL 或勾选清空");
    return;
  }
  if (clearing) {
    form.clear_url = true;
    Modal.warning({
      title: "确认停用推送",
      content: "清空后系统将不再向站外平台推送告警，是否继续？",
      onOk: () => void persist()
    });
    return;
  }
  await persist();
}

async function persist() {
  try {
    const rsp = await monitorApi.updateNotification({
      channel_type: form.channel_type,
      webhook_url: form.clear_url ? "" : form.webhook_url
    });
    form.masked = rsp.channel?.masked_url || "";
    initialType = form.channel_type;
    emit("update:visible", false);
    emit("saved");
    Message.success("推送配置已保存");
  } catch (err) {
    Message.error(err instanceof Error ? err.message : "推送配置保存失败");
  }
}
</script>

<style scoped lang="scss">
.current-setting {
  margin-top: var(--moox-space-2);
  color: var(--color-text-3);
}
</style>
