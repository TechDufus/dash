import { screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { Link } from "@/api/types";
import { renderApp } from "@/test/harness";
import { HeadlampIcon, headlampLink, LinkButtons } from "./links";

const link = (kind: string, label = kind): Link => ({
  kind,
  label,
  href: `https://${label}.example.com`,
});

/** The lucide icon class of the anchor labelled `label` (lucide names its icons `lucide-<name>`). */
function iconOf(label: string): string {
  const svg = screen.getByRole("link", { name: label }).querySelector("svg");
  const name = [...(svg?.classList ?? [])].find((c) => c.startsWith("lucide-") && c !== "lucide");
  if (!name) throw new Error(`no icon on ${label}`);
  return name;
}

describe("LinkButtons", () => {
  it("falls back to the generic icon for a kind it has never heard of, without an error", () => {
    renderApp(<LinkButtons links={[link("nomad"), link("external")]} />);
    expect(screen.getByRole("link", { name: "nomad" })).toHaveAttribute(
      "href",
      "https://nomad.example.com",
    );
    expect(iconOf("nomad")).toBe("lucide-external-link");
    expect(iconOf("nomad")).toBe(iconOf("external"));
  });

  it("uses the icon a module contributes for its own kind, and the app and external defaults", () => {
    renderApp(
      <LinkButtons
        links={[link("proxmox"), link("grafana"), link("headlamp"), link("app"), link("external")]}
      />,
    );
    expect(iconOf("proxmox")).toBe("lucide-server");
    expect(iconOf("grafana")).toBe("lucide-chart-line");
    expect(iconOf("headlamp")).toBe("lucide-boxes");
    expect(iconOf("app")).toBe("lucide-layout-grid");
    expect(iconOf("external")).toBe("lucide-external-link");
  });

  it("does not mistake an object property name for a kind", () => {
    renderApp(<LinkButtons links={[link("constructor"), link("toString"), link("__proto__")]} />);
    for (const label of ["constructor", "toString", "__proto__"]) {
      expect(iconOf(label), label).toBe("lucide-external-link");
    }
  });

  it("renders nothing without links", () => {
    const { container } = renderApp(<LinkButtons links={[]} />);
    expect(container).toBeEmptyDOMElement();
  });

  it.each([
    "javascript:alert(1)",
    "  JaVaScRiPt:alert(1)",
    "data:text/html,<script>alert(1)</script>",
    "vbscript:msgbox(1)",
  ])("draws a link whose href is %j as text, not an anchor", (href) => {
    const { container } = renderApp(
      <LinkButtons links={[{ kind: "grafana", label: "Dash", href }]} />,
    );
    expect(screen.getByText("Dash")).toBeInTheDocument();
    expect(screen.queryByRole("link")).toBeNull();
    expect(container.querySelector("a")).toBeNull();
    expect(container.querySelector("[href]")).toBeNull();
  });
});

describe("headlampLink and HeadlampIcon", () => {
  const headlamp = (href: string): Link => ({ kind: "headlamp", label: "Headlamp", href });

  it("returns the first headlamp link that is safe to open, skipping an unsafe one", () => {
    const safe = headlamp("https://headlamp.example.com/x");
    expect(headlampLink([link("grafana"), safe])).toBe(safe);
    expect(headlampLink([headlamp("javascript:alert(1)"), safe])).toBe(safe);
    expect(headlampLink([headlamp("javascript:alert(1)")])).toBeUndefined();
    expect(headlampLink([headlamp("data:text/html,x")])).toBeUndefined();
    expect(headlampLink(undefined)).toBeUndefined();
  });

  it("renders the icon anchor for a safe link", () => {
    renderApp(<HeadlampIcon links={[headlamp("https://headlamp.example.com/x")]} />);
    expect(screen.getByRole("link", { name: "Open in Headlamp" })).toHaveAttribute(
      "href",
      "https://headlamp.example.com/x",
    );
  });

  it.each(["javascript:alert(1)", "  JaVaScRiPt:alert(1)", "vbscript:msgbox(1)"])(
    "renders nothing for a headlamp link whose href is %j",
    (href) => {
      const { container } = renderApp(<HeadlampIcon links={[headlamp(href)]} />);
      expect(container).toBeEmptyDOMElement();
    },
  );
});
