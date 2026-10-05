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
        <a-select
          v-model="form.source_dataset_id"
          allow-search
          :loading="datasetsLoading"
          placeholder="选择已激活的时序数据集"
          @change="sourceChanged"
        >
          <a-option v-for="dataset in sourceDatasets" :key="dataset.dataset_id" :value="dataset.dataset_id">
            {{ dataset.name || dataset.dataset_id }}（{{ dataset.dataset_id }}）
          </a-option>
        </a-select>
      </a-form-item>
      <a-form-item label="频率" required>
        <a-select v-model="form.freq" :disabled="!availableFreqs.length" placeholder="选择源数据集支持的频率">
          <a-option v-for="freq in availableFreqs" :key="freq" :value="freq">{{ freq }}</a-option>
        </a-select>
      </a-form-item>
      <SubjectScopeFields v-model:mode="form.subject_mode" v-model:subjects="subjects" />
    </a-form>
  </a-modal>
</template>

<script setup lang="ts">
import { computed, reactive, ref } from "vue";
import { Message } from "@arco-design/web-vue";
import { createFactorSet } from "@/api/factor";
import type { SubjectMode } from "@/api/factor/types";
import { listDatasets } from "@/api/storage/metadata";
import type { Dataset } from "@/api/storage/types";
import { useSpaceStore } from "@/store/modules/space";
import { isTimeSeriesDataKind } from "@/views/data/shared/metadata-utils";
import SubjectScopeFields from "./subject-scope-fields.vue";

defineOptions({ name: "FactorSetCreateModal" });

const emit = defineEmits<{ (e: "created", setId: string): void }>();
const visible = defineModel<boolean>("visible", { default: false });

const spaceStore = useSpaceStore();
const sourceDatasets = ref<Dataset[]>([]);
const datasetsLoading = ref(false);
const submitting = ref(false);
const subjects = ref<string[]>([]);
const form = reactive({ source_dataset_id: "", freq: "", subject_mode: "all" as SubjectMode });

const availableFreqs = computed(() => sourceDatasets.value.find(item => item.dataset_id === form.source_dataset_id)?.freqs || []);

async function loadSourceDatasets(spaceId: string) {
  const items: Dataset[] = [];
  for (let page = 1; ; page += 1) {
    const rsp = await listDatasets({ space_id: spaceId, page: { page, size: 500 } });
    items.push(...(rsp.datasets || []));
    if (!rsp.page_result?.has_more || !(rsp.datasets || []).length) break;
  }
  return items.filter(
    item =>
      item.status === "active" &&
      isTimeSeriesDataKind(item.data_kind) &&
      (item.freqs || []).length > 0 &&
      item.attributes?.dataset_role !== "factor_result"
  );
}

async function prepare() {
  form.source_dataset_id = "";
  form.freq = "";
  form.subject_mode = "all";
  subjects.value = [];
  const spaceId = spaceStore.selectedSpaceId;
  if (!spaceId) return;
  datasetsLoading.value = true;
  try {
    const items = await loadSourceDatasets(spaceId);
    if (spaceId === spaceStore.selectedSpaceId) sourceDatasets.value = items;
  } catch (error) {
    Message.error(error instanceof Error ? error.message : "数据集加载失败");
  } finally {
    datasetsLoading.value = false;
  }
}

function sourceChanged() {
  form.freq = availableFreqs.value.includes(form.freq) ? form.freq : availableFreqs.value[0] || "";
}

async function submit() {
  const spaceId = spaceStore.selectedSpaceId;
  if (!spaceId || !form.source_dataset_id || !form.freq) {
    Message.warning("请选择源数据集和频率");
    return false;
  }
  if (form.subject_mode === "include" && !subjects.value.length) {
    Message.warning("指定对象模式至少需要一个对象 ID");
    return false;
  }
  submitting.value = true;
  try {
    const created = await createFactorSet({
      space_id: spaceId,
      source_dataset_id: form.source_dataset_id,
      freq: form.freq,
      subject_mode: form.subject_mode,
      subjects: form.subject_mode === "include" ? [...subjects.value] : []
    });
    Message.success(`计算任务已创建：${created.set_id}`);
    emit("created", created.set_id);
    return true;
  } catch (error) {
    Message.error(error instanceof Error ? error.message : "创建计算任务失败");
    return false;
  } finally {
    submitting.value = false;
  }
}
</script>
