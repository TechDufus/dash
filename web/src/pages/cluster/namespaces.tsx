import { Lock, ShieldAlert, Users } from "lucide-react";
import { useMemo } from "react";
import { Link } from "wouter";
import type { ClusterData, Group } from "@/api/types";
import { LayoutItem } from "@/components/page";
import { Empty, Panel, PanelSkeleton } from "@/components/panel";
import { StatusDot } from "@/components/status";
import { Badge } from "@/components/ui/badge";
import { compareHealth } from "./common";

const GROUP_ORDER: Record<Group, number> = { Platform: 0, Apps: 1, Tenants: 2 };

export function NamespacesTab({ data }: { data: ClusterData | undefined }) {
  const rows = useMemo(
    () =>
      [...(data?.namespaces ?? [])].sort(
        (a, b) =>
          compareHealth(a.health, b.health) ||
          (GROUP_ORDER[a.group] ?? 9) - (GROUP_ORDER[b.group] ?? 9) ||
          a.name.localeCompare(b.name),
      ),
    [data],
  );

  if (!data) {
    return (
      <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-3 2xl:grid-cols-4">
        {Array.from({ length: 6 }, (_, i) => (
          // biome-ignore lint/suspicious/noArrayIndexKey: static placeholder cards
          <PanelSkeleton key={i} lines={2} />
        ))}
      </div>
    );
  }
  if (rows.length === 0) {
    return (
      <Panel>
        <Empty>No namespaces reported</Empty>
      </Panel>
    );
  }

  return (
    <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-3 2xl:grid-cols-4">
      {rows.map((ns) => (
        <LayoutItem key={ns.name}>
          <Panel interactive className="relative h-full space-y-3 p-4">
            <div className="flex items-center gap-2">
              <StatusDot health={ns.health} />
              <h3 className="min-w-0 flex-1 truncate text-sm font-medium text-fg">
                <Link
                  href={`/cluster?tab=workloads&ns=${encodeURIComponent(ns.name)}`}
                  className="after:absolute after:inset-0 after:rounded-2xl after:content-['']"
                  aria-label={`${ns.name} workloads`}
                >
                  {ns.name}
                </Link>
              </h3>
              <Badge variant="outline" className="text-muted">
                {ns.group}
              </Badge>
            </div>

            <div className="flex min-h-5 flex-wrap gap-1.5">
              {ns.protected && (
                <Badge variant="outline" className="gap-1 text-warn">
                  <Lock aria-hidden="true" />
                  protected
                </Badge>
              )}
              {ns.tenant && (
                <Badge variant="outline" className="gap-1 text-accent">
                  <Users aria-hidden="true" />
                  {ns.tenant}
                </Badge>
              )}
              {ns.privileged && (
                <Badge variant="outline" className="gap-1 text-crit">
                  <ShieldAlert aria-hidden="true" />
                  privileged
                </Badge>
              )}
            </div>

            <dl className="grid grid-cols-3 gap-2 text-xs">
              <div>
                <dt className="text-muted">Pods</dt>
                <dd className="num text-fg">
                  {ns.podsRunning}/{ns.podsTotal}
                </dd>
              </div>
              <div>
                <dt className="text-muted">Restarts</dt>
                <dd className="num text-fg">{ns.restarts}</dd>
              </div>
              <div>
                <dt className="text-muted">Not ready</dt>
                <dd className={`num ${ns.workloadsNotReady > 0 ? "text-warn" : "text-fg"}`}>
                  {ns.workloadsNotReady}
                </dd>
              </div>
            </dl>
          </Panel>
        </LayoutItem>
      ))}
    </div>
  );
}
