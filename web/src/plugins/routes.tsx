import { Component, type ErrorInfo, lazy, type ReactNode, Suspense } from "react";
import { Link, Route, Switch } from "wouter";
import { useMe } from "@/api/queries";
import type { PluginInfo } from "@/api/types";
import { Page } from "@/components/page";
import { Empty, Panel, PanelSkeleton } from "@/components/panel";
import { Button } from "@/components/ui/button";
import { AppsPage } from "@/pages/apps";
import { OverviewPage } from "@/pages/overview";
import { StoragePage } from "@/pages/storage";
import { GenericPluginPage } from "./generic/generic";
import { MODULES } from "./registry";
import { isVisible, usePluginsState } from "./state";
import type { ModuleRoute as ModuleRouteDecl, PluginModule } from "./types";

// The settings pages are their own chunks: only an administrator ever loads them.
const SettingsPage = lazy(() =>
  import("@/settings/settings-page").then((m) => ({ default: m.SettingsPage })),
);
const PluginSettingsPage = lazy(() =>
  import("@/settings/plugin-settings-page").then((m) => ({ default: m.PluginSettingsPage })),
);
const AuditPage = lazy(() =>
  import("@/settings/audit-page").then((m) => ({ default: m.AuditPage })),
);
const TokensPage = lazy(() =>
  import("@/settings/tokens-page").then((m) => ({ default: m.TokensPage })),
);

export function PageLoading() {
  return (
    <Page>
      <PanelSkeleton lines={5} />
    </Page>
  );
}

export function NotFound() {
  return (
    <Page>
      <Panel className="p-10 text-center text-sm text-muted">
        Nothing here. Pick a page from the rail.
      </Panel>
    </Page>
  );
}

/** Catches a render error (and a lazy chunk that failed to load) so only the page area fails. */
export class RouteBoundary extends Component<{ children: ReactNode }, { failed: boolean }> {
  override state = { failed: false };

  static getDerivedStateFromError() {
    return { failed: true };
  }

  override componentDidCatch(error: Error, info: ErrorInfo) {
    console.error("dash: a page failed", error, info.componentStack);
  }

  override render() {
    if (!this.state.failed) {
      return <Suspense fallback={<PageLoading />}>{this.props.children}</Suspense>;
    }
    return (
      <Page>
        <Panel className="space-y-3 p-10 text-center text-sm text-muted" role="alert">
          <p>This page failed to load.</p>
          <Button variant="outline" size="sm" onClick={() => window.location.reload()}>
            Reload
          </Button>
        </Panel>
      </Page>
    );
  }
}

const STATE_WORDS: Record<string, string> = {
  unavailable: "unavailable",
  unconfigured: "not configured",
  blocked: "blocked",
};

/** A row message that already starts with "not configured" (optionally ": set plugins.<id>.url"). */
const NOT_CONFIGURED = /^not configured\b/;

/**
 * What a hidden plugin's route shows instead of its page. The text never carries more than
 * `info.message`, which the server already keeps viewer-safe.
 */
export function PluginDisabledPage({ id, info }: { id: string; info: PluginInfo | undefined }) {
  const name = info?.name || id;
  let body: ReactNode;
  if (!info) {
    body = `This dash has no ${id} plugin.`;
  } else if (info.state === "disabled" && !NOT_CONFIGURED.test(info.message)) {
    body = (
      <>
        {name} is switched off. Set <code className="num">plugins.{info.id}.enabled</code> in the
        configuration to turn it on.
      </>
    );
  } else if (NOT_CONFIGURED.test(info.message)) {
    // The row already says what to set ("not configured: set plugins.gatus.url"): once, as the
    // reason, for a plugin that is off for lack of settings (disabled) and one that was asked
    // to run without them (unconfigured) alike.
    body = `${name} is ${info.message}.`;
  } else {
    body = (
      <>
        {name} is {STATE_WORDS[info.state] ?? info.state}.{info.message ? ` ${info.message}` : ""}
      </>
    );
  }
  return (
    <Page>
      <Panel className="space-y-3 p-2">
        <Empty>
          <div role="status" className="space-y-3">
            <p>{body}</p>
            <Link href="/" className="text-accent underline-offset-4 hover:underline">
              Back to Overview
            </Link>
          </div>
        </Empty>
      </Panel>
    </Page>
  );
}

function ModuleRoute({
  module,
  route,
  params,
}: {
  module: PluginModule;
  route: ModuleRouteDecl;
  params: Readonly<Record<string, string | undefined>>;
}) {
  const { ready, byId } = usePluginsState();
  // Until the plugin list is known nothing mounts: a hidden plugin's page must never fetch.
  if (!ready) return <PageLoading />;
  const info = byId.get(module.id);
  if (!info || !isVisible(info.state)) return <PluginDisabledPage id={module.id} info={info} />;
  const RoutePage = route.page;
  return (
    <RouteBoundary>
      <RoutePage params={params} />
    </RouteBoundary>
  );
}

/**
 * The gate of every /settings page: an administrator gets the page, anyone else (a viewer, the
 * anonymous viewer) "Not authorised", and the page, which reads the settings API, never mounts.
 * The API refuses them anyway; this keeps the request from being made at all.
 */
export function AdminOnly({ children }: { children: ReactNode }) {
  const me = useMe();
  if (me.isPending) return <PageLoading />;
  if (me.data?.role !== "admin") {
    return (
      <Page>
        <Panel className="space-y-3 p-2">
          <Empty>
            <div role="status" className="space-y-3">
              <p>Not authorised: the settings are for administrators.</p>
              <Link href="/" className="text-accent underline-offset-4 hover:underline">
                Back to Overview
              </Link>
            </div>
          </Empty>
        </Panel>
      </Page>
    );
  }
  return <RouteBoundary>{children}</RouteBoundary>;
}

export function PluginRoutes() {
  return (
    <Switch>
      <Route path="/">
        <OverviewPage />
      </Route>
      <Route path="/storage">
        <StoragePage />
      </Route>
      <Route path="/apps">
        <AppsPage />
      </Route>
      <Route path="/settings">
        <AdminOnly>
          <SettingsPage />
        </AdminOnly>
      </Route>
      <Route path="/settings/audit">
        <AdminOnly>
          <AuditPage />
        </AdminOnly>
      </Route>
      <Route path="/settings/tokens">
        {/* Every interactive user's page (D8): it explains itself where tokens do not exist. */}
        <RouteBoundary>
          <TokensPage />
        </RouteBoundary>
      </Route>
      <Route path="/settings/plugins/:id">
        {(params) => (
          // Keyed by plugin: one plugin's form state must not carry over to the next.
          <AdminOnly key={params.id}>
            <PluginSettingsPage id={params.id ?? ""} />
          </AdminOnly>
        )}
      </Route>
      {MODULES.flatMap((m) =>
        (m.routes ?? []).map((r) => (
          <Route key={`${m.id}:${r.path}`} path={r.path}>
            {(params) => <ModuleRoute module={m} route={r} params={params} />}
          </Route>
        )),
      )}
      <Route path="/x/:id">
        {(params) => (
          // Keyed by plugin: a page that failed must not keep its failure for the next plugin.
          <RouteBoundary key={params.id}>
            <GenericPluginPage id={params.id ?? ""} />
          </RouteBoundary>
        )}
      </Route>
      <Route>
        <NotFound />
      </Route>
    </Switch>
  );
}
