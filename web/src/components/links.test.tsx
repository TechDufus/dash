import { screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { Link } from "@/api/types";
import { renderApp } from "@/test/harness";
import { LinkButtons } from "./links";

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
});
