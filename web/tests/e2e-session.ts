import type { Page } from "@playwright/test";

export async function installE2ESession(page: Page, selectedSpaceId: string) {
  await page.route("**/__e2e_session__", route =>
    route.fulfill({ contentType: "text/html", body: "<!doctype html><title>session setup</title>" })
  );
  await page.goto("/__e2e_session__");
  await page.evaluate(async spaceId => {
    const expiresAt = Math.floor(Date.now() / 1000) + 3600;
    localStorage.setItem("user-info", JSON.stringify({ token: "e2e-token", sessionId: "e2e-session", expiresAt }));
    localStorage.setItem("spaceStore", JSON.stringify({ selectedSpaceId: spaceId, spaces: [] }));
    const key = await crypto.subtle.importKey(
      "raw",
      new TextEncoder().encode("0".repeat(64)),
      { name: "HMAC", hash: "SHA-256" },
      false,
      ["sign"]
    );
    await new Promise<void>((resolve, reject) => {
      const request = indexedDB.open("moox-request-signing", 1);
      request.onupgradeneeded = () => request.result.createObjectStore("sessions", { keyPath: "sessionId" });
      request.onerror = () => reject(request.error);
      request.onsuccess = () => {
        const db = request.result;
        const tx = db.transaction("sessions", "readwrite");
        tx.objectStore("sessions").put({ sessionId: "e2e-session", key, expiresAt });
        tx.oncomplete = () => {
          db.close();
          resolve();
        };
        tx.onerror = () => reject(tx.error);
      };
    });
  }, selectedSpaceId);
}
