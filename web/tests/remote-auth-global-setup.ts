import { readFileSync } from "node:fs";
import type { FullConfig } from "@playwright/test";

type RemoteCredentials = {
  base_url: string;
  username: string;
  password: string;
};

export default function globalSetup(_config: FullConfig) {
  if (process.env.MOOX_REMOTE_PLAYWRIGHT !== "1") return;
  let credentials: RemoteCredentials;
  try {
    credentials = JSON.parse(readFileSync(0, "utf8")) as RemoteCredentials;
  } catch {
    throw new Error("remote_playwright_credentials_invalid");
  }
  if (!/^https?:\/\/[^\s]+$/.test(credentials.base_url) || !credentials.username || !credentials.password) {
    throw new Error("remote_playwright_credentials_invalid");
  }
  process.env.MOOX_REMOTE_BASE_URL = credentials.base_url;
  process.env.MOOX_REMOTE_USERNAME = credentials.username;
  process.env.MOOX_REMOTE_PASSWORD = credentials.password;
}
