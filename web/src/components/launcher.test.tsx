import { act, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";
import type { App, AppsData, LauncherData } from "@/api/types";
import { AppsPage } from "@/pages/apps";
import { connectStream, type TestStream } from "@/plugins/test-util";
import { type FetchStub, pluginInfo, renderApp, stubFetch } from "@/test/harness";
import { AppsSection, InfraSection } from "./launcher";

const BASE = "https://cdn.example.com/icons/png";

const app = (name: string, icon: string): App =>
  ({
    ref: `kubernetes:App/media/httproute/${name}/${name}.example.com`,
    name,
    description: `${name} app`,
    icon,
    href: `https://${name}.example.com`,
    group: "Apps",
    namespace: "media",
  }) as App;

const rows = [
  pluginInfo({ id: "kubernetes", name: "Kubernetes", topics: ["kubernetes.apps"] }),
  pluginInfo({ id: "launcher", name: "Launcher", topics: ["launcher.links"] }),
];

describe("the launcher's icons", () => {
  let stream: TestStream;
  let me: FetchStub;
  afterEach(() => stream.close());

  async function show(iconBaseUrl: string, apps: App[], links: LauncherData["links"] = []) {
    me = stubFetch({
      "GET /api/v1/me": { user: "u", role: "admin", scopes: ["read"], ui: { iconBaseUrl } },
    });
    stream = connectStream();
    renderApp(
      <>
        <AppsSection query="" />
        <InfraSection query="" />
      </>,
    );
    stream.plugins(rows);
    stream.send("kubernetes.apps", { apps } satisfies AppsData);
    stream.send("launcher.links", { links } satisfies LauncherData);
    await screen.findByText(apps[0]?.name ?? "");
    await waitFor(() => expect(me.callsTo("GET /api/v1/me")).toHaveLength(1));
    await act(async () => {}); // /me has been read into the query cache
  }

  const sources = () =>
    [...document.querySelectorAll("img")].map((i) => i.getAttribute("src") ?? "");

  it("requests nothing outside this origin while ui.iconBaseURL is empty", async () => {
    await show(
      "",
      [
        app("grafana", "grafana.png"),
        app("plex", "plex"),
        app("cdn", "https://cdn.example.com/icons/png/cdn.png"),
        app("mdi", "mdi-monitor-dashboard"),
        app("local", "/icons/local.png"),
      ],
      [{ name: "NAS", href: "https://nas.example.com", icon: "nas", description: "" }],
    );
    const srcs = sources();
    expect(srcs).toEqual(["/icons/local.png"]);
    for (const src of srcs) {
      expect(new URL(src, window.location.href).origin).toBe(window.location.origin);
    }
  });

  it("resolves an app's icon token against the base: names with or without an extension", async () => {
    await show("https://cdn.example.com/icons/png", [
      app("grafana", "grafana.png"),
      app("plex", "plex.svg"),
      app("jellyfin", "jellyfin"),
      app("dotted", "a.b.png"),
    ]);
    await waitFor(() => expect(sources()).toHaveLength(4));
    expect(sources().sort()).toEqual(
      [`${BASE}/a.b.png`, `${BASE}/grafana.png`, `${BASE}/jellyfin.png`, `${BASE}/plex.png`].sort(),
    );
  });

  it("draws a letter tile for a homepage-style token or a URL outside the base", async () => {
    await show(BASE, [
      app("mdi", "mdi-monitor-dashboard"),
      app("si", "si-github"),
      app("sh", "sh-plex"),
      app("evil", "https://evil.example.net/a.png"),
      app("under", `${BASE}/under.png`),
    ]);
    await waitFor(() => expect(sources()).toEqual([`${BASE}/under.png`]));
  });

  it("resolves the launcher plugin's links the same way", async () => {
    await show(
      BASE,
      [app("grafana", "")],
      [
        { name: "Proxmox", href: "https://pve.example.com", icon: "proxmox.png", description: "" },
        { name: "Home", href: "https://home.example.com", icon: "mdi-home", description: "" },
      ],
    );
    await waitFor(() => expect(sources()).toEqual([`${BASE}/proxmox.png`]));
  });

  it("shows no icon before /me has arrived", async () => {
    me = stubFetch({ "GET /api/v1/me": () => new Promise(() => {}) });
    stream = connectStream();
    renderApp(<AppsSection query="" />);
    stream.plugins(rows);
    stream.send("kubernetes.apps", { apps: [app("grafana", "grafana.png")] });
    await screen.findByText("grafana");
    expect(sources()).toEqual([]);
  });

  it("draws the Apps page's icons through the same rule: nothing foreign with an empty base", async () => {
    me = stubFetch({
      "GET /api/v1/me": { user: "u", role: "admin", scopes: ["read"], ui: { iconBaseUrl: "" } },
    });
    stream = connectStream();
    renderApp(<AppsPage />);
    stream.plugins(rows);
    stream.send("kubernetes.apps", {
      apps: [app("grafana", "grafana.png"), app("local", "/icons/local.png")],
    } satisfies AppsData);
    stream.send("launcher.links", { links: [] } satisfies LauncherData);
    await screen.findByText("grafana");
    await waitFor(() => expect(me.callsTo("GET /api/v1/me")).toHaveLength(1));
    await act(async () => {});
    expect(sources()).toEqual(["/icons/local.png"]);
  });
});

describe("the launcher's hrefs", () => {
  let stream: TestStream;
  afterEach(() => stream.close());

  const unsafe = ["javascript:alert(1)", "  JaVaScRiPt:alert(1)", "data:text/html,x", "vbscript:x"];

  async function show(apps: App[], links: LauncherData["links"]) {
    stubFetch({
      "GET /api/v1/me": { user: "u", role: "admin", scopes: ["read"], ui: { iconBaseUrl: "" } },
    });
    stream = connectStream();
    renderApp(
      <>
        <AppsSection query="" />
        <InfraSection query="" />
      </>,
    );
    stream.plugins(rows);
    stream.send("kubernetes.apps", { apps } satisfies AppsData);
    stream.send("launcher.links", { links } satisfies LauncherData);
    await screen.findByText(apps[0]?.name ?? "");
  }

  it("keeps an http(s) app and link as anchors", async () => {
    await show(
      [app("grafana", "")],
      [{ name: "NAS", href: "https://nas.example.com", icon: "", description: "" }],
    );
    await screen.findByText("NAS");
    const hrefs = screen.getAllByRole("link").map((a) => a.getAttribute("href"));
    expect(hrefs.sort()).toEqual(["https://grafana.example.com", "https://nas.example.com"]);
  });

  it.each(unsafe)("draws an app whose href is %j as text, not a link", async (href) => {
    await show([{ ...app("evil", ""), href }], []);
    expect(screen.getByText("evil")).toBeInTheDocument();
    expect(screen.queryByRole("link")).toBeNull();
    expect(document.querySelector("[href]")).toBeNull();
  });

  it.each(unsafe)(
    "draws an infrastructure link whose href is %j as text, not a link",
    async (href) => {
      await show([app("grafana", "")], [{ name: "Evil", href, icon: "", description: "" }]);
      await screen.findByText("Evil");
      expect(screen.getAllByRole("link").map((a) => a.getAttribute("href"))).toEqual([
        "https://grafana.example.com",
      ]);
      expect(document.querySelectorAll("[href]")).toHaveLength(1);
    },
  );
});
