import { Boxes, ChartLine, ExternalLink, LayoutGrid, Server } from "lucide-react";
import type { Link, LinkKind } from "@/api/types";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";

const ICONS: Record<LinkKind, typeof ExternalLink> = {
  headlamp: Boxes,
  proxmox: Server,
  grafana: ChartLine,
  app: LayoutGrid,
  external: ExternalLink,
};

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
        const Icon = ICONS[l.kind] ?? ExternalLink;
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
