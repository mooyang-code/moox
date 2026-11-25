<template>
  <div class="moox-page">
    <div class="moox-inner">
      <div class="page-head">
        <div>
          <h2>空间管理</h2>
        </div>
        <a-space>
          <a-button type="primary" status="success" @click="openCreate">
            <template #icon><icon-plus /></template>
            创建空间
          </a-button>
        </a-space>
      </div>

      <a-table
        row-key="space_id"
        size="small"
        :bordered="{ cell: true }"
        :loading="loading"
        :data="rows"
        :pagination="pagination"
        :scroll="{ x: 'max-content' }"
        @page-change="onPageChange"
        @page-size-change="onPageSizeChange"
      >
        <template #columns>
          <a-table-column title="空间ID" data-index="space_id" :width="180" />
          <a-table-column title="名称" :width="180">
            <template #cell="{ record }">
              {{ record.name }}<span v-if="record.space_id === spaceStore.selectedSpaceId" class="current-mark">（当前）</span>
            </template>
          </a-table-column>
          <a-table-column title="负责人" data-index="owner" :width="120" />
          <a-table-column title="市场" data-index="market" :width="110" />
          <a-table-column title="时区" data-index="timezone" :width="150" />
          <a-table-column title="状态" :width="90">
            <template #cell="{ record }">
              <StatusMark :tone="record.status === 'active' ? 'ok' : ''">{{ statusLabel(record.status) }}</StatusMark>
            </template>
          </a-table-column>
          <a-table-column title="更新时间" :width="180">
            <template #cell="{ record }">{{ formatInstant(record.updated_at, "UTC") }}</template>
          </a-table-column>
          <a-table-column title="操作" :width="220" align="center" :fixed="'right'">
            <template #cell="{ record }">
              <a-space>
                <a-button
                  size="mini"
                  type="text"
                  :disabled="record.status !== 'active'"
                  @click="spaceStore.setSelectedSpace(record.space_id)"
                >
                  设为当前
                </a-button>
                <a-button size="mini" type="text" @click="openEdit(record)">编辑</a-button>
                <a-button size="mini" type="text" status="danger" @click="confirmDelete(record)">删除</a-button>
              </a-space>
            </template>
          </a-table-column>
        </template>
      </a-table>

      <a-modal v-model:visible="visible" width="min(720px, 100vw)" :title="modalTitle" :on-before-ok="submit">
        <a-form :model="form" auto-label-width>
          <a-form-item field="space_id" label="空间ID" required>
            <a-input v-model="form.space_id" :disabled="editing" placeholder="例如 cn_stock" />
          </a-form-item>
          <a-form-item field="name" label="名称" required>
            <a-input v-model="form.name" placeholder="例如 A股交易空间" />
          </a-form-item>
          <a-form-item field="description" label="描述">
            <a-textarea v-model="form.description" :auto-size="{ minRows: 3, maxRows: 5 }" />
          </a-form-item>
          <a-form-item field="owner" label="负责人">
            <a-input v-model="form.owner" />
          </a-form-item>
          <a-form-item field="market" label="市场">
            <a-input v-model="form.market" placeholder="例如 CN、US、CRYPTO" />
          </a-form-item>
          <a-form-item field="timezone" label="时区" required :extra="uncommonZoneHint(form.timezone || '')">
            <a-select v-model="form.timezone" placeholder="选择时区：页面上这个空间的时间按它显示">
              <a-option v-for="item in SPACE_TIMEZONES" :key="item.value" :value="item.value">{{ item.label }}</a-option>
              <a-option v-if="uncommonZoneHint(form.timezone || '')" :value="form.timezone">{{ form.timezone }}</a-option>
            </a-select>
          </a-form-item>
          <a-form-item field="status" label="状态">
            <a-select v-model="form.status">
              <a-option v-for="item in statusOptions" :key="item.value" :value="item.value">{{ item.label }}</a-option>
            </a-select>
          </a-form-item>
        </a-form>
      </a-modal>
    </div>
  </div>
</template>

<script setup lang="ts">
import { reportError } from "@/api/http";
import StatusMark from "@/views/trading/accounts/shared/status-mark.vue";
import { createSubmitScope } from "@/utils/submit-scope";
import { computed, onMounted, reactive, ref, watch } from "vue";
import { Message, Modal } from "@arco-design/web-vue";
import { SPACE_TIMEZONES, uncommonZoneHint } from "@/utils/space-zones";
import { createSpace, deleteSpace, listSpaces, updateSpace } from "@/api/storage/spaces";
import type { Space } from "@/api/storage/spaces";
import { useSpaceStore } from "@/store/modules/space";
import { applyPageResult, defaultPagination, statusLabel, statusOptions } from "@/views/data/shared/metadata-utils";
import { formatInstant } from "@/utils/time-zone";

