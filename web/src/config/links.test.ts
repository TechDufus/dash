import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, renderHook, screen, waitFor } from "@testing-library/react";
import { createElement, type ReactNode } from "react";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { startStream, stopStream } from "@/api/stream";
import type { LauncherLink } from "@/api/types";
import { InfraSection, useLauncherStatus } from "@/components/launcher";
import {
  envelope,
  type FakeEventSource,
  installEventSource,
  pluginInfo,
  pluginsEnvelope,
  renderApp,
  stubFetch,
} from "@/test/harness";
import { iconSrc, useInfraLinks } from "./links";

const CDN = "https://cdn.example.com/icons/png";

describe("iconSrc", () => {
  const cases: [string, string, string, string][] = [
    // [what, token, base, want]
    ["a name with a base becomes its PNG", "proxmox", CDN, `${CDN}/proxmox.png`],
    ["a base with a trailing slash", "unifi-drive", `${CDN}/`, `${CDN}/unifi-drive.png`],
    ["a base without a path", "nas", "https://cdn.example.com", "https://cdn.example.com/nas.png"],
    ["a name with no base shows no icon", "proxmox", "", ""],
    ["a same-origin path is used as is, with no base", "/icons/nas.png", "", "/icons/nas.png"],
    ["a same-origin path is used as is, with a base", "/icons/nas.png", CDN, "/icons/nas.png"],
    ["an absolute URL under the base", `${CDN}/nas.png`, CDN, `${CDN}/nas.png`],
    ["an absolute URL with no base is refused", `${CDN}/nas.png`, "", ""],
    ["an absolute URL on another host is refused", "https://evil.example.net/a.png", CDN, ""],
    [
      "an absolute URL on the base's host but outside its path",
      "https://cdn.example.com/x.png",
      CDN,
      "",
    ],
    ["a dot-dot escape from the base's path", `${CDN}/../../x.png`, CDN, ""],
    [
      "a host that merely starts like the base's",
      "https://cdn.example.com.evil.net/icons/png/a.png",
      CDN,
      "",
    ],
    ["another scheme on the same host", "http://cdn.example.com/icons/png/a.png", CDN, ""],
    ["a protocol-relative URL", "//cdn.example.com/icons/png/a.png", CDN, ""],
    ["a backslash path", "/\\evil.example.net/a.png", CDN, ""],
    ["a tab hides a second slash", "/\t/evil.example.net/a.png", CDN, ""],
    ["a newline hides a second slash", "/\n/evil.example.net/a.png", CDN, ""],
    ["a carriage return hides a second slash", "/\r/evil.example.net/a.png", CDN, ""],
    ["a tab inside a path", "/icons/\tnas.png", "", ""],
    ["a relative path", "icons/a.png", CDN, ""],
    ["a javascript: URL", "javascript:alert(1)", CDN, ""],
    ["a data: URL", "data:image/png;base64,AAAA", CDN, ""],
    ["an empty token", "", CDN, ""],
    ["a blank token", "   ", CDN, ""],
    ["a name that is a path in disguise", "a b", CDN, ""],
    ["a base that is not a URL", "proxmox", "not a url", ""],
    ["a base with another scheme", "proxmox", "ftp://cdn.example.com", ""],
    // A name carries the extension an annotation gives it; the PNG is requested either way.
    ["a name with .png", "grafana.png", CDN, `${CDN}/grafana.png`],
    ["a name with .svg", "grafana.svg", CDN, `${CDN}/grafana.png`],
    ["a name with .webp", "grafana.webp", CDN, `${CDN}/grafana.png`],
    ["the same name with no extension", "grafana", CDN, `${CDN}/grafana.png`],
    ["a dotted name that is not an extension", "a.b", CDN, `${CDN}/a.b.png`],
    ["a dotted name with an extension", "a.b.png", CDN, `${CDN}/a.b.png`],
    ["a hyphenated name with an extension", "home-assistant.png", CDN, `${CDN}/home-assistant.png`],
    ["only one trailing extension is dropped", "x.png.png", CDN, `${CDN}/x.png.png`],
    ["an extension in the middle stays", "x.svg.old", CDN, `${CDN}/x.svg.old.png`],
    ["an uppercase extension is not one", "x.PNG", CDN, `${CDN}/x.PNG.png`],
    ["a name with an extension and no base shows no icon", "grafana.png", "", ""],
    ["an absolute URL keeps its own extension", `${CDN}/nas.svg`, CDN, `${CDN}/nas.svg`],
    // Homepage-style tokens name icon sets that are not dashboard-icons.
    ["a homepage mdi- token", "mdi-monitor-dashboard", CDN, ""],
    ["a homepage si- token", "si-github", CDN, ""],
    ["a homepage sh- token", "sh-plex", CDN, ""],
    ["a homepage token with an extension", "mdi-home.png", CDN, ""],
    ["a homepage token with no base", "mdi-monitor-dashboard", "", ""],
    ["a name that only starts like a homepage prefix", "mdix-tool", CDN, `${CDN}/mdix-tool.png`],
    ["a name that merely begins with si", "simple-icons", CDN, `${CDN}/simple-icons.png`],
    ["a same-origin path named like a homepage token", "/icons/mdi-x.png", CDN, "/icons/mdi-x.png"],
  ];
  for (const [what, token, base, want] of cases) {
    it(what, () => expect(iconSrc(token, base)).toBe(want));
  }
});

const links: LauncherLink[] = [
  { name: "Proxmox", href: "https://pve.example.com", icon: "proxmox", description: "Hypervisor" },
  { name: "NAS", href: "https://nas.example.com", icon: "/icons/nas.png", description: "Backups" },
  {
    name: "Wiki",
    href: "https://wiki.example.com",
    icon: "https://evil.example.net/w.png",
    description: "",
  },
];

