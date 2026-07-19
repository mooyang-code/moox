import { ref } from "vue";

export function useCloudNodeDeploy() {
  const visible = ref(false);
  const submitting = ref(false);
  const selectedPackageId = ref<string>();

  function open(packageId?: string) {
    selectedPackageId.value = packageId;
    visible.value = true;
  }
  function close() {
    visible.value = false;
    submitting.value = false;
  }

  return { visible, submitting, selectedPackageId, open, close };
}
