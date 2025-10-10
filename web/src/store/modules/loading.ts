import { defineStore } from 'pinia';
import { ref, readonly } from 'vue';


export const useLoadingStore = defineStore('loading', () => {
  // State
  const routeLoading = ref(false);
  const pageLoading = ref(false);

  // Actions
  const setRouteLoading = (loading: boolean) => {
    routeLoading.value = loading;
  };

  const setPageLoading = (loading: boolean) => {
    pageLoading.value = loading;
  };

  const showRouteLoading = () => {
    routeLoading.value = true;
  };

  const hideRouteLoading = () => {
    routeLoading.value = false;
  };

  const showPageLoading = () => {
    pageLoading.value = true;
  };

  const hidePageLoading = () => {
    pageLoading.value = false;
  };

  return {
    routeLoading: readonly(routeLoading),
    pageLoading: readonly(pageLoading),
    setRouteLoading,
    setPageLoading,
    showRouteLoading,
    hideRouteLoading,
    showPageLoading,
    hidePageLoading
  };
});
