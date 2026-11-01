import {
  Component,
  type ComponentType,
  createElement,
  type ErrorInfo,
  type ReactNode,
} from "react";
import type { Topic } from "@/api/types";
import { Reveal } from "@/components/page";
import { cn } from "@/lib/utils";
import { PANELS } from "./panels";
import { MODULES } from "./registry";
import { isVisible, usePluginsState } from "./state";
import type { PanelContribution, SlotId, SlotProps } from "./types";

interface BoundaryProps {
  /** `module/panel`, for the log line. */
  name: string;
  /** The boundary tries again when any of these changes (a bad payload is replaced by the next one). */
  resetKeys: readonly unknown[];
  children: ReactNode;
}

/** Keeps a panel that throws from taking the page down: a muted row replaces it. */
export class PanelBoundary extends Component<BoundaryProps, { failed: boolean }> {
  state = { failed: false };

  static getDerivedStateFromError() {
    return { failed: true };
  }

  componentDidCatch(error: unknown, info: ErrorInfo) {
    console.error(`panel ${this.props.name} failed`, error, info.componentStack);
  }

  componentDidUpdate(prev: BoundaryProps) {
    const { resetKeys } = this.props;
    const changed =
      resetKeys.length !== prev.resetKeys.length ||
      resetKeys.some((k, i) => !Object.is(k, prev.resetKeys[i]));
    if (this.state.failed && changed) this.setState({ failed: false });
  }

  render() {
    if (!this.state.failed) return this.props.children;
    return (
      <div
        role="status"
        className="rounded-2xl border border-line px-4 py-3 text-xs text-muted"
        data-testid="panel-failed"
      >
        This panel failed to render.
      </div>
    );
  }
}

/** Whether a plugin's module contributes at least one panel to `slot`. */
export function contributesPanel(pluginId: string, slot: SlotId): boolean {
  return (PANELS[pluginId]?.[slot]?.length ?? 0) > 0;
}

interface Placed {
  moduleId: string;
  panel: PanelContribution;
}

/** Cell classes for the storage grid; the other slots lay their contributions out themselves. */
const SPAN = { full: "lg:col-span-2", half: undefined } as const;

/**
 * The panels the visible modules contribute to a slot, ordered by `order` (the module's rail order
 * when a panel has none), then registry position, then declaration. A panel that reads a topic no
 * visible plugin publishes is left out, so it never waits for data that cannot arrive.
 */
export function Slot<S extends SlotId>({ id, ...props }: { id: S } & SlotProps[S]) {
  const { ready, byId, plugins } = usePluginsState();
  if (!ready) return null;

  const available = new Set<string>();
  for (const p of plugins) if (isVisible(p.state)) for (const t of p.topics) available.add(t);
  const topicAvailable = (t: Topic) => t.startsWith("core.") || available.has(t);

  const placed: (Placed & { key: [number, number, number] })[] = [];
  MODULES.forEach((m, mi) => {
    const info = byId.get(m.id);
    if (!info || !isVisible(info.state)) return;
    const list = (PANELS[m.id]?.[id] ?? []) as readonly PanelContribution[];
    list.forEach((panel, pi) => {
      if (panel.reads?.some((t) => !topicAvailable(t))) return;
      placed.push({
        moduleId: m.id,
        panel,
        key: [panel.order ?? m.order ?? 1000 + mi, mi, pi],
      });
    });
  });
  placed.sort((a, b) => a.key[0] - b.key[0] || a.key[1] - b.key[1] || a.key[2] - b.key[2]);

  const resetKeys = Object.values(props);
  return (
    <>
      {placed.map(({ moduleId, panel }, i) => {
        const key = `${moduleId}/${panel.id}`;
        const boundary = (
          <PanelBoundary key={key} name={key} resetKeys={resetKeys}>
            {createElement(panel.component as ComponentType<object>, props)}
          </PanelBoundary>
        );
        // Only the storage grid needs a cell; the KPI grid and the apps stack take contributions as is.
        return id === "storage" ? (
          <Reveal key={key} index={i} className={cn(SPAN[panel.span ?? "half"])}>
            {boundary}
          </Reveal>
        ) : (
          boundary
        );
      })}
    </>
  );
}
