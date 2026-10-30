import { FileKey, KeyRound } from "lucide-react";
import { useMemo, useState } from "react";
import type { Cert, ClusterData, ExtSecret, Health, SourceStatus } from "@/api/types";
import { HeadlampIcon } from "@/components/links";
import { Empty, Panel, PanelHeader, PanelSkeleton } from "@/components/panel";
import { HealthBadge } from "@/components/status";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { formatDateTime, formatRelative } from "@/lib/format";
import { useNow } from "@/lib/hooks";
import { useQueryParam } from "@/lib/query-param";
import { compareHealth, matchesQuery, ROW_CAP } from "./common";
import { SearchBox, ShowAll } from "./filters";

const TH = "h-9 px-3 text-xs font-medium text-muted";
const TD = "px-3 py-2";
const DAY_MS = 86_400_000;

/** Days until expiry (negative when expired), or undefined without a notAfter. */
function daysRemaining(cert: Cert, now: number): number | undefined {
  if (!cert.notAfter) return undefined;
  const t = Date.parse(cert.notAfter);
  return Number.isFinite(t) ? Math.floor((t - now) / DAY_MS) : undefined;
}

function certHealth(cert: Cert, days: number | undefined): Health {
  if (!cert.ready) return "crit";
  if (days === undefined) return "unknown";
  if (days < 7) return "crit";
  if (days < 21) return "warn";
  return "ok";
}

function daysLabel(days: number): string {
  if (days < 0) return `expired ${-days}d ago`;
  return `${days}d left`;
}

