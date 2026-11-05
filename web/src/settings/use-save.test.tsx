import { describe, expect, it } from "vitest";
import { stubFetch } from "@/test/harness";
import { settingsPaths } from "./api";
import { patchResult, REVISION } from "./fixtures";
import { testRequest, writeChanges } from "./use-save";

const PLUGIN = `PUT ${settingsPaths.plugin("grafana")}`;
const TOKEN = `PUT ${settingsPaths.secret("grafana", "auth.bearerToken")}`;
const PASSWORD = `PUT ${settingsPaths.secret("grafana", "auth.password")}`;

function backend() {
  let revision = REVISION;
  const next = () => {
    revision += 1;
    return patchResult({ revision });
  };
  return stubFetch({ [PLUGIN]: next, [TOKEN]: next, [PASSWORD]: next });
}

describe("writeChanges", () => {
  it("writes the patch, then each secret through its route, each If-Match the last answer", async () => {
    const stub = backend();
    const result = await writeChanges("grafana", REVISION, {
      patch: { timeout: "5s" },
      secrets: { "auth.bearerToken": "tok", "auth.password": " k8s:dash/grafana#password" },
      destChanged: false,
    });
    expect(
      stub.calls.map((c) => [`${c.method} ${c.path}`, c.headers.get("If-Match"), c.body]),
    ).toEqual([
      [PLUGIN, `"${REVISION}"`, { timeout: "5s" }],
      [TOKEN, `"${REVISION + 1}"`, { value: "tok" }],
      [PASSWORD, `"${REVISION + 2}"`, { ref: "k8s:dash/grafana#password" }],
    ]);
    expect(result?.revision).toBe(REVISION + 3);
  });

  it("puts every typed secret into the one patch of a destination change", async () => {
    const stub = backend();
    await writeChanges("grafana", REVISION, {
      patch: { url: "https://grafana.lab.test" },
      secrets: { "auth.bearerToken": "tok", "auth.password": "k8s:dash/grafana#password" },
      destChanged: true,
    });
    expect(
      stub.calls.map((c) => [`${c.method} ${c.path}`, c.headers.get("If-Match"), c.body]),
    ).toEqual([
      [
        PLUGIN,
        `"${REVISION}"`,
        {
          url: "https://grafana.lab.test",
          auth: { bearerToken: "tok", password: "k8s:dash/grafana#password" },
        },
      ],
    ]);
  });

  it("writes nothing for no changes", async () => {
    const stub = backend();
    expect(
      await writeChanges("grafana", REVISION, { patch: {}, secrets: {}, destChanged: false }),
    ).toBeUndefined();
    expect(stub.calls).toEqual([]);
  });
});

describe("testRequest", () => {
  it("carries the patch and the typed secrets keyed by relative field, omitting empty parts", () => {
    expect(testRequest({ patch: {}, secrets: { tokenSecret: "s" }, destChanged: false })).toEqual({
      secrets: { tokenSecret: "s" },
    });
    expect(testRequest({ patch: { node: "pve" }, secrets: {}, destChanged: false })).toEqual({
      patch: { node: "pve" },
    });
  });
});
