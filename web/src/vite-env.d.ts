/// <reference types="vite/client" />
declare module "nprogress";
declare module "virtual:space-policy" {
  export type GeneratedSpacePolicy = "global" | "space_optional" | "space_required";
  export const SERVICE_SPACE_POLICIES: Readonly<Record<string, GeneratedSpacePolicy>>;
  export const METHOD_SPACE_POLICIES: Readonly<Record<string, Readonly<Record<string, GeneratedSpacePolicy>>>>;
}
