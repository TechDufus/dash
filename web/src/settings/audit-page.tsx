import { useInfiniteQuery } from "@tanstack/react-query";
import { ScrollText } from "lucide-react";
import { useMemo, useState } from "react";
import { api, errorMessage, withParams } from "@/api/client";
import { useMe } from "@/api/queries";
import type { AuditKind, AuditRing, SettingsAudit } from "@/api/types";
import { Page } from "@/components/page";
import { Empty, Panel, PanelHeader, PanelSkeleton } from "@/components/panel";
import { Button } from "@/components/ui/button";
import { settingsKeys, settingsPaths, useSettingsAvailable } from "./api";
import { AuditTable } from "./audit-table";

const RINGS: { value: AuditRing; label: string }[] = [
  { value: "main", label: "Activity" },
  { value: "unauth", label: "Refusals" },
];

const KINDS: { value: AuditKind | "all"; label: string }[] = [
  { value: "all", label: "All" },
  { value: "intent", label: "Intent" },
  { value: "outcome", label: "Outcome" },
  { value: "event", label: "Event" },
  { value: "denial", label: "Denial" },
];

/**
 * One ring of GET /api/v1/settings/audit, page by page: each "Load older" sends the `next` of the
 * ring's last page as `before`. A cursor is valid for its own ring only, so the ring is part of
 * the query key and switching rings starts over from that ring's newest page.
 */
function useAuditRing(ring: AuditRing, enabled: boolean) {
  return useInfiniteQuery({
    queryKey: settingsKeys.audit(ring),
    queryFn: ({ pageParam }) =>
      api.get<SettingsAudit>(withParams(settingsPaths.audit, { ring, before: pageParam })),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (last) => last.next || undefined,
    enabled,
  });
}

/** A row of toggle buttons; the pressed one is the current value. */
function Choice<T extends string>({
  label,
  options,
  value,
  onChange,
}: {
  label: string;
  options: { value: T; label: string }[];
  value: T;
  onChange: (v: T) => void;
}) {
  return (
    // biome-ignore lint/a11y/useSemanticElements: a group of toggle buttons, not a form fieldset
    <div role="group" aria-label={label} className="flex flex-wrap gap-1">
      {options.map((o) => (
        <Button
          key={o.value}
          size="sm"
          variant={o.value === value ? "secondary" : "ghost"}
          aria-pressed={o.value === value}
          onClick={() => onChange(o.value)}
        >
          {o.label}
        </Button>
      ))}
    </div>
  );
}

/**
 * /settings/audit: settings changes and actions (main ring) or refused requests (unauth ring),
 * newest first. The kind filter applies to the records loaded so far; it sends no request.
 */
export function AuditPage() {
  const me = useMe();
  const admin = useSettingsAvailable();
  const [ring, setRing] = useState<AuditRing>("main");
  const [kind, setKind] = useState<AuditKind | "all">("all");
  const audit = useAuditRing(ring, admin);

  const pages = audit.data?.pages;
  const loaded = useMemo(() => pages?.flatMap((p) => p.records ?? []) ?? [], [pages]);
  const shown = useMemo(
    () => (kind === "all" ? loaded : loaded.filter((e) => e.kind === kind)),
    [loaded, kind],
  );

  if (me.isPending) {
    return (
      <Page>
        <PanelSkeleton lines={5} />
      </Page>
    );
  }
  if (!admin) {
    return (
      <Page>
        <Panel>
          <Empty>Not authorised: the audit log is for administrators.</Empty>
        </Panel>
      </Page>
    );
  }

  const first = pages?.[0];
  return (
    <Page>
      <Panel>
        <PanelHeader title="Audit log" icon={<ScrollText />}>
          <Choice label="Ring" options={RINGS} value={ring} onChange={setRing} />
        </PanelHeader>
        <div className="flex flex-wrap items-center gap-2 px-4 pb-3">
          <Choice label="Kind" options={KINDS} value={kind} onChange={setKind} />
          {first && (
            <span className="ml-auto text-xs text-muted">
              Source:{" "}
              {first.source === "file"
                ? "file audit.jsonl"
                : "since this start (no data directory)"}
            </span>
          )}
        </div>
        {audit.isPending ? (
          <PanelSkeleton lines={5} className="border-0 shadow-none" />
        ) : audit.isError && !first ? (
          <Empty>Could not read the audit log: {errorMessage(audit.error)}</Empty>
        ) : loaded.length === 0 ? (
          <Empty>No audit records yet</Empty>
        ) : shown.length === 0 ? (
          <Empty>No loaded records of this kind</Empty>
        ) : (
          <AuditTable records={shown} />
        )}
        {loaded.length > 0 && (
          <div className="flex items-center justify-center gap-3 px-4 py-3 text-xs text-muted">
            {audit.hasNextPage ? (
              <Button
                size="sm"
                variant="outline"
                disabled={audit.isFetchingNextPage}
                onClick={() => audit.fetchNextPage()}
              >
                {audit.isFetchingNextPage ? "Loading…" : "Load older"}
              </Button>
            ) : (
              <span>No older records</span>
            )}
            {audit.isFetchNextPageError && (
              <span className="text-destructive">{errorMessage(audit.error)}</span>
            )}
          </div>
        )}
      </Panel>
    </Page>
  );
}
