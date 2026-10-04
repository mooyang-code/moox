import { describe, expect, it } from "vitest";
import { RequestGate } from "./request-gate";

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>(done => {
    resolve = done;
  });
  return { promise, resolve };
}

describe("RequestGate", () => {
  it("discards an older delayed response after a newer request starts", async () => {
    const gate = new RequestGate();
    const oldResponse = deferred<string>();
    const committed: string[] = [];
    const oldToken = gate.next();
    const oldCommit = oldResponse.promise.then(value => {
      if (gate.isCurrent(oldToken)) committed.push(value);
    });

    const currentToken = gate.next();
    oldResponse.resolve("old-space");
    await oldCommit;
    if (gate.isCurrent(currentToken)) committed.push("current-space");

    expect(committed).toEqual(["current-space"]);
  });
});
