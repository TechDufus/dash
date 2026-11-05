import { describe, expect, it } from "vitest";
import { api } from "@/api/client";
import type { SettingField, SettingsPluginView } from "@/api/types";
import { stubFetch } from "@/test/harness";
import { settingsPaths } from "./api";
import {
  EXPORT_YAML,
  hardwareView,
  lockedField,
  proxmoxView,
  readonlyProxmoxView,
  readonlySettingsView,
  settingsRoutes,
  settingsView,
} from "./fixtures";

type Schema = { [key: string]: unknown };

/** The subschema of a dotted path inside a plugin's document, following $ref into $defs. */
function schemaAt(doc: Schema, rel: string): Schema | undefined {
  const defs = (doc.$defs ?? {}) as Record<string, Schema>;
  const deref = (s: Schema | undefined): Schema | undefined => {
    const ref = s?.$ref;
    return typeof ref === "string" ? { ...defs[ref.replace("#/$defs/", "")], ...s } : s;
  };
  let cur: Schema | undefined = doc;
  for (const seg of rel.split(".")) {
    const props = deref(cur)?.properties as Record<string, Schema> | undefined;
    cur = deref(props?.[seg]);
  }
  return cur;
}

function checkFields(view: SettingsPluginView) {
  const prefix = `plugins.${view.id}.`;
  for (const f of view.fields) {
    expect(f.path.startsWith(prefix), f.path).toBe(true);
    const s = schemaAt(view.schema, f.path.slice(prefix.length));
    expect(s, `${f.path} has no schema`).toBeDefined();
    expect(s?.type, f.path).toBe(f.type);
    expect(!!s?.["x-dash-secret"], f.path).toBe(!!f.secret);
  }
}

function checkInvariants(fields: SettingField[]) {
  for (const f of fields) {
    expect(f.locked, f.path).toBe(f.lockedBy !== undefined);
    if (f.secret) expect(f, f.path).not.toHaveProperty("value");
    if (f.source === "env" || f.source === "file") expect(f.ref, f.path).toBeTruthy();
  }
}

describe("settings fixtures", () => {
  it.each([
    ["proxmox", proxmoxView()],
    ["hardware", hardwareView()],
    ["read-only proxmox", readonlyProxmoxView()],
  ])("%s: every field is a leaf of the schema with its type", (_, view) => {
    checkFields(view);
    checkInvariants(view.fields);
  });

  it("marks the destination leaves and the secret in the proxmox schema", () => {
    const doc = proxmoxView().schema;
    expect(schemaAt(doc, "url")?.format).toBe("uri");
    expect(schemaAt(doc, "url")?.["x-dash-dest"]).toBeUndefined();
    expect(schemaAt(doc, "tls.caFile")?.["x-dash-dest"]).toBe(true);
    expect(schemaAt(doc, "tokenSecret")?.writeOnly).toBe(true);
  });

  it("keeps the views' invariants", () => {
    for (const v of [settingsView(), readonlySettingsView()]) {
      checkInvariants([...v.general, ...v.plugins.map((p) => p.enabled)]);
    }
    const ro = readonlySettingsView();
    expect(ro.store).toMatchObject({ readonly: true, reason: "config" });
    expect(ro.plugins.every((p) => p.enabled.lockedBy === "env")).toBe(true);
  });

  it("answers the settings routes", async () => {
    stubFetch(
      settingsRoutes({
        [`PUT ${settingsPaths.plugin("grafana")}`]: () =>
          lockedField("plugins.grafana.enabled", "env", "DASH_PLUGINS_GRAFANA_ENABLED"),
      }),
    );
    await expect(api.get(settingsPaths.view)).resolves.toEqual(settingsView());
    await expect(api.text(settingsPaths.export)).resolves.toBe(EXPORT_YAML);
    await expect(api.get(settingsPaths.audit, { ring: "unauth" })).resolves.toMatchObject({
      ring: "unauth",
    });
    await expect(
      api.put(settingsPaths.plugin("grafana"), { enabled: true }, { ifMatch: 7 }),
    ).rejects.toMatchObject({
      status: 409,
      code: "locked_field",
      details: { path: "plugins.grafana.enabled", source: "env" },
    });
  });
});
