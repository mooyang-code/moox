<template>
  <DatasetDefinitions
    page-title="复合因子数据集"
    owner-module="factor"
    dataset-role="merged_factor"
    managed-by="factor"
    :filter-owner-modules="['factor']"
    :filter-dataset-roles="['merged_factor']"
    include-dataset-id-prefix="mdataset_"
  />
</template>

<script setup lang="ts">
import DatasetDefinitions from "@/views/data/datasets/index.vue";

defineOptions({ name: "FactorDatasets" });
</script>
