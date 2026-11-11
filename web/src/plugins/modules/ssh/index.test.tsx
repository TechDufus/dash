import { describe, expect, it } from "vitest";
import type { PaletteCtx } from "../../types";
import { host, hostsData } from "./fixtures";
import ssh from "./index";

describe("the ssh module", () => {
  it("declares its topic, its page and its own settings form", () => {
    expect(Object.keys(ssh.topics)).toEqual(["ssh.hosts"]);
    expect(ssh.routes?.map((r) => r.path)).toEqual(["/hosts/:host?"]);
    expect(ssh.settings).toBeDefined();
  });

  it("lists every host in the palette and opens its page", () => {
    const went: string[] = [];
    const ctx: PaletteCtx = {
      topic: ((name: string) =>
        name === "ssh.hosts"
          ? hostsData([host("alpha"), host("bob/2", "unreachable")])
          : undefined) as PaletteCtx["topic"],
      go: (to) => went.push(to),
      actionAvailable: () => false,
      close: () => {},
    };
    const [group] = ssh.palette?.(ctx) ?? [];
    expect(group?.heading).toBe("Hosts");
    expect(group?.items.map((i) => [i.label, i.hint])).toEqual([
      ["alpha", "Debian GNU/Linux 13 (trixie)"],
      ["bob/2", undefined],
    ]);
    for (const i of group?.items ?? []) i.onSelect();
    expect(went).toEqual(["/hosts/alpha", "/hosts/bob%2F2"]);
  });

  it("lists nothing before the topic arrives", () => {
    const ctx: PaletteCtx = {
      topic: (() => undefined) as PaletteCtx["topic"],
      go: () => {},
      actionAvailable: () => false,
      close: () => {},
    };
    expect(ssh.palette?.(ctx)[0]?.items).toEqual([]);
  });
});
