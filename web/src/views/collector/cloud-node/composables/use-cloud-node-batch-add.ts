import { computed, ref } from "vue";

export function useCloudNodeBatchAdd() {
  const visible = ref(false);
  const submitting = ref(false);
  const requested = ref(0);
  const planned = ref(0);
  const canSubmit = computed(
    () => visible.value && requested.value > 0 && planned.value === requested.value && !submitting.value
  );

  function open(count = 0) {
    visible.value = true;
    requested.value = count;
    planned.value = 0;
  }
  function close() {
    visible.value = false;
    submitting.value = false;
  }

  return { visible, submitting, requested, planned, canSubmit, open, close };
}