describe("the launcher.links topic in the SPA", () => {
  let ES: typeof FakeEventSource;
  let es: FakeEventSource;

  const wrapper = ({ children }: { children: ReactNode }) =>
    createElement(
      QueryClientProvider,
      { client: new QueryClient({ defaultOptions: { queries: { retry: false } } }) },
      children,
    );

  beforeEach(() => {
    ES = installEventSource();
  });
  afterEach(() => stopStream());

  function connect(iconBaseUrl: string) {
    stubFetch({
      "GET /api/v1/me": { user: "dev", role: "admin", scopes: [], ui: { iconBaseUrl } },
    });
    startStream();
    const instance = ES.instances.at(-1);
    if (!instance) throw new Error("no EventSource");
    instance.open();
    es = instance;
  }

  const sendRow = (state = "healthy") =>
    act(() =>
      es.emit(
        "core.plugins",
        pluginsEnvelope([
          pluginInfo({ id: "launcher", name: "Launcher", state, topics: ["launcher.links"] }),
        ]),
      ),
    );
  const sendLinks = (data: LauncherLink[]) =>
    act(() => es.emit("launcher.links", envelope("launcher.links", { links: data })));

  it("is pending until the envelope arrives, then lists the links with their icons resolved", async () => {
    connect(CDN);
    const { result } = renderHook(() => useInfraLinks(), { wrapper });
    expect(result.current).toEqual({ links: [], pending: true });
    sendRow();
    expect(result.current.pending).toBe(true);
    sendLinks(links);
    await waitFor(() => expect(result.current.links[0]?.icon).toBe(`${CDN}/proxmox.png`));
    expect(result.current.pending).toBe(false);
    expect(result.current.links.map((l) => l.icon)).toEqual([
      `${CDN}/proxmox.png`,
      "/icons/nas.png",
      "", // another origin: a letter tile
    ]);
    expect(result.current.links.map((l) => l.name)).toEqual(["Proxmox", "NAS", "Wiki"]);
  });

  it("makes no third-party icon request without ui.iconBaseURL: names and URLs are letter tiles", async () => {
    connect("");
    const { result } = renderHook(() => useInfraLinks(), { wrapper });
    sendRow();
    sendLinks(links);
    await waitFor(() => expect(result.current.pending).toBe(false));
    expect(result.current.links.map((l) => l.icon)).toEqual(["", "/icons/nas.png", ""]);
  });

  it("an empty list is not pending", async () => {
    connect("");
    const { result } = renderHook(() => useInfraLinks(), { wrapper });
    sendRow();
    sendLinks([]);
    await waitFor(() => expect(result.current.pending).toBe(false));
    expect(result.current.links).toEqual([]);
  });

  it("InfraSection draws a tile per link with the resolved icon, from the topic alone", async () => {
    connect(CDN);
    const { container } = renderApp(createElement(InfraSection, { query: "" }));
    expect(container.querySelector("a")).toBeNull();
    sendRow();
    sendLinks(links);
    expect(await screen.findByText("Proxmox")).toBeTruthy();
    expect(screen.getByText("NAS").closest("a")?.getAttribute("href")).toBe(
      "https://nas.example.com",
    );
    await waitFor(() =>
      expect(
        Array.from(container.querySelectorAll("img")).map((i) => i.getAttribute("src")),
      ).toEqual([`${CDN}/proxmox.png`, "/icons/nas.png"]),
    );
    expect(container.querySelectorAll("a")).toHaveLength(3);
  });

  it("InfraSection draws nothing for an empty list", async () => {
    connect("");
    const { container } = renderApp(createElement(InfraSection, { query: "" }));
    sendRow();
    sendLinks([]);
    await waitFor(() => expect(container.querySelector("a")).toBeNull());
    expect(screen.queryByText("Infrastructure")).toBeNull();
  });

  it("the launcher is loading until its links arrive, shown with links, off when the list is empty", async () => {
    connect("");
    const { result } = renderHook(() => useLauncherStatus(), { wrapper });
    expect(result.current).toBe("loading"); // no plugin list yet
    sendRow();
    await waitFor(() => expect(result.current).toBe("loading")); // the plugin is visible, its links are not here
    sendLinks(links);
    await waitFor(() => expect(result.current).toBe("shown"));
    sendLinks([]);
    await waitFor(() => expect(result.current).toBe("off"));
  });

  it("a launcher that is not visible shows nothing, and the topic is dropped with its row", async () => {
    connect("");
    const { result } = renderHook(() => useLauncherStatus(), { wrapper });
    sendRow();
    sendLinks(links);
    await waitFor(() => expect(result.current).toBe("shown"));
    sendRow("unconfigured");
    await waitFor(() => expect(result.current).toBe("off"));
    const { result: infra } = renderHook(() => useInfraLinks(), { wrapper });
    expect(infra.current.pending).toBe(true); // the envelope left with the row
  });

  it("a launcher that failed to start is not pending forever: it is off", async () => {
    connect("");
    const { result } = renderHook(() => ({ status: useLauncherStatus(), infra: useInfraLinks() }), {
      wrapper,
    });
    sendRow("starting");
    await waitFor(() => expect(result.current.status).toBe("loading"));
    expect(result.current.infra.pending).toBe(true);
    sendRow("error"); // a failed start publishes nothing, and the supervisor drops the topics
    await waitFor(() => expect(result.current.status).toBe("off"));
    expect(result.current.infra).toEqual({ links: [], pending: false });
  });
});
