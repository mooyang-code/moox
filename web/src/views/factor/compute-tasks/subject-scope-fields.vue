<template>
  <a-form-item label="对象范围" required>
    <a-radio-group v-model="mode" type="button">
      <a-radio value="all">全部对象</a-radio>
      <a-radio value="include">指定对象</a-radio>
    </a-radio-group>
  </a-form-item>
  <a-form-item v-if="mode === 'include'" label="对象 ID" required>
    <a-input-tag v-model="subjects" allow-clear placeholder="输入对象 ID 后回车" />
  </a-form-item>
</template>

<script setup lang="ts">
import type { SubjectMode } from "@/api/factor/types";

defineOptions({ name: "FactorSubjectScopeFields" });

const mode = defineModel<SubjectMode>("mode", { required: true });
const subjects = defineModel<string[]>("subjects", { required: true });
</script>
