import type { ComponentProps, ReactNode } from "react";
import type { SourceStatus } from "@/api/types";
import { Skeleton } from "@/components/ui/skeleton";
import { cn } from "@/lib/utils";

interface PanelProps extends ComponentProps<"div"> {
  /** Adds the cyan hover glow (clickable panels). */
  interactive?: boolean;
}

/** Glass panel: the base surface of the whole UI. */
export function Panel({ interactive, className, ...props }: PanelProps) {
  return (
    <div
      data-slot="panel"
      className={cn(
        "rounded-2xl border border-line bg-panel shadow-[inset_0_1px_0_rgb(255_255_255/0.05),0_24px_60px_-30px_rgb(0_0_0/0.9)] backdrop-blur-xl",
        interactive &&
          "transition-[border-color,box-shadow,background-color] duration-200 hover:border-accent/30 hover:bg-panel-hover hover:shadow-[0_0_0_1px_rgb(56_225_255/0.15),0_0_40px_-12px_rgb(56_225_255/0.35)]",
        className,
      )}
      {...props}
    />
  );
}

/** Muted "source unavailable: <name>" chips for upstreams with ok=false. */
export function SourceChips({ sources }: { sources?: SourceStatus[] }) {
  const bad = sources?.filter((s) => !s.ok) ?? [];
  if (bad.length === 0) return null;
  return (
    <span className="flex flex-wrap items-center gap-1">
      {bad.map((s) => (
        <span
          key={s.name}
          title={s.error}
          className="rounded-full border border-line bg-panel px-2 py-0.5 text-[11px] text-muted"
        >
          source unavailable: {s.name}
        </span>
      ))}
    </span>
  );
}

interface PanelHeaderProps {
  title: ReactNode;
  icon?: ReactNode;
  sources?: SourceStatus[];
  children?: ReactNode;
  className?: string;
}

export function PanelHeader({ title, icon, sources, children, className }: PanelHeaderProps) {
  return (
    <div className={cn("flex flex-wrap items-center gap-x-3 gap-y-1 px-4 pt-4 pb-3", className)}>
      {icon && <span className="text-accent [&_svg]:size-4">{icon}</span>}
      <h2 className="text-sm font-medium tracking-wide text-fg">{title}</h2>
      <SourceChips sources={sources} />
      <div className="ml-auto flex items-center gap-2">{children}</div>
    </div>
  );
}

export function PanelSkeleton({ className, lines = 3 }: { className?: string; lines?: number }) {
  return (
    <Panel className={cn("space-y-3 p-4", className)} aria-busy="true">
      <Skeleton className="h-4 w-1/3" />
      {Array.from({ length: lines }, (_, i) => (
        // biome-ignore lint/suspicious/noArrayIndexKey: static placeholder rows
        <Skeleton key={i} className="h-8 w-full" />
      ))}
    </Panel>
  );
}

export function Empty({ children, className }: { children: ReactNode; className?: string }) {
  return (
    <div className={cn("px-4 py-8 text-center text-sm text-muted", className)}>{children}</div>
  );
}
