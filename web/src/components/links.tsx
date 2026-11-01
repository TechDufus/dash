import { Boxes, ExternalLink, LayoutGrid, type LucideIcon } from "lucide-react";
import type { Link } from "@/api/types";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";
import { MODULES } from "@/plugins/registry";

const DEFAULT_ICONS: Readonly<Record<string, LucideIcon>> = {
  app: LayoutGrid,
  external: ExternalLink,
};

/**
 * The icon of a link kind: the one a module contributes for it (`linkIcons`), then the app and
 * external defaults, then the generic external-link icon. The kind is a provider id, so a plugin
 * the SPA has no module for still renders.
 */
function linkIcon(kind: string): LucideIcon {
  for (const m of MODULES) {
    const icons: Readonly<Record<string, LucideIcon>> | undefined = m.linkIcons;
    if (icons && Object.hasOwn(icons, kind)) return icons[kind] ?? ExternalLink;
  }
  return Object.hasOwn(DEFAULT_ICONS, kind) ? (DEFAULT_ICONS[kind] ?? ExternalLink) : ExternalLink;
}

/** Deep links as small outline buttons that open in a new tab. */
export function LinkButtons({
  links,
  className,
  size = "sm",
}: {
  links: Link[] | undefined;
  className?: string;
  size?: "xs" | "sm";
}) {
  if (!links || links.length === 0) return null;
  return (
    <div className={cn("flex flex-wrap gap-2", className)}>
      {links.map((l) => {
        const Icon = linkIcon(l.kind);
        return (
          <Button key={`${l.kind}:${l.href}`} asChild variant="outline" size={size}>
            <a href={l.href} target="_blank" rel="noreferrer noopener">
              <Icon aria-hidden="true" />
              {l.label}
            </a>
          </Button>
        );
      })}
    </div>
  );
}

/** The first Headlamp link of a list (row actions). */
export function headlampLink(links: Link[] | undefined): Link | undefined {
  return links?.find((l) => l.kind === "headlamp");
}

/** A compact icon-only link button (for table rows). */
export function HeadlampIcon({ links }: { links: Link[] | undefined }) {
  const l = headlampLink(links);
  if (!l) return null;
  return (
    <Button asChild variant="ghost" size="icon-xs" title="Open in Headlamp">
      <a href={l.href} target="_blank" rel="noreferrer noopener" aria-label="Open in Headlamp">
        <Boxes aria-hidden="true" />
      </a>
    </Button>
  );
}
