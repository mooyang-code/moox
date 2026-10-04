import { readFileSync } from "node:fs";
import type { FullConfig } from "@playwright/test";

type RemoteCredentials = {
  base_url: string;
  username: string;
  password: string;
};

function readRemoteCredentials(): RemoteCredentials {
  let value: unknown;
  try {
    value = JSON.parse(readFileSync(0, "utf8"));
  } catch {
    throw new Error("remote_playwright_credentials_invalid");
  }
  if (!value || typeof value !== "object") throw new Error("remote_playwright_credentials_invalid");
  const credentials = value as Partial<RemoteCredentials>;
  let baseURL: URL;
  try {
    baseURL = new URL(credentials.base_url || "");
  } catch {
    throw new Error("remote_playwright_credentials_invalid");
  }
  if (
    !["http:", "https:"].includes(baseURL.protocol) ||
    baseURL.username ||
    baseURL.password ||
    baseURL.search ||
    baseURL.hash ||
    !credentials.username ||
    !credentials.password
  ) {
    throw new Error("remote_playwright_credentials_invalid");
  }
  return {
    base_url: baseURL.toString().replace(/\/$/, ""),
    username: credentials.username,
    password: credentials.password
  };
}

export default function globalSetup(_config: FullConfig) {
  if (process.env.MOOX_REMOTE_PLAYWRIGHT !== "1") return;
  const credentials = readRemoteCredentials();
  process.env.MOOX_REMOTE_BASE_URL = credentials.base_url;
  process.env.MOOX_REMOTE_USERNAME = credentials.username;
  process.env.MOOX_REMOTE_PASSWORD = credentials.password;
}
