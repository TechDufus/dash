import { type ReactNode, Suspense, useMemo } from "react";
import { Link } from "wouter";
import { ApiError } from "@/api/client";
import type { SettingsPluginView } from "@/api/types";
import { Page } from "@/components/page";
import { Empty, Panel, PanelSkeleton } from "@/components/panel";
import { moduleById } from "@/plugins/registry";
import { usePluginsState } from "@/plugins/state";
import { useSettingsPlugin } from "./api";
import { SchemaForm } from "./schema-form";

/** `view` with an unset `enabled` field's value taken from the live state: an instance runs
 * unless core.plugins says it is disabled. A set `enabled` keeps its value. */
function withLiveEnabled(view: SettingsPluginView, state: string | undefined): SettingsPluginView {
  const path = `plugins.${view.id}.enabled`;
  const f = view.fields.find((x) => x.path === path);
  if (!f || f.set || state === undefined) return view;
  return {
    ...view,
    fields: view.fields.map((x) => (x === f ? { ...f, value: state !== "disabled" } : x)),
  };
}

/**
 * /settings/plugins/:id (P4.9): the plugin's live state from core.plugins and its settings form,
 * the module's own (`PluginModule.settings`) when it provides one, the schema-driven form
 * otherwise. The page assumes an administrator: the route gate is the shell's.
 */
export function PluginSettingsPage({ id }: { id: string }) {
  const q = useSettingsPlugin(id);
  const info = usePluginsState().byId.get(id);
  const Custom = moduleById.get(id)?.settings;
  const state = info?.state;
  const data = useMemo(() => q.data && withLiveEnabled(q.data, state), [q.data, state]);

  let body: ReactNode;
  if (q.isError) {
    body = (
      <Empty>
        {q.error instanceof ApiError && q.error.status === 404
          ? `No plugin named ${id}.`
          : `The settings could not be read: ${q.error.message}`}
      </Empty>
    );
  } else if (!data) {
    body = <PanelSkeleton lines={6} />;
  } else if (Custom) {
    body = (
      <Suspense fallback={<PanelSkeleton lines={6} />}>
        <Custom view={data} />
      </Suspense>
    );
  } else {
    body = <SchemaForm key={id} view={data} />;
  }

  return (
    <Page>
      <div className="max-w-3xl space-y-4">
        <div className="space-y-1">
          <Link href="/settings" className="text-xs text-muted hover:text-fg">
            ← Settings
          </Link>
          <h1 className="text-lg font-semibold text-fg">{info?.name ?? id}</h1>
          {info && (
            <dl className="flex gap-2 text-sm">
              <dt className="text-muted">State</dt>
              <dd>{info.state}</dd>
              {info.message && <dd className="text-muted">{info.message}</dd>}
            </dl>
          )}
        </div>
        <Panel className="p-4">{body}</Panel>
      </div>
    </Page>
  );
}
