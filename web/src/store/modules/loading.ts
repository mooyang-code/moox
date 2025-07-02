import { defineStore } from 'pinia';

interface LoadingState {
  routeLoading: boolean;
  pageLoading: boolean;
}

export const useLoadingStore = defineStore('loading', {
  state: (): LoadingState => ({
    routeLoading: false,
    pageLoading: false
  }),

  actions: {
    setRouteLoading(loading: boolean) {
      this.routeLoading = loading;
    },

    setPageLoading(loading: boolean) {
      this.pageLoading = loading;
    },

    // 显示路由加载
    showRouteLoading() {
      this.routeLoading = true;
    },

    // 隐藏路由加载
    hideRouteLoading() {
      this.routeLoading = false;
    },

    // 显示页面加载
    showPageLoading() {
      this.pageLoading = true;
    },

    // 隐藏页面加载
    hidePageLoading() {
      this.pageLoading = false;
    }
  }
});