defineOptions({ name: "SettingsSpaces" });

const spaceStore = useSpaceStore();
const rows = ref<Space[]>([]);
const loading = ref(false);
const visible = ref(false);
const editing = ref(false);
const pagination = reactive(defaultPagination());

const form = reactive<Space>({
  space_id: "",
  name: "",
  description: "",
  owner: "",
  market: "",
  timezone: "",
  status: "active",
  attributes: {}
});

const modalTitle = computed(() => (editing.value ? "编辑空间" : "创建空间"));

async function load() {
  loading.value = true;
  try {
    const rsp = await listSpaces({ page: { page: pagination.current, size: pagination.pageSize } });
    rows.value = rsp.spaces || [];
    applyPageResult(pagination, rsp.page_result);
    await spaceStore.loadSpaces();
  } finally {
    loading.value = false;
  }
}

function resetForm() {
  Object.assign(form, {
    space_id: "",
    name: "",
    description: "",
    owner: "",
    market: "",
    timezone: "",
    status: "active",
    attributes: {}
  });
}

function openCreate() {
  editing.value = false;
  resetForm();
  visible.value = true;
}

function openEdit(record: Space) {
  editing.value = true;
  Object.assign(form, {
    space_id: record.space_id,
    name: record.name,
    description: record.description || "",
    owner: record.owner || "",
    market: record.market || "",
    timezone: record.timezone || "",
    status: record.status || "active",
    // Storage 的修改是整体替换：扩展属性原样带回
    attributes: { ...(record.attributes || {}) }
  });
  visible.value = true;
}

/** 弹窗的“确定”：校验或保存失败时返回 false，弹窗保持打开，填的内容不丢。 */
async function submit(): Promise<boolean> {
  if (!form.space_id || !form.name) {
    Message.warning("请填写空间ID和名称");
    return false;
  }
  if (!form.timezone) {
    Message.warning("请选择时区：页面上这个空间的时间按它显示");
    return false;
  }
  // 固定提交快照：保存期间表单可能被关闭并重新打开去编辑另一个空间，旧提交只能用自己提交的数据
  const submission = spaceScope.begin();
  const submitted = { ...form };
  const creating = !editing.value;
  try {
    if (creating) await createSpace(submitted);
    else await updateSpace(submitted);
  } catch (error) {
    reportError(error);
    return false;
  }
  Message.success("空间已保存");
  if (!submission.isCurrent()) return false;
  void load().then(() => {
    // 只有新建的空间才切过去；编辑别的空间不能把当前空间悄悄切走
    if (creating && submitted.status === "active" && submission.isCurrent()) spaceStore.setSelectedSpace(submitted.space_id);
  });
  return true;
}

/** 删除空间会清空该空间全部数据，二次确认写明后果。 */
function confirmDelete(record: Space) {
  Modal.confirm({
    title: `删除空间“${record.name || record.space_id}”`,
    content:
      "会清空该空间全部数据：数据集、View 与全部行情、因子数据一起删除，不能恢复。采集任务、交易、策略里按这个空间存的记录不会跟着删除。确认删除？",
    okText: "删除",
    okButtonProps: { status: "danger" },
    onBeforeOk: async () => {
      try {
        await deleteSpace(record.space_id);
      } catch (error) {
        reportError(error);
        return false;
      }
      Message.success("空间已删除");
      void load();
      return true;
    }
  });
}

function onPageChange(page: number) {
  pagination.current = page;
  load();
}

function onPageSizeChange(pageSize: number) {
  pagination.current = 1;
  pagination.pageSize = pageSize;
  load();
}

onMounted(load);

// 空间是全局资源：提交作用域只受弹窗打开序号约束
const spaceScope = createSubmitScope({ global: true });
watch(visible, open => {
  if (open) spaceScope.invalidate();
});
</script>

<style scoped>
.page-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: var(--moox-space-2);
}

.page-head h2 {
  margin: 0;
  font-size: 20px;
  font-weight: 600;
}

.page-head span {
  color: var(--color-text-3);
}

.current-mark {
  color: var(--color-text-3);
}
</style>
