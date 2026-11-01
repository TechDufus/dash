import { motion } from "motion/react";
import { Link, useLocation } from "wouter";
import { useMe } from "@/api/queries";
import { Skeleton } from "@/components/ui/skeleton";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { cn } from "@/lib/utils";
import { isActive, useNavEntries } from "@/plugins/state";

/** Fixed 72 px icon rail (desktop). */
export function Rail() {
  const [location] = useLocation();
  const me = useMe();
  const { ready, entries } = useNavEntries();
  // Until the plugin list arrives the plugin positions hold placeholders: no layout jump and no
  // wrong entries. They sit after the first (Overview) entry, where the plugin entries start.
  const placeholders = ready ? [] : ["a", "b", "c"];
  return (
    <nav
      aria-label="Primary"
      className="fixed inset-y-0 left-0 z-30 hidden w-[72px] flex-col items-center border-r border-line bg-canvas/60 py-4 backdrop-blur-xl md:flex"
    >
      <Link
        href="/"
        aria-label="dash home"
        className="mb-5 flex size-10 items-center justify-center"
      >
        <span className="relative flex size-4">
          <span className="absolute inset-0 rounded-full bg-accent opacity-40 blur-md" />
          <span className="relative size-4 rounded-full bg-accent shadow-[0_0_14px_rgb(56_225_255/0.8)]" />
        </span>
      </Link>
      <ul className="flex flex-1 flex-col items-center gap-1.5">
        {entries.flatMap((item, i) => {
          const active = isActive(item, location);
          const Icon = item.icon;
          const Badge = item.badge;
          const node = (
            <li key={item.key} className="relative">
              {active && (
                <motion.span
                  layoutId="rail-active"
                  aria-hidden="true"
                  className="absolute top-2 -left-[15px] h-6 w-[3px] rounded-r-full bg-accent shadow-[0_0_12px_2px_rgb(56_225_255/0.6)]"
                />
              )}
              <Tooltip>
                <TooltipTrigger asChild>
                  <Link
                    href={item.path}
                    aria-label={item.label}
                    aria-current={active ? "page" : undefined}
                    className={cn(
                      "relative flex size-10 items-center justify-center rounded-xl text-muted transition-colors hover:bg-panel-hover hover:text-fg",
                      active && "bg-accent/10 text-accent hover:bg-accent/15 hover:text-accent",
                    )}
                  >
                    <Icon className="size-5" aria-hidden="true" />
                    {Badge && <Badge />}
                  </Link>
                </TooltipTrigger>
                <TooltipContent side="right" sideOffset={10}>
                  {item.label}
                </TooltipContent>
              </Tooltip>
            </li>
          );
          if (i !== 0) return [node];
          return [
            node,
            ...placeholders.map((p) => (
              <li key={`placeholder:${p}`} aria-hidden="true" data-placeholder="">
                <Skeleton className="size-10 rounded-xl" />
              </li>
            )),
          ];
        })}
      </ul>
      <div className="flex w-full flex-col items-center gap-1 px-1 text-center">
        <Tooltip>
          <TooltipTrigger asChild>
            <span className="flex size-8 items-center justify-center rounded-full border border-line bg-panel text-xs font-semibold text-fg uppercase">
              {me.data?.user.slice(0, 2) ?? "··"}
            </span>
          </TooltipTrigger>
          <TooltipContent side="right" sideOffset={10}>
            {me.data ? `Signed in as ${me.data.user}` : "Signing in…"}
          </TooltipContent>
        </Tooltip>
        <span className="w-full truncate text-[10px] text-muted">{me.data?.user ?? ""}</span>
        <span className="num w-full truncate text-[10px] text-faint">{me.data?.version ?? ""}</span>
      </div>
    </nav>
  );
}
