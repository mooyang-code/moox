/// <reference types="vite/client" />
declare module "*.vue" {
  import { DefineComponent } from "vue";
  const component: DefineComponent<object, object, any>;
  export default component;
}
declare module "vue-i18n";
declare module "@arco-design/color";
declare module "nprogress";
declare module "postcss-preset-env";
