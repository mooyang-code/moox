<template>
  <!-- 代码包详情弹窗：执行器列表的“代码包版本”和代码包版本管理共用这一份 -->
  <a-modal
    :visible="visible"
    title="代码包详情"
    :width="800"
    :mask-closable="false"
    :footer="false"
    @update:visible="value => emit('update:visible', value)"
  >
    <div v-if="packageDetail">
      <a-descriptions title="基本信息" :column="2" bordered size="medium" style="margin-bottom: var(--moox-space-4)">
        <a-descriptions-item label="代码包名称">{{ packageDetail.package_name }}</a-descriptions-item>
        <a-descriptions-item label="版本">{{ packageDetail.version }}</a-descriptions-item>
        <a-descriptions-item label="类型">
          <a-tag :color="getPackageTypeColor(packageDetail.package_type)">
            {{ getPackageTypeLabel(packageDetail.package_type) }}
          </a-tag>
        </a-descriptions-item>
        <a-descriptions-item label="运行时环境">{{ packageDetail.runtime }}</a-descriptions-item>
        <a-descriptions-item label="文件大小">{{ formatFileSize(packageDetail.file_size) }}</a-descriptions-item>
        <a-descriptions-item label="状态">
          <a-tag :color="getPackageStatusColor(packageDetail.status)">
            {{ getPackageStatusLabel(packageDetail.status) }}
          </a-tag>
        </a-descriptions-item>
        <a-descriptions-item label="文件MD5" :span="2">
          <a-typography-text copyable>{{ packageDetail.file_md5 || "-" }}</a-typography-text>
        </a-descriptions-item>
        <a-descriptions-item label="描述" :span="2">
          {{ packageDetail.description || "-" }}
        </a-descriptions-item>
      </a-descriptions>

      <a-descriptions title="存储信息" :column="2" bordered size="medium" style="margin-bottom: var(--moox-space-4)">
        <a-descriptions-item label="云账户">
          {{ packageDetail.cloud_account_id || "本地存储" }}
        </a-descriptions-item>
        <a-descriptions-item label="存储类型">
          <a-tag :color="packageDetail.cos_bucket === 'local' ? 'orange' : 'blue'">
            {{ packageDetail.cos_bucket === "local" ? "本地存储" : "COS存储" }}
          </a-tag>
        </a-descriptions-item>
        <a-descriptions-item label="COS地区">
          {{ packageDetail.cos_region || "-" }}
        </a-descriptions-item>
        <a-descriptions-item label="COS桶">
          {{ packageDetail.cos_bucket === "local" ? "-" : packageDetail.cos_bucket || "-" }}
        </a-descriptions-item>
        <a-descriptions-item label="存储路径" :span="2">
          <a-typography-text copyable v-if="packageDetail.cos_path">
            {{ packageDetail.cos_path }}
          </a-typography-text>
          <span v-else>-</span>
        </a-descriptions-item>
        <a-descriptions-item label="原始文件名" :span="2">{{ packageDetail.original_filename || "-" }}</a-descriptions-item>
      </a-descriptions>

      <a-descriptions title="时间信息" :column="2" bordered size="medium">
        <a-descriptions-item label="创建者">{{ packageDetail.created_by }}</a-descriptions-item>
        <a-descriptions-item label="创建时间">{{ formatDateTime(packageDetail.created_time, "UTC") }}</a-descriptions-item>
        <a-descriptions-item label="最后部署时间" :span="2">
          {{ packageDetail.last_deploy_time ? formatDateTime(packageDetail.last_deploy_time, "UTC") : "-" }}
        </a-descriptions-item>
      </a-descriptions>

      <div style="margin-top: var(--moox-space-4); text-align: right">
        <a-space>
          <a-button @click="close">关闭</a-button>
          <a-button
            type="primary"
            status="success"
            :disabled="Number(packageDetail.status) !== PACKAGE_STATUS_AVAILABLE"
            :loading="downloading"
            @click="onDownload(packageDetail)"
          >
            <template #icon>
              <icon-download />
            </template>
            <span>{{ downloading ? "下载中..." : "下载" }}</span>
          </a-button>
        </a-space>
      </div>
    </div>
    <div v-else style="text-align: center; padding: var(--moox-space-8)">
      <a-spin :loading="true" />
      <div style="margin-top: var(--moox-space-4)">加载中...</div>
    </div>
  </a-modal>
</template>

<script setup lang="ts">
import { ref, watch } from "vue";
import { Message } from "@arco-design/web-vue";
import { downloadPackageByURL, getFunctionPackageDetail, type FunctionPackage } from "@/api/function-package";
import { StaleSpaceRequestError } from "@/api/admin/stale-space";
import { createLatestRequestGuard } from "@/utils/latest-request";
import {
  PACKAGE_STATUS_AVAILABLE,
  formatDateTime,
  formatFileSize,
  getPackageStatusColor,
  getPackageStatusLabel,
  getPackageTypeColor,
  getPackageTypeLabel
} from "./function-instance-model";

const props = defineProps<{
  visible: boolean;
  packageId?: string;
}>();

const emit = defineEmits<{
  "update:visible": [value: boolean];
}>();

const packageDetail = ref<FunctionPackage | null>(null);
const downloading = ref(false);
const detailGuard = createLatestRequestGuard();

// 打开或换了代码包就重新读详情；关闭时作废在途请求，迟到的响应不能回写已关闭的弹窗
watch(
  () => [props.visible, props.packageId] as const,
  ([visible, packageId]) => {
    detailGuard.invalidate();
    packageDetail.value = null;
    if (visible && packageId) void loadDetail(packageId);
  },
  { immediate: true }
);

async function loadDetail(packageId: string) {
  const request = detailGuard.begin();
  try {
    const detail = await getFunctionPackageDetail(packageId);
    if (!request.isLatest()) return;
    if (!detail) throw new Error("获取详情失败");
    packageDetail.value = detail;
  } catch (error) {
    // 切换空间造成的取消、或已被新的详情请求取代：不弹失败提示，也不关闭新的详情
    if (error instanceof StaleSpaceRequestError || !request.isLatest()) return;
    console.error("获取代码包详情失败:", error);
    Message.error({ content: "获取代码包详情失败", duration: 5000 });
    emit("update:visible", false);
  }
}

function close() {
  emit("update:visible", false);
}

async function onDownload(pkg: FunctionPackage) {
  if (downloading.value) return;
  downloading.value = true;
  try {
    await downloadPackageByURL(pkg.package_id);
  } catch (error) {
    console.error("下载失败:", error);
    const errorMessage = error instanceof Error ? error.message : "未知错误";
    Message.error({ content: `下载失败: ${errorMessage}`, duration: 5000 });
  } finally {
    downloading.value = false;
  }
}
</script>
