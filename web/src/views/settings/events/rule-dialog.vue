<template>
  <a-modal
    :visible="visible"
    width="640px"
    :title="rule ? '编辑规则' : '新建规则'"
    :ok-loading="saving"
    @update:visible="(value: boolean) => emit('update:visible', value)"
    @before-ok="submit"
  >
    <a-form :model="form" auto-label-width>
      <a-form-item field="name" label="名称" required>
        <a-input v-model="form.name" placeholder="例如：下架通知到交易群" :max-length="64" />
      </a-form-item>
      <a-form-item field="event_name" label="触发事件" required>
        <a-select v-model="form.event_name" placeholder="选择什么时候触发" :disabled="Boolean(rule)" @change="onEventChange">
          <a-option v-for="event in events" :key="event.event_name" :value="event.event_name">
            {{ event.title }}
          </a-option>
        </a-select>
        <template #extra>{{ currentEvent?.description }}</template>
      </a-form-item>
      <a-form-item v-for="field in currentEvent?.scope_fields ?? []" :key="field.field" :label="field.title">
        <a-select v-if="field.options.length" v-model="scopeValues[field.field]" multiple allow-clear placeholder="不选表示不限">
          <a-option v-for="option in field.options" :key="option" :value="option">{{ option }}</a-option>
        </a-select>
        <a-input-tag v-else v-model="scopeValues[field.field]" allow-clear placeholder="不填表示不限，输入后回车添加" />
      </a-form-item>
      <a-form-item field="notify_url" label="通知地址" required>
        <a-input v-model="form.notify_url" placeholder="https://example.com/hooks/moox" />
        <template #extra>事件发生时，向这个地址发一条 POST 请求。只支持 http 和 https。</template>
      </a-form-item>
      <a-form-item field="allow_private_address" label="允许内网地址">
        <a-switch v-model="form.allow_private_address" />
        <template #extra>
          默认不能向内网地址发送。你的接口在内网时才需要打开；本机地址和云服务器的元数据地址始终不允许。
        </template>
      </a-form-item>
      <a-form-item field="enabled" label="启用">
        <a-switch v-model="form.enabled" />
      </a-form-item>
    </a-form>
  </a-modal>
</template>

<script setup lang="ts">
import { computed, reactive, ref, watch } from "vue";
import { Message } from "@arco-design/web-vue";
import { createEventRule, updateEventRule, type EventRule, type SubscribableEvent } from "@/api/callback-dispatcher";
import { reportControlError } from "@/api/admin/http";
import { notifyUrlError, scopeFromForm, scopeToForm } from "./event-center-utils";

defineOptions({ name: "EventRuleDialog" });

const props = defineProps<{ visible: boolean; rule: EventRule | null; events: SubscribableEvent[] }>();
const emit = defineEmits<{
  "update:visible": [value: boolean];
  saved: [payload: { rule: EventRule; signingSecret: string }];
}>();

const form = reactive({ name: "", event_name: "", notify_url: "", allow_private_address: false, enabled: true });
const scopeValues = reactive<Record<string, string[]>>({});
const saving = ref(false);

const currentEvent = computed(() => props.events.find(event => event.event_name === form.event_name));

function resetScope(values: Record<string, string[]> = {}) {
  for (const key of Object.keys(scopeValues)) delete scopeValues[key];
  for (const field of currentEvent.value?.scope_fields ?? []) scopeValues[field.field] = [...(values[field.field] ?? [])];
}

function onEventChange() {
  resetScope();
}

watch(
  () => props.visible,
  visible => {
    if (!visible) return;
    const rule = props.rule;
    Object.assign(form, {
      name: rule?.name ?? "",
      event_name: rule?.event_name ?? props.events[0]?.event_name ?? "",
      notify_url: rule?.notify_url ?? "",
      allow_private_address: rule?.allow_private_address ?? false,
      enabled: rule?.enabled ?? true
    });
    resetScope(rule ? scopeToForm(rule.scope) : {});
  }
);

async function submit(): Promise<boolean> {
  if (!form.name.trim()) {
    Message.warning("请填写规则名称");
    return false;
  }
  if (!form.event_name) {
    Message.warning("请选择触发事件");
    return false;
  }
  const urlError = notifyUrlError(form.notify_url);
  if (urlError) {
    Message.warning(urlError);
    return false;
  }
  const scope = scopeFromForm(scopeValues, currentEvent.value?.scope_fields ?? []);
  saving.value = true;
  try {
    if (props.rule) {
      const rule = await updateEventRule({
        rule_id: props.rule.rule_id,
        name: form.name.trim(),
        scope: { items: scope },
        notify_url: form.notify_url.trim(),
        allow_private_address: form.allow_private_address,
        enabled: form.enabled
      });
      Message.success("规则已更新");
      emit("saved", { rule, signingSecret: "" });
    } else {
      const created = await createEventRule({
        name: form.name.trim(),
        event_name: form.event_name,
        scope,
        notify_url: form.notify_url.trim(),
        allow_private_address: form.allow_private_address,
        enabled: form.enabled
      });
      emit("saved", { rule: created.rule, signingSecret: created.signing_secret });
    }
    return true;
  } catch (error) {
    reportControlError(error);
    return false;
  } finally {
    saving.value = false;
  }
}
</script>
