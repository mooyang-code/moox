<template>
  <a-modal
    :visible="visible"
    width="min(720px, 100vw)"
    :title="rule ? '编辑订阅' : '新建订阅'"
    :ok-loading="saving"
    :ok-button-props="{ disabled: !catalogReady }"
    @update:visible="(value: boolean) => emit('update:visible', value)"
    @before-ok="submit"
  >
    <a-alert v-if="!catalogReady" type="warning" class="catalog-alert">
      没有读到事件目录，暂时不能{{
        rule ? "编辑" : "新建"
      }}订阅：不知道这个事件能按哪些字段限定范围，保存会把已有的范围清掉。请关闭后刷新页面再试。
    </a-alert>
    <a-form :model="form" auto-label-width :disabled="!catalogReady">
      <a-form-item field="name" label="名称" required>
        <a-input v-model="form.name" placeholder="例如：下架通知到交易群" :max-length="64" />
      </a-form-item>
      <a-form-item field="event_name" label="触发事件" required>
        <a-select v-model="form.event_name" placeholder="选择什么时候触发" :disabled="Boolean(rule)" @change="onEventChange">
          <a-option v-for="event in events" :key="event.event_name" :value="event.event_name">
            {{ event.title }}
          </a-option>
        </a-select>
      </a-form-item>
      <a-form-item v-if="currentEvent" hide-label>
        <EventInfo :event="currentEvent" class="rule-event-info" />
      </a-form-item>
      <a-form-item v-for="field in currentEvent?.scope_fields ?? []" :key="field.field" :label="field.title">
        <a-select v-if="field.options.length" v-model="scopeValues[field.field]" multiple allow-clear placeholder="不选表示不限">
          <a-option v-for="option in field.options" :key="option" :value="option">{{ option }}</a-option>
        </a-select>
        <a-input-tag v-else v-model="scopeValues[field.field]" allow-clear placeholder="不填表示不限，输入后回车添加" />
      </a-form-item>
      <a-form-item field="notify_url" label="通知地址" required>
        <a-input v-model="form.notify_url" placeholder="https://example.com/hooks/moox" />
        <template #extra
          >事件发生时，向这个地址发一条 POST 请求。只支持 http 和
          https。地址会显示在订阅列表和执行记录里，不要把令牌放进地址，接收方用验证密钥验签即可。<template
            v-if="rule?.secret_hint"
            >这条订阅的验证密钥末 4 位是 {{ rule.secret_hint }}。</template
          ></template
        >
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
import { notifyUrlError, sameScope, scopeFromForm, scopeToForm } from "../shared/event-center-utils";
import EventInfo from "../shared/event-info.vue";

defineOptions({ name: "EventRuleDialog" });

/** presetEvent：新建时预选的触发事件（从事件页签点“订阅”进来）；为空时选第一个。 */
const props = defineProps<{ visible: boolean; rule: EventRule | null; events: SubscribableEvent[]; presetEvent?: string }>();
const emit = defineEmits<{
  "update:visible": [value: boolean];
  saved: [payload: { rule: EventRule; signingSecret: string }];
}>();

const form = reactive({ name: "", event_name: "", notify_url: "", allow_private_address: false, enabled: true });
const scopeValues = reactive<Record<string, string[]>>({});
const saving = ref(false);

const currentEvent = computed(() => props.events.find(event => event.event_name === form.event_name));
// 事件目录没读到（或编辑的规则的事件不在目录里）时不知道能限定哪些字段：此时保存会把范围清空，所以整个表单禁用。
const catalogReady = computed(() => props.events.length > 0 && (!props.rule || Boolean(currentEvent.value)));

function resetScope(values: Record<string, string[]> = {}) {
  for (const key of Object.keys(scopeValues)) delete scopeValues[key];
  for (const field of currentEvent.value?.scope_fields ?? []) scopeValues[field.field] = [...(values[field.field] ?? [])];
}

function onEventChange() {
  resetScope();
}

function init() {
  const rule = props.rule;
  Object.assign(form, {
    name: rule?.name ?? "",
    event_name:
      rule?.event_name ??
      (props.events.some(event => event.event_name === props.presetEvent) ? props.presetEvent : props.events[0]?.event_name) ??
      "",
    notify_url: rule?.notify_url ?? "",
    allow_private_address: rule?.allow_private_address ?? false,
    enabled: rule?.enabled ?? true
  });
  resetScope(rule ? scopeToForm(rule.scope) : {});
}

watch(
  () => props.visible,
  visible => {
    if (visible) init();
  }
);

// 打开时事件目录还没读到，之后读到了：表单此前是禁用的，没有可丢的改动，按目录重新填一次。
watch(catalogReady, (ready, wasReady) => {
  if (props.visible && ready && !wasReady) init();
});

async function submit(): Promise<boolean> {
  if (!catalogReady.value) return false;
  if (!form.name.trim()) {
    Message.warning("请填写订阅名称");
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
      // 范围带上就整体替换：只在范围真的改了时才带，避免把没改的范围按表单重写一遍。
      const rule = await updateEventRule({
        rule_id: props.rule.rule_id,
        name: form.name.trim(),
        ...(sameScope(scope, props.rule.scope) ? {} : { scope: { items: scope } }),
        notify_url: form.notify_url.trim(),
        allow_private_address: form.allow_private_address,
        enabled: form.enabled
      });
      Message.success("订阅已更新");
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

<style scoped>
.catalog-alert {
  margin-bottom: 12px;
}

.rule-event-info {
  width: 100%;
}
</style>
