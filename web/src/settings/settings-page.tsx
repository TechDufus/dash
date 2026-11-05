import type { JSX } from "react";
import { Link } from "wouter";
import { errorMessage } from "@/api/client";
import { Page } from "@/components/page";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { useSettingsView } from "./api";
import { ExportButton } from "./export-button";
import { GeneralSection } from "./general-section";
import { readonlyReason } from "./list-locked";
import { PluginList } from "./plugin-list";

/**
 * /settings: the plugins with their live state and enable toggles, the general settings with
 * their provenance, the read-only banner and the export. It assumes an administrator: the route's
 * gate shows a viewer "Not authorised" before this page mounts.
 */
export function SettingsPage(): JSX.Element {
  const view = useSettingsView();

  return (
    <Page>
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h1 className="text-lg font-semibold text-fg">Settings</h1>
        <div className="flex items-center gap-3">
          <Link
            href="/settings/audit"
            className="text-sm text-accent underline-offset-4 hover:underline"
          >
            Audit log
          </Link>
          <ExportButton />
        </div>
      </div>
      {view.isError ? (
        <Alert variant="destructive">
          <AlertTitle>The settings could not be read</AlertTitle>
          <AlertDescription>{errorMessage(view.error)}</AlertDescription>
        </Alert>
      ) : null}
      {view.data ? (
        <>
          {view.data.store.readonly ? (
            <Alert role="status">
              <AlertTitle>Settings are read-only</AlertTitle>
              <AlertDescription>{readonlyReason(view.data.store)}</AlertDescription>
            </Alert>
          ) : null}
          <PluginList view={view.data} />
          <GeneralSection view={view.data} />
        </>
      ) : view.isPending ? (
        <p className="text-sm text-muted">Loading settings…</p>
      ) : null}
    </Page>
  );
}