export function EventsTab({
  data,
  sources,
}: {
  data: ClusterData | undefined;
  sources: SourceStatus[] | undefined;
}) {
  const [q, setQ] = useQueryParam("q");
  const [all, setAll] = useState(false);
  const needle = q.trim().toLowerCase();

  const rows = useMemo(() => {
    if (!data) return [];
    return data.events
      .filter((e) => matchesQuery(needle, e.namespace, e.object, e.reason, e.message))
      .sort((a, b) => Date.parse(b.lastSeen) - Date.parse(a.lastSeen));
  }, [data, needle]);

  if (!data) {
    return (
      <div className="space-y-4">
        <PanelSkeleton lines={5} />
        <div className="grid gap-4 lg:grid-cols-2">
          <PanelSkeleton lines={3} />
          <PanelSkeleton lines={3} />
        </div>
      </div>
    );
  }
  const shown = all ? rows : rows.slice(0, ROW_CAP);

  return (
    <div className="space-y-4">
      <Panel>
        <PanelHeader title="Warning events" sources={sources} />
        <div className="flex flex-wrap items-center gap-2 px-4 pb-3">
          <SearchBox
            value={q}
            onChange={setQ}
            placeholder="Filter namespace, object, reason, message"
            label="Filter events"
          />
          <span className="num ml-auto text-xs text-muted">
            {rows.length} of {data.events.length}
          </span>
        </div>
        {data.events.length === 0 ? (
          <Empty>No warning events</Empty>
        ) : rows.length === 0 ? (
          <Empty>No events match the filter</Empty>
        ) : (
          <Table>
            <TableHeader>
              <TableRow className="border-line hover:bg-transparent">
                <TableHead className={TH}>Namespace</TableHead>
                <TableHead className={TH}>Object</TableHead>
                <TableHead className={TH}>Reason</TableHead>
                <TableHead className={TH}>Message</TableHead>
                <TableHead className={`${TH} text-right`}>Count</TableHead>
                <TableHead className={`${TH} text-right`}>Last seen</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {shown.map((e) => (
                <TableRow
                  key={`${e.namespace}/${e.object}/${e.reason}/${e.lastSeen}`}
                  className="border-line"
                >
                  <TableCell className={`${TD} text-muted`}>{e.namespace}</TableCell>
                  <TableCell className={`${TD} font-medium text-fg`}>{e.object}</TableCell>
                  <TableCell className={`${TD} text-warn`}>{e.reason}</TableCell>
                  <TableCell className={`${TD} min-w-72 max-w-xl whitespace-normal break-words`}>
                    {e.message}
                  </TableCell>
                  <TableCell className={`${TD} num text-right`}>{e.count}</TableCell>
                  <TableCell
                    className={`${TD} num text-right text-muted`}
                    title={formatDateTime(e.lastSeen)}
                  >
                    {formatRelative(e.lastSeen)}
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        )}
        <ShowAll shown={shown.length} total={rows.length} onAll={() => setAll(true)} />
      </Panel>

      <div className="grid gap-4 lg:grid-cols-2">
        <CertificatesPanel certs={data.certificates} sources={sources} />
        <ExternalSecretsPanel secrets={data.externalSecrets} sources={sources} />
      </div>
    </div>
  );
}

function CertificatesPanel({
  certs,
  sources,
}: {
  certs: Cert[];
  sources: SourceStatus[] | undefined;
}) {
  const now = useNow(60_000).getTime();
  const rows = useMemo(() => {
    return certs
      .map((cert) => {
        const days = daysRemaining(cert, now);
        return { cert, days, health: certHealth(cert, days) };
      })
      .sort(
        (a, b) =>
          compareHealth(a.health, b.health) ||
          (a.days ?? Number.POSITIVE_INFINITY) - (b.days ?? Number.POSITIVE_INFINITY) ||
          a.cert.name.localeCompare(b.cert.name),
      );
  }, [certs, now]);

  return (
    <Panel>
      <PanelHeader title="Certificates" icon={<FileKey />} sources={sources}>
        <span className="num text-xs text-muted">{certs.length}</span>
      </PanelHeader>
      {rows.length === 0 ? (
        <Empty>No certificates</Empty>
      ) : (
        <Table>
          <TableHeader>
            <TableRow className="border-line hover:bg-transparent">
              <TableHead className={TH}>Certificate</TableHead>
              <TableHead className={TH}>Ready</TableHead>
              <TableHead className={TH}>Expires</TableHead>
              <TableHead className={`${TH} text-right`}>
                <span className="sr-only">Links</span>
              </TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {rows.map(({ cert, days, health }) => (
              <TableRow key={`${cert.namespace}/${cert.name}`} className="border-line">
                <TableCell className={TD}>
                  <span className="text-faint">{cert.namespace}/</span>
                  <span className="font-medium text-fg">{cert.name}</span>
                </TableCell>
                <TableCell className={TD}>
                  <HealthBadge
                    health={cert.ready ? "ok" : "crit"}
                    label={cert.ready ? "Ready" : "Not ready"}
                  />
                </TableCell>
                <TableCell className={TD}>
                  {days === undefined ? (
                    <span className="text-muted">–</span>
                  ) : (
                    <span className="flex flex-wrap items-center gap-x-2">
                      <span className="num text-xs text-muted">
                        {formatDateTime(cert.notAfter)}
                      </span>
                      <HealthBadge health={health} label={daysLabel(days)} />
                    </span>
                  )}
                </TableCell>
                <TableCell className={`${TD} text-right`}>
                  <HeadlampIcon links={cert.links} />
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      )}
    </Panel>
  );
}

function ExternalSecretsPanel({
  secrets,
  sources,
}: {
  secrets: ExtSecret[];
  sources: SourceStatus[] | undefined;
}) {
  const rows = useMemo(
    () =>
      [...secrets].sort(
        (a, b) =>
          Number(a.ready) - Number(b.ready) ||
          a.namespace.localeCompare(b.namespace) ||
          a.name.localeCompare(b.name),
      ),
    [secrets],
  );
  return (
    <Panel>
      <PanelHeader title="External Secrets" icon={<KeyRound />} sources={sources}>
        <span className="num text-xs text-muted">{secrets.length}</span>
      </PanelHeader>
      {rows.length === 0 ? (
        <Empty>No external secrets</Empty>
      ) : (
        <Table>
          <TableHeader>
            <TableRow className="border-line hover:bg-transparent">
              <TableHead className={TH}>Secret</TableHead>
              <TableHead className={TH}>Ready</TableHead>
              <TableHead className={TH}>Message</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {rows.map((s) => (
              <TableRow key={`${s.namespace}/${s.name}`} className="border-line">
                <TableCell className={TD}>
                  <span className="text-faint">{s.namespace}/</span>
                  <span className="font-medium text-fg">{s.name}</span>
                </TableCell>
                <TableCell className={TD}>
                  <HealthBadge
                    health={s.ready ? "ok" : "crit"}
                    label={s.ready ? "Ready" : "Not ready"}
                  />
                </TableCell>
                <TableCell className={`${TD} max-w-sm whitespace-normal break-words text-muted`}>
                  {s.message || "–"}
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      )}
    </Panel>
  );
}
