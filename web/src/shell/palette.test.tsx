import { act, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { startStream, stopStream } from "@/api/stream";
import { ui } from "@/api/ui-store";
import { moduleById } from "@/plugins/registry";
import {
  envelope,
  type FakeEventSource,
  type FetchStub,
  installEventSource,
  pluginsEnvelope,
  renderApp,
  stubFetch,
} from "@/test/harness";
import { shimRows, withState } from "@/test/shims";
import { CommandPalette } from "./command-palette";

let ES: typeof FakeEventSource;
let me: FetchStub;

beforeEach(() => {
  ES = installEventSource();
  me = stubFetch({
    "GET /api/v1/plugins": () => new Promise(() => {}),
    "GET /api/v1/me": { user: "u", role: "admin", scopes: ["read"], ui: { iconBaseUrl: "" } },
  });
});

afterEach(() => {
  act(() => ui.setPalette(false));
  stopStream();
});

function connect(rows = shimRows()): FakeEventSource {
  startStream();
  const es = ES.instances.at(-1);
  if (!es) throw new Error("no EventSource");
  es.open();
  es.emit("core.plugins", pluginsEnvelope(rows));
  es.emit(
    "proxmox.vms",
    envelope("proxmox.vms", {
      vms: [
        { vmid: 101, name: "media", template: false },
        { vmid: 9000, name: "base-image", template: true },
      ],
    }),
  );
  es.emit(
    "kubernetes.cluster",
    envelope("kubernetes.cluster", {
      nodes: [{ name: "node-a", ip: "192.0.2.10" }],
      workloads: [
        {
          kind: "Deployment",
          namespace: "media",
          name: "app",
          restartable: true,
          protected: false,
        },
        {
          kind: "Deployment",
          namespace: "kube-system",
          name: "dns",
          restartable: false,
          protected: true,
        },
      ],
      pods: [{ namespace: "media", name: "app-0" }],
    }),
  );
  es.emit(
    "kubernetes.apps",
    envelope("kubernetes.apps", {
      apps: [{ name: "Wiki", href: "https://wiki.example.com", group: "docs" }],
    }),
  );
  es.emit(
    "flux.objects",
    envelope("flux.objects", {
      sources: [{ kind: "GitRepository", namespace: "gitops", name: "root" }],
      kustomizations: [{ kind: "Kustomization", namespace: "gitops", name: "apps" }],
      helmReleases: [{ kind: "HelmRelease", namespace: "media", name: "player" }],
      verifiedSources: [{ kind: "GitRepository", namespace: "gitops", name: "root" }],
    }),
  );
  return es;
}

async function open() {
  act(() => ui.setPalette(true));
  const dialog = await screen.findByRole("dialog");
  // The palette gates Restart on the role, so /me is read into the query cache first.
  await waitFor(() => expect(me.callsTo("GET /api/v1/me")).toHaveLength(1));
  await act(async () => {});
  return dialog;
}

function headings(dialog: HTMLElement): string[] {
  return [...dialog.querySelectorAll("[cmdk-group-heading]")].map((h) => h.textContent ?? "");
}

function itemsOf(dialog: HTMLElement, heading: string): string[] {
  const group = [...dialog.querySelectorAll("[cmdk-group]")].find(
    (g) => g.querySelector("[cmdk-group-heading]")?.textContent === heading,
  );
  return [...(group?.querySelectorAll("[cmdk-item]") ?? [])].map((i) => i.textContent ?? "");
}

describe("command palette", () => {
  it("shows the pages of visible entries and the groups of visible modules, in today's order", async () => {
    connect();
    renderApp(<CommandPalette />);
    const dialog = await open();
    expect(headings(dialog)).toEqual([
      "Pages",
      "Actions",
      "VMs",
      "Nodes",
      "Workloads",
      "Pods",
      "Apps",
      "Flux",
      "Navigate",
    ]);
    expect(itemsOf(dialog, "Pages")).toEqual([
      "Overview",
      "VMs",
      "Cluster",
      "GitOps",
      "Hardware",
      "Storage",
      "Alerts",
      "Status",
      "Apps",
    ]);
    expect(itemsOf(dialog, "VMs")).toEqual(["media101"]); // the template is left out
    expect(itemsOf(dialog, "Nodes")).toEqual(["node-a192.0.2.10"]);
    expect(itemsOf(dialog, "Pods")).toEqual(["media/app-0"]);
  });

  it("merges the restart and reconcile items of two modules into one Actions group, reconcile first", async () => {
    connect();
    renderApp(<CommandPalette />);
    const dialog = await open();
    expect(headings(dialog).filter((h) => h === "Actions")).toHaveLength(1);
    // Kubernetes comes before flux in module order, but flux ranks its item first, as in v0.5.0.
    expect(itemsOf(dialog, "Actions")).toEqual([
      "Reconcile gitops/rootGitRepository",
      "Restart media/app",
    ]);
  });

  it("keeps module order between items of the same rank", async () => {
    const es = connect();
    es.emit(
      "kubernetes.cluster",
      envelope("kubernetes.cluster", {
        nodes: [],
        workloads: ["a", "b", "c"].map((name) => ({
          kind: "Deployment",
          namespace: "media",
          name,
          restartable: true,
          protected: false,
        })),
        pods: [],
      }),
    );
    renderApp(<CommandPalette />);
    const dialog = await open();
    expect(itemsOf(dialog, "Actions")).toEqual([
      "Reconcile gitops/rootGitRepository",
      "Restart media/a",
      "Restart media/b",
      "Restart media/c",
    ]);
  });

  // The build targets Firefox 114, which has no ES2023 array copy methods (toSorted and friends).
  it("builds the palette on a runtime without the ES2023 array copy methods", async () => {
    const methods = ["toSorted", "toReversed", "toSpliced"] as const;
    const saved = methods.map((m) => Object.getOwnPropertyDescriptor(Array.prototype, m));
    for (const m of methods) Reflect.deleteProperty(Array.prototype, m);
    try {
      connect();
      renderApp(<CommandPalette />);
      const dialog = await open();
      expect(itemsOf(dialog, "Actions")).toEqual([
        "Reconcile gitops/rootGitRepository",
        "Restart media/app",
      ]);
    } finally {
      methods.forEach((m, i) => {
        const d = saved[i];
        if (d) Object.defineProperty(Array.prototype, m, d);
      });
    }
  });

  it("draws ids and addresses in the monospace face and kinds and groups in the body face", async () => {
    connect();
    renderApp(<CommandPalette />);
    const dialog = await open();
    const hint = (item: string, text: string) => {
      const row = within(dialog).getByText(item).closest("[cmdk-item]");
      if (!(row instanceof HTMLElement)) throw new Error(`no row ${item}`);
      return within(row).getByText(text);
    };
    expect(hint("media", "101")).toHaveClass("num");
    expect(hint("node-a", "192.0.2.10")).toHaveClass("num");
    expect(hint("media/app", "Deployment")).not.toHaveClass("num");
    expect(hint("player", "HelmRelease")).not.toHaveClass("num");
    expect(hint("Wiki", "docs")).not.toHaveClass("num");
  });

  it("leaves out the groups, items and page of a plugin that is switched off", async () => {
    connect(withState(withState(shimRows(), "proxmox", "disabled"), "flux", "disabled"));
    renderApp(<CommandPalette />);
    const dialog = await open();
    expect(headings(dialog)).toEqual([
      "Pages",
      "Actions",
      "Nodes",
      "Workloads",
      "Pods",
      "Apps",
      "Navigate",
    ]);
    expect(itemsOf(dialog, "Actions")).toEqual(["Restart media/app"]);
    expect(itemsOf(dialog, "Pages")).not.toContain("VMs");
    expect(itemsOf(dialog, "Pages")).not.toContain("GitOps");
  });

  it("drops a plugin's items the moment its row goes disabled, while open", async () => {
    const es = connect();
    renderApp(<CommandPalette />);
    const dialog = await open();
    expect(headings(dialog)).toContain("VMs");

    act(() =>
      es.emit("core.plugins", pluginsEnvelope(withState(shimRows(), "proxmox", "disabled"))),
    );
    expect(headings(dialog)).not.toContain("VMs");
    expect(itemsOf(dialog, "Pages")).not.toContain("VMs");
    expect(within(dialog).queryByText("media")).toBeNull();
  });

  it("opens with Ctrl+K and calls no module while closed", async () => {
    connect();
    const spies = ["proxmox", "kubernetes", "flux"].map((id) => {
      const m = moduleById.get(id);
      if (!m) throw new Error(`no module ${id}`);
      return vi.spyOn(m, "palette");
    });
    renderApp(<CommandPalette />);
    expect(screen.queryByRole("dialog")).toBeNull();
    for (const s of spies) expect(s).not.toHaveBeenCalled();

    await userEvent.keyboard("{Control>}k{/Control}");
    await screen.findByRole("dialog");
    for (const s of spies) expect(s).toHaveBeenCalled();
    for (const s of spies) s.mockRestore();
  });

  it("navigates to a VM and closes", async () => {
    connect();
    const { history } = renderApp(<CommandPalette />);
    const dialog = await open();
    await userEvent.click(within(dialog).getByText("media"));
    expect(history.at(-1)).toBe("/vms/101");
    expect(screen.queryByRole("dialog")).toBeNull();
  });

  it("navigates to a page entry", async () => {
    connect();
    const { history } = renderApp(<CommandPalette />);
    const dialog = await open();
    await userEvent.click(within(dialog).getByText("GitOps"));
    expect(history.at(-1)).toBe("/gitops");
  });

  // Radix keeps the dialog mounted while its exit animation runs; jsdom has no animations, so the
  // content reports one ("exit") only while it is closing, and the test ends it by hand.
  it("keeps its items on screen during the close animation", async () => {
    connect();
    const real = globalThis.getComputedStyle.bind(globalThis);
    const styles = vi.spyOn(globalThis, "getComputedStyle").mockImplementation((el, pseudo) => {
      if (el.getAttribute("data-slot") !== "dialog-content") return real(el, pseudo);
      // Live, like a real CSSStyleDeclaration: Radix reads it again when the dialog closes.
      return {
        get animationName() {
          return el.getAttribute("data-state") === "closed" ? "exit" : "none";
        },
        display: "block",
      } as CSSStyleDeclaration;
    });
    try {
      renderApp(<CommandPalette />);
      const dialog = await open();
      expect(itemsOf(dialog, "VMs")).toEqual(["media101"]);

      act(() => ui.setPalette(false));
      expect(screen.getByRole("dialog")).toBe(dialog); // still animating out
      expect(itemsOf(dialog, "VMs")).toEqual(["media101"]);
      expect(within(dialog).getByRole("combobox")).toBeInTheDocument();

      const end = new Event("animationend", { bubbles: true });
      Object.assign(end, { animationName: "exit" });
      act(() => {
        dialog.dispatchEvent(end);
      });
      expect(screen.queryByRole("dialog")).toBeNull();
    } finally {
      styles.mockRestore();
    }
  });
});
