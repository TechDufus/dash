import { Navigation } from "lucide-react";
import { Fragment, useEffect } from "react";
import { useLocation } from "wouter";
import { getEnvelope, useStreamVersion } from "@/api/stream";
import type { TopicData } from "@/api/topics";
import type { Topic } from "@/api/types";
import { ui, useUi } from "@/api/ui-store";
import {
  Command,
  CommandDialog,
  CommandEmpty,
  CommandGroup,
  CommandInput,
  CommandItem,
  CommandList,
  CommandSeparator,
} from "@/components/ui/command";
import { cn } from "@/lib/utils";
import { MODULES, moduleOrder } from "@/plugins/registry";
import { isVisible, useNavEntries, usePluginsState } from "@/plugins/state";
import type { PaletteCtx, PaletteGroup, PaletteItem } from "@/plugins/types";

/**
 * The groups of every visible module, merged by id (the first heading wins) in module order.
 * "actions" always comes first, as it did when the palette was one component.
 */
function moduleGroups(ctx: PaletteCtx, visible: (id: string) => boolean): PaletteGroup[] {
  const merged = new Map<string, { heading: string; items: PaletteItem[] }>();
  for (const m of [...MODULES].sort((a, b) => moduleOrder(a) - moduleOrder(b))) {
    if (!visible(m.id)) continue;
    for (const g of m.palette?.(ctx) ?? []) {
      const into = merged.get(g.id) ?? { heading: g.heading, items: [] };
      into.items.push(...g.items);
      merged.set(g.id, into);
    }
  }
  const groups = [...merged].map(([id, g]) => ({
    id,
    heading: g.heading,
    items: [...g.items].sort((a, b) => (a.rank ?? 0) - (b.rank ?? 0)), // stable: ties keep module order
  }));
  const actions = groups.filter((g) => g.id === "actions");
  return [...actions, ...groups.filter((g) => g.id !== "actions")].filter(
    (g) => g.items.length > 0,
  );
}

/**
 * Rendered inside the dialog content, which exists only while the dialog is open or animating out:
 * a closed palette calls no module, and the items stay on screen through the close animation.
 */
function PaletteBody() {
  useStreamVersion(); // re-render with the stream: modules read topics through getEnvelope
  const [, navigate] = useLocation();
  const { entries } = useNavEntries();
  const { byId } = usePluginsState();

  const ctx: PaletteCtx = {
    topic: <T extends Topic>(name: T) => getEnvelope(name)?.data as TopicData[T] | undefined,
    go: (to) => {
      ui.setPalette(false);
      navigate(to);
    },
    close: () => ui.setPalette(false),
  };
  const groups = moduleGroups(ctx, (id) => {
    const info = byId.get(id);
    return info !== undefined && isVisible(info.state);
  });

  return (
    <Command loop>
      <CommandInput placeholder="Search pages, VMs, workloads, pods, apps…" />
      <CommandList className="max-h-[min(24rem,60vh)]">
        <CommandEmpty>Nothing matches.</CommandEmpty>
        <CommandGroup heading="Pages">
          {entries.map((n) => (
            <CommandItem
              key={n.key}
              value={`page ${n.label} ${n.title}`}
              onSelect={() => ctx.go(n.path)}
            >
              <n.icon aria-hidden="true" />
              {n.label}
            </CommandItem>
          ))}
        </CommandGroup>
        {groups.map((g) => (
          <Fragment key={g.id}>
            <CommandSeparator />
            <CommandGroup heading={g.heading}>
              {g.items.map((item) => (
                <CommandItem key={item.id} value={item.value} onSelect={item.onSelect}>
                  {item.icon && <item.icon aria-hidden="true" />}
                  {item.label}
                  {item.hint && (
                    <span className={cn("ml-auto text-xs text-faint", item.hintMono && "num")}>
                      {item.hint}
                    </span>
                  )}
                </CommandItem>
              ))}
            </CommandGroup>
          </Fragment>
        ))}
        <CommandSeparator />
        <CommandGroup heading="Navigate">
          <CommandItem value="navigate home" onSelect={() => ctx.go("/")}>
            <Navigation aria-hidden="true" />
            Back to Overview
          </CommandItem>
        </CommandGroup>
      </CommandList>
    </Command>
  );
}

/** ⌘K / Ctrl+K palette: the visible pages, what each visible plugin contributes, and Navigate. */
export function CommandPalette() {
  const { palette } = useUi();

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === "k") {
        e.preventDefault();
        ui.togglePalette();
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, []);

  return (
    <CommandDialog
      open={palette}
      onOpenChange={ui.setPalette}
      title="Command palette"
      description="Jump to pages, machines, workloads and run safe actions"
      className="sm:max-w-xl"
    >
      <PaletteBody />
    </CommandDialog>
  );
}
