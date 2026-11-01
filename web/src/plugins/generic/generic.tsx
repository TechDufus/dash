import { useEnvelope } from "@/api/stream";
import type { GenericColumn, GenericView, PluginInfo } from "@/api/types";
import { Page, Reveal } from "@/components/page";
import { Empty, Panel, PanelHeader } from "@/components/panel";
import { Skeleton } from "@/components/ui/skeleton";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { NotFound, PageLoading, PluginDisabledPage } from "@/plugins/routes";
import { PanelBoundary } from "@/plugins/slots";
import { isVisible, usePluginsState } from "@/plugins/state";
import { Cell } from "./cell";
import { resolvePointer } from "./pointer";

type Row = Record<string, unknown>;

const isRow = (v: unknown): v is Row => typeof v === "object" && v !== null && !Array.isArray(v);

/** The rows of a table or list view, or undefined when the path is missing or not an array of objects. */
function rowsOf(data: unknown, path: string): Row[] | undefined {
  const r = resolvePointer(data, path);
  if (!r.found || !Array.isArray(r.value) || !r.value.every(isRow)) return undefined;
  return r.value;
}

const NO_DATA = <Empty className="py-6">No data</Empty>;

function TableView({ rows, columns }: { rows: Row[]; columns: GenericColumn[] }) {
  return (
    <Table>
      <TableHeader>
        <TableRow className="hover:bg-transparent">
          {columns.map((c) => (
            <TableHead key={c.key} className="text-xs text-muted">
              {c.label}
            </TableHead>
          ))}
        </TableRow>
      </TableHeader>
      <TableBody>
        {rows.map((row, i) => (
          // biome-ignore lint/suspicious/noArrayIndexKey: generic rows have no declared identity
          <TableRow key={i}>
            {columns.map((c) => (
              <TableCell key={c.key} className="num text-sm text-fg">
                <Cell value={row[c.key]} format={c.format} />
              </TableCell>
            ))}
          </TableRow>
        ))}
      </TableBody>
    </Table>
  );
}

function ListView({ rows, columns }: { rows: Row[]; columns: GenericColumn[] }) {
  const [first, ...rest] = columns;
  if (!first) return NO_DATA;
  return (
    <ul className="divide-y divide-line px-4 pb-2">
      {rows.map((row, i) => (
        // biome-ignore lint/suspicious/noArrayIndexKey: generic rows have no declared identity
        <li key={i} className="flex flex-wrap items-center gap-x-4 gap-y-1 py-3">
          <span className="min-w-0 flex-1 truncate text-sm text-fg">
            <Cell value={row[first.key]} format={first.format} />
          </span>
          {rest.map((c) => (
            <span key={c.key} title={c.label} className="num text-xs text-muted">
              <Cell value={row[c.key]} format={c.format} />
            </span>
          ))}
        </li>
      ))}
    </ul>
  );
}

function StatView({ view, data }: { view: GenericView; data: unknown }) {
  const r = resolvePointer(data, view.path);
  if (!r.found || r.value === null || typeof r.value === "object") return NO_DATA;
  const format = view.format || "number";
  return (
    <div className="px-4 pb-4">
      <div className="flex items-baseline gap-1.5">
        <span className="num text-3xl font-semibold text-fg">
          <Cell value={r.value} format={format} />
        </span>
        {view.unit && <span className="text-sm text-muted">{view.unit}</span>}
      </div>
      {view.label && <p className="mt-1 text-xs text-muted">{view.label}</p>}
    </div>
  );
}

function Body({ view, data }: { view: GenericView; data: unknown }) {
  switch (view.kind) {
    case "table":
    case "list": {
      const rows = rowsOf(data, view.path);
      if (!rows || rows.length === 0 || view.columns.length === 0) return NO_DATA;
      return view.kind === "table" ? (
        <TableView rows={rows} columns={view.columns} />
      ) : (
        <ListView rows={rows} columns={view.columns} />
      );
    }
    case "stat":
      return <StatView view={view} data={data} />;
    default:
      return NO_DATA;
  }
}

/**
 * One declared view of a plugin, drawn from the plugin's topic. A bad path or shape shows "No
 * data"; a view that still throws is replaced by a muted row until its next payload arrives.
 */
export function GenericPanel({ info, view }: { info: PluginInfo; view: GenericView }) {
  const env = useEnvelope(view.topic);
  return (
    <PanelBoundary name={`${info.id}/${view.id}`} resetKeys={[view, env]}>
      <Panel>
        <PanelHeader title={view.title} sources={env?.sources} />
        {env ? (
          <Body view={view} data={env.data} />
        ) : (
          <div className="px-4 pb-4" aria-busy="true">
            <Skeleton className="h-8 w-full" />
          </div>
        )}
      </Panel>
    </PanelBoundary>
  );
}

/** The page of a plugin that has no SPA module: its declared views, in order. */
export function GenericPluginPage({ id }: { id: string }) {
  const { ready, byId } = usePluginsState();
  if (!ready) return <PageLoading />;
  const info = byId.get(id);
  // A plugin that is off or unknown gets the explanation; a visible one with no views of its own
  // (its pages are a module's) has no /x page at all.
  if (!info || !isVisible(info.state)) return <PluginDisabledPage id={id} info={info} />;
  if (info.generic.length === 0) return <NotFound />;
  return (
    <Page>
      <h2 className="text-lg font-semibold text-fg">{info.name}</h2>
      {info.generic.map((view, i) => (
        <Reveal key={view.id} index={i}>
          <GenericPanel info={info} view={view} />
        </Reveal>
      ))}
    </Page>
  );
}
