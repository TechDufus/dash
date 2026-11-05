import { BellRing, CircleCheck, OctagonAlert, TriangleAlert } from "lucide-react";
import { AnimatePresence, motion, useReducedMotion } from "motion/react";
import { Link } from "wouter";
import type { AttentionItem, SourceStatus } from "@/api/types";
import { CopyContextButton } from "@/components/copy-context";
import { LinkButtons } from "@/components/links";
import { BorderBeam } from "@/components/magicui/border-beam";
import { LayoutItem } from "@/components/page";
import { Panel, PanelHeader } from "@/components/panel";
import { formatRelative } from "@/lib/format";
import { cn } from "@/lib/utils";
import { useRoutable } from "@/plugins/routable";

const CRIT_HEX = "#ff4d6d";
const WARN_HEX = "#ffb547";

function AttentionRow({ item }: { item: AttentionItem }) {
  const crit = item.severity === "crit";
  const Icon = crit ? OctagonAlert : TriangleAlert;
  const routable = useRoutable();
  const body = (
    <>
      <Icon
        className={cn("mt-0.5 size-4 shrink-0", crit ? "text-crit" : "text-warn")}
        aria-label={crit ? "Critical" : "Warning"}
        role="img"
      />
      <span className="min-w-0 flex-1">
        <span className="flex items-baseline gap-2">
          <span className="truncate text-sm font-medium text-fg">{item.title}</span>
          <span className="num ml-auto shrink-0 text-[11px] text-faint">
            {formatRelative(item.since)}
          </span>
        </span>
        <span className="mt-0.5 line-clamp-2 block text-xs text-muted">{item.detail}</span>
        <span className="mt-1 inline-block rounded-full border border-line px-1.5 py-px text-[10px] text-faint">
          {item.source}
        </span>
      </span>
    </>
  );
  return (
    <div className="rounded-xl border border-transparent px-2 py-2 transition-colors hover:border-line hover:bg-panel-hover">
      {/* The route is a link only while the plugin that raised the item has a page for it. */}
      {routable(item.plugin, item.route) ? (
        <Link href={item.route} className="flex gap-3 rounded-lg">
          {body}
        </Link>
      ) : (
        <div className="flex gap-3 rounded-lg">{body}</div>
      )}
      <div className="mt-2 flex flex-wrap items-center gap-2 pl-7">
        <LinkButtons links={item.links} size="xs" />
        <CopyContextButton contextRef={item.ref} name={item.title} size="xs" />
      </div>
    </div>
  );
}

interface AttentionPanelProps {
  items: AttentionItem[];
  sources?: SourceStatus[];
  className?: string;
}

/** Things that need the owner; a crit or warn BorderBeam circles the panel while any exist. */
export function AttentionPanel({ items, sources, className }: AttentionPanelProps) {
  const reduced = useReducedMotion();
  const hasCrit = items.some((i) => i.severity === "crit");
  const hasWarn = items.some((i) => i.severity === "warn");
  const color = hasCrit ? CRIT_HEX : hasWarn ? WARN_HEX : undefined;
  return (
    <Panel
      className={cn(
        "relative flex flex-col overflow-hidden",
        reduced && hasCrit && "border-crit/50",
        reduced && !hasCrit && hasWarn && "border-warn/50",
        className,
      )}
    >
      <PanelHeader title="Attention" icon={<BellRing aria-hidden="true" />} sources={sources}>
        {items.length > 0 && (
          <span className="num rounded-full border border-line px-2 py-0.5 text-xs text-muted">
            {items.length}
          </span>
        )}
      </PanelHeader>
      {items.length === 0 ? (
        <div className="flex flex-1 flex-col items-center justify-center gap-2 px-4 py-10 text-center">
          <CircleCheck className="size-8 text-ok/80" aria-hidden="true" />
          <p className="text-sm font-medium text-fg">Nothing needs you</p>
          <p className="text-xs text-muted">All systems are quiet.</p>
        </div>
      ) : (
        <div className="max-h-[420px] min-h-0 flex-1 space-y-1 overflow-y-auto px-2 pb-3">
          <AnimatePresence initial={false}>
            {items.map((item) => (
              <LayoutItem key={item.id}>
                <motion.div
                  initial={{ opacity: 0, x: -8 }}
                  animate={{ opacity: 1, x: 0 }}
                  exit={{ opacity: 0, x: 8 }}
                  transition={{ duration: 0.2 }}
                >
                  <AttentionRow item={item} />
                </motion.div>
              </LayoutItem>
            ))}
          </AnimatePresence>
        </div>
      )}
      {color && !reduced && (
        <BorderBeam size={120} duration={8} borderWidth={1.5} colorFrom={color} colorTo={color} />
      )}
    </Panel>
  );
}
