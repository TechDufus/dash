import { describe, expect, it } from "vitest";
import { proxmoxView } from "./fixtures";
import {
  buildTree,
  collect,
  type Leaf,
  leafForPath,
  leavesOf,
  parseRaw,
  REENTER,
  resolveRef,
} from "./form-model";

const leaves = leavesOf(buildTree(proxmoxView()));
const at = (rel: string) => leaves.find((l) => l.rel === rel);

describe("form model", () => {
  it("resolves $ref into $defs and keeps the property's own description", () => {
    const caFile = at("tls.caFile");
    expect(caFile).toMatchObject({
      kind: "string",
      dest: true,
      path: "plugins.proxmox.tls.caFile",
    });
    expect(at("criticalVMs.ids")).toMatchObject({ kind: "list", itemType: "integer" });
    expect(at("storage.source")).toMatchObject({ kind: "enum", enum: ["api", "prometheus"] });
    expect(at("tokenSecret")?.kind).toBe("secret");
  });

  // Pins D16 as the server applies it: any raw change of a format: uri leaf moves the destination;
  // the browser parses no URL, so it cannot disagree with the server about a scheme, a path, an IP
  // literal or an IDN spelled another way.
  it.each([
    ["a path change", "https://pve.example.com:8006/api"],
    ["a host change", "https://pve2.example.com:8006"],
    ["a port change", "https://pve.example.com:8007"],
    ["a scheme-only change", "http://pve.example.com:8006"],
    ["an explicit default port", "https://pve.example.com:443"],
  ])("treats %s of the URL as a destination change", (_, url) => {
    const { changes, errors } = collect(leaves, { url }, {});
    expect(changes.destChanged).toBe(true);
    expect(errors.get("plugins.proxmox.tokenSecret")).toBe(REENTER);
  });

  it.each([
    ["https://[::1]:8006", "https://[0:0:0:0:0:0:0:1]:8006"],
    ["https://127.0.0.1:8006", "https://127.1:8006"],
    ["https://bücher.example:8006", "https://xn--bcher-kva.example:8006"],
  ])("treats %s rewritten as %s as a destination change", (from, to) => {
    const view = proxmoxView();
    const url = view.fields.find((f) => f.path === "plugins.proxmox.url");
    if (url) url.value = from;
    const { changes, errors } = collect(leavesOf(buildTree(view)), { url: to }, {});
    expect(changes.destChanged).toBe(true);
    expect(errors.get("plugins.proxmox.tokenSecret")).toBe(REENTER);
  });

  it("leaves an unchanged URL out of the destination", () => {
    const { changes, errors } = collect(
      leaves,
      { url: "https://pve.example.com:8006" },
      {
        tokenSecret: "s",
      },
    );
    expect(changes).toEqual({ patch: {}, secrets: { tokenSecret: "s" }, destChanged: false });
    expect(errors.size).toBe(0);
  });

  // Pins that a blank number input resets the field (null in the merge patch), never writes 0.
  it("parses a blank number as null, the reset to the default", () => {
    const leaf: Leaf = {
      node: "leaf",
      rel: "n",
      path: "plugins.x.n",
      key: "n",
      kind: "integer",
      uri: false,
      dest: false,
    };
    expect(parseRaw(leaf, "  ")).toEqual({ ok: true, value: null });
    expect(parseRaw({ ...leaf, kind: "number" }, "")).toEqual({ ok: true, value: null });
  });

  // Pins that a property's own description wins over the one of the $defs entry it references.
  it("keeps a property's own description over its definition's", () => {
    const root = { $defs: { TLS: { type: "object", description: "from the definition" } } };
    expect(resolveRef(root, { $ref: "#/$defs/TLS", description: "the property's own" })).toEqual({
      type: "object",
      description: "the property's own",
    });
  });

  it("treats any change of an x-dash-dest leaf as a destination change", () => {
    const { changes, errors } = collect(
      leaves,
      { "tls.insecureSkipVerify": true },
      {
        tokenSecret: "again",
      },
    );
    expect(changes).toEqual({
      patch: { tls: { insecureSkipVerify: true } },
      secrets: { tokenSecret: "again" },
      destChanged: true,
    });
    expect(errors.size).toBe(0);
  });

  it("leaves locked and unchanged leaves out of the patch", () => {
    const { changes } = collect(leaves, { tokenID: "other", node: "pve" }, {});
    expect(changes.patch).toEqual({});
  });

  it("files an error inside a list under the list's leaf", () => {
    expect(leafForPath(leaves, "plugins.proxmox.criticalVMs.ids[2]")?.rel).toBe("criticalVMs.ids");
    expect(leafForPath(leaves, "plugins.proxmox.urlx")).toBeUndefined();
  });
});
