<template>
  <a-modal
    :visible="visible"
    :modal-class="`tx-confirm tx-confirm--${tier.env}`"
    width="min(560px, calc(100vw - 24px))"
    :ok-text="confirmText"
    :ok-button-props="{ status: danger ? 'danger' : 'normal', disabled: !canSubmit }"
    :on-before-ok="handleOk"
    unmount-on-close
    @update:visible="(value: boolean) => emit('update:visible', value)"
  >
    <template #title>
      <span>{{ title }}</span>
      <EnvMark v-if="tier.env !== 'paper'" :env="tier.env" class="tx-confirm-env" />
    </template>
    <a-alert v-if="tier.banner" :type="tier.env === 'prod' ? 'error' : 'warning'" class="tx-confirm-block">
      {{ tier.banner }}
    </a-alert>
    <slot />
    <a-form v-if="needReason || tier.requiresName" :model="{}" layout="vertical" class="tx-confirm-block">
      <a-form-item v-if="needReason" label="原因（必填）">
        <a-input v-model="reason" :placeholder="reasonPlaceholder" :max-length="200" allow-clear />
      </a-form-item>
      <a-form-item v-if="tier.requiresName" :label="`输入${nameLabel}「${comboName}」确认`">
        <a-input v-model="typedName" autocomplete="off" />
      </a-form-item>
    </a-form>
    <a-alert v-if="error" type="error" class="tx-confirm-block">{{ error }}</a-alert>
  </a-modal>
</template>

<script setup lang="ts">
import { computed, ref, watch } from "vue";
import { withDialogErrors } from "@/api/trade";
import { createSubmitScope } from "@/utils/submit-scope";
import EnvMark from "./env-mark.vue";
import { confirmTier, nameConfirmed, type Environment } from "./environment";

const props = withDefaults(
  defineProps<{
    visible: boolean;
    title: string;
    env: Environment;
    /** 生产环境要输入的名称：默认是组合名称，执行账户的操作传执行账户名称 */
    comboName: string;
    /** 名称的说法，显示在输入框标题里 */
    nameLabel?: string;
    confirmText?: string;
    danger?: boolean;
    needReason?: boolean;
    /** 生产环境是否要输入组合名称；暂停这类朝安全方向的操作不需要 */
    nameRequired?: boolean;
    reasonPlaceholder?: string;
    /** 提交：抛出异常表示失败，错误信息显示在对话框里；返回 false 表示失败但不显示信息 */
    submit: (reason: string) => Promise<boolean | void>;
  }>(),
  { nameLabel: "组合名称", confirmText: "确认", danger: false, needReason: true, nameRequired: true, reasonPlaceholder: "" }
);
const emit = defineEmits<{ "update:visible": [value: boolean] }>();

const reason = ref("");
const typedName = ref("");
const error = ref("");
const tier = computed(() => {
  const base = confirmTier(props.env);
  return { ...base, requiresName: base.requiresName && props.nameRequired };
});
const canSubmit = computed(
  () => (!props.needReason || reason.value.trim() !== "") && nameConfirmed(tier.value, typedName.value, props.comboName)
);

// 重新打开或关闭都使进行中的提交失效：迟到的响应不能关闭用户重新打开的对话框。
const scope = createSubmitScope();
watch(
  () => props.visible,
  () => {
    scope.invalidate();
    reason.value = "";
    typedName.value = "";
    error.value = "";
  }
);

async function handleOk() {
  if (!canSubmit.value) return false;
  const submission = scope.begin();
  error.value = "";
  try {
    const result = await withDialogErrors(() => props.submit(reason.value.trim()));
    if (!submission.isCurrent()) return false;
    return result !== false;
  } catch (cause) {
    if (submission.isCurrent()) error.value = cause instanceof Error ? cause.message : "请求失败，请稍后重试";
    return false;
  }
}
</script>

<style src="./trade-confirm.css"></style>
<style scoped>
.tx-confirm-block {
  margin-top: var(--moox-space-3);
}

.tx-confirm-block:first-child {
  margin-top: 0;
}

.tx-confirm-env {
  margin-left: var(--moox-space-3);
  font-size: 13px;
}
</style>
