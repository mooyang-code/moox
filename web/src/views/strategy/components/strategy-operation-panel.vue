<template>
  <a-card title="运行控制" :bordered="false">
    <a-alert v-if="!canOperate" type="info" show-icon>当前账号只有查看权限。</a-alert>
    <a-space v-else>
      <a-button v-if="status !== 'disabled'" status="warning" :loading="loading" @click="pause">暂停 Binding</a-button>
      <a-button v-else type="primary" :loading="loading" @click="resume">恢复 Binding</a-button>
      <a-select v-model="mode" style="width: 130px" :disabled="loading"><a-option value="observe">Observe</a-option><a-option value="paper">Paper</a-option><a-option value="live">Live</a-option></a-select>
      <a-button :loading="loading" @click="changeMode">应用模式</a-button>
    </a-space>
    <a-modal v-model:visible="reasonVisible" title="填写操作原因" @ok="submitReason">
      <a-textarea v-model="reason" placeholder="请输入操作原因" :auto-size="{ minRows: 3, maxRows: 6 }" />
    </a-modal>
  </a-card>
</template>

<script setup lang="ts">
import { computed, ref } from 'vue';
import { Message } from '@arco-design/web-vue';
import { useStrategyStore } from '@/store/modules/strategy';
import { useUserInfoStore } from '@/store/modules/user-info';
const props = defineProps<{ bindingId: string; status?: string; currentMode?: string }>();
const emit = defineEmits<{ changed: [] }>();
const store = useStrategyStore();
const userStore = useUserInfoStore();
const canOperate = computed(() => userStore.account.roles.includes('admin'));
const mode = ref(props.currentMode || 'observe');
const reason = ref('');
const pending = ref<'pause' | 'resume' | 'mode'>('pause');
const reasonVisible = ref(false);
const loading = ref(false);
function ask(action: 'pause' | 'resume' | 'mode') { pending.value = action; reason.value = ''; reasonVisible.value = true; }
function pause() { ask('pause'); }
function resume() { ask('resume'); }
function changeMode() { ask('mode'); }
async function submitReason() {
  if (!reason.value.trim()) { Message.warning('请填写操作原因'); return; }
  loading.value = true;
  try {
    if (pending.value === 'pause') await store.pause(props.bindingId, reason.value);
    else if (pending.value === 'resume') await store.resume(props.bindingId, reason.value);
    else await store.changeMode(props.bindingId, mode.value, reason.value);
    reasonVisible.value = false;
    Message.success('操作已提交');
    emit('changed');
  } catch (err) { Message.error(err instanceof Error ? err.message : '操作失败'); }
  finally { loading.value = false; }
}
</script>
