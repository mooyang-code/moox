/// <reference types="vite/client" />
declare module "nprogress";
declare module "virtual:space-policy" {
  export const GLOBAL_SERVICES: ReadonlySet<string>;
  export const GLOBAL_METHODS: Record<string, ReadonlySet<string>>;
  export const SPACE_OPTIONAL_METHODS: Record<string, ReadonlySet<string>>;
}
