<template>
  <a-modal
    v-model:visible="visible"
    title="新建计算任务"
    :width="640"
    :ok-loading="submitting"
    :on-before-ok="submit"
    @before-open="prepare"
  >
    <a-form layout="vertical">
      <a-form-item label="源数据集" required>
        <a-select v-model="form.source_dataset_id" allow-search :loading="datasetsLoading" placeholder="选择已激活的时序数据集">
          <a-option v-for="dataset in sourceDatasets" :key="dataset.dataset_id" :value="dataset.dataset_id">
            {{ dataset.name || dataset.dataset_id }}（{{ dataset.dataset_id }}）
          </a-option>
        </a-select>
      </a-form-item>
      <a-form-item label="频率">
        <a-input :model-value="sourceFreq" disabled placeholder="由源数据集决定" />
      </a-form-item>
      <a-form-item label="任务名称" required :validate-status="nameError ? 'error' : undefined" :help="nameError">
        <a-input v-model="form.name" :max-length="10" show-word-limit placeholder="含汉字，不超过 10 个字，空间内唯一" />
        <template #extra>任务名称直接用作结果数据集的名称，创建后不能修改。</template>
      </a-form-item>
    </a-form>
  </a-modal>
</template>

<script setup lang="ts">
import { createSubmitScope } from "@/utils/submit-scope";
import { computed, onBeforeUnmount, reactive, ref, watch } from "vue";
import { Message } from "@arco-design/web-vue";
import { createFactorTask } from "@/api/factor";
import { readAllPages } from "@/api/read-all-pages";
import { listDatasets } from "@/api/storage/metadata";
import type { Dataset } from "@/api/storage/types";
import { useSpaceStore } from "@/store/modules/space";
import { isTimeSeriesDataKind } from "@/views/data/shared/metadata-utils";
import { validateTaskName } from "./compute-tasks-model";

defineOptions({ name: "FactorTaskCreateModal" });

const emit = defineEmits<{ (e: "created", taskId: string): void }>();
const visible = defineModel<boolean>("visible", { default: false });

const spaceStore = useSpaceStore();
const sourceDatasets = ref<Dataset[]>([]);
const datasetsLoading = ref(false);
const submitting = ref(false);
const form = reactive({ source_dataset_id: "", name: "" });

// 输入过之后才提示名称错误，避免一打开就满屏红字。
const nameError = computed(() => (form.name ? validateTaskName(form.name) : ""));

// A Dataset has exactly one frequency, so the factor task takes the source's. 任务的标的就是源数据集当前的标的。
const sourceFreq = computed(() => sourceDatasets.value.find(item => item.dataset_id === form.source_dataset_id)?.frequency || "");

async function loadSourceDatasets(spaceId: string) {
  const items = await readAllPages(async page => {
    const rsp = await listDatasets({ space_id: spaceId, page: { page, size: 500 } });
    return { items: rsp.datasets || [], hasMore: Boolean(rsp.page_result?.has_more) };
  });
  return items.filter(
    item =>
      item.status === "active" &&
      isTimeSeriesDataKind(item.data_kind) &&
      Boolean(item.frequency) &&
      item.attributes?.dataset_role !== "factor_result"
  );
}

async function prepare() {
  form.source_dataset_id = "";
  form.name = "";
  // 打开时先清空：上一次（可能属于其他空间）的选项不能在新空间加载失败后继续可选
  sourceDatasets.value = [];
  const spaceId = spaceStore.selectedSpaceId;
  if (!spaceId) return;
  datasetsLoading.value = true;
  try {
    const items = await loadSourceDatasets(spaceId);
    if (!unmounted) sourceDatasets.value = items;
  } catch (error) {
    if (!unmounted) Message.error(error instanceof Error ? error.message : "数据集加载失败");
  } finally {
    if (!unmounted) datasetsLoading.value = false;
  }
}

async function submit() {
  const spaceId = spaceStore.selectedSpaceId;
  if (!spaceId || !form.source_dataset_id || !sourceFreq.value) {
    Message.warning("请选择源数据集");
    return false;
  }
  const invalidName = validateTaskName(form.name);
  if (invalidName) {
    Message.warning(invalidName);
    return false;
  }
  const submission = createScope.begin();
  submitting.value = true;
  try {
    const created = await createFactorTask({
      space_id: spaceId,
      source_dataset_id: form.source_dataset_id,
      frequency: sourceFreq.value,
      name: form.name
    });
    if (!submission.isCurrent()) {
      // 弹窗已关闭/重新打开或页面已卸载（含切空间重建）：不再发出 created 或打开详情
      Message.info(`计算任务 ${created.task_id} 已创建；弹窗已关闭/重新打开或空间已切换，不再打开详情`);
      return false;
    }
    Message.success(`计算任务已创建：${created.task_id}`);
    emit("created", created.task_id);
    return true;
  } catch (error) {
    if (!submission.isCurrent()) return false;
    Message.error(error instanceof Error ? error.message : "创建计算任务失败");
    return false;
  } finally {
    if (submission.isCurrent()) submitting.value = false;
  }
}

// 提交作用域：弹窗打开/关闭/卸载都会使进行中的创建失效，旧响应不能再发出 created 或关闭新弹窗
const createScope = createSubmitScope();
let unmounted = false;
watch(visible, () => {
  createScope.invalidate();
  submitting.value = false;
});
onBeforeUnmount(() => {
  unmounted = true;
  createScope.invalidate();
});
</script>
