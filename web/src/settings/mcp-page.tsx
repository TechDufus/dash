import { useQueryClient } from "@tanstack/react-query";
import { Bot, Plug } from "lucide-react";
import { type JSX, useState } from "react";
import { toast } from "sonner";
import { Link } from "wouter";
import { api, errorMessage } from "@/api/client";
import { useMe } from "@/api/queries";
import type { SettingsView } from "@/api/types";
import { Page } from "@/components/page";
import { Panel, PanelHeader } from "@/components/panel";
import { HealthBadge } from "@/components/status";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import { invalidateSettings, settingsPaths, useSettingsView } from "./api";
import { lockedText, sourceText } from "./list-locked";

/** The setting this page switches; it applies live, without a restart. */
export const MCP_ENABLED = "mcp.enabled";
const PUBLIC_URL = "server.publicURL";

/** The MCP endpoint: server.publicURL (else this page's origin) plus /mcp. */
function endpointOf(view: SettingsView | undefined): string {
  const v = view?.general.find((f) => f.path === PUBLIC_URL)?.value;
  const base = typeof v === "string" && v !== "" ? v : window.location.origin;
  return `${base.replace(/\/+$/, "")}/mcp`;
}

/**
 * /settings/mcp: the switch of the read-only MCP server (mcp.enabled, written through PUT
 * general with the revision of the last read), whether dash serves it now (/me's
 * capabilities.mcp), and how to connect a client. The route's gate shows it to administrators
 * only, never in demo mode, which has no settings API.
 */
export function McpPage(): JSX.Element {
  const me = useMe();
  const view = useSettingsView();
  const queryClient = useQueryClient();
  const [pending, setPending] = useState(false);
  const field = view.data?.general.find((f) => f.path === MCP_ENABLED);
  const serving = me.data?.capabilities.mcp === true;
  const endpoint = endpointOf(view.data);
  const lock =
    (field && lockedText(field)) ??
    (view.data?.store.readonly ? "Settings are read-only" : undefined);

  async function toggle(enabled: boolean) {
    if (!view.data) return;
    setPending(true);
    try {
      await api.put(settingsPaths.general, { mcp: { enabled } }, { ifMatch: view.data.revision });
      toast.success(enabled ? "MCP server enabled" : "MCP server disabled");
    } catch (e) {
      toast.error(errorMessage(e));
    } finally {
      // After a failure too: a 412 leaves the next attempt the fresh revision. /me says whether
      // dash serves MCP now.
      await Promise.all([
        invalidateSettings(queryClient),
        queryClient.invalidateQueries({ queryKey: ["me"] }),
      ]);
      setPending(false);
    }
  }

  return (
    <Page>
      <h1 className="text-lg font-semibold text-fg">MCP</h1>
      {view.isError ? (
        <Alert variant="destructive">
          <AlertTitle>The settings could not be read</AlertTitle>
          <AlertDescription>{errorMessage(view.error)}</AlertDescription>
        </Alert>
      ) : null}
      <Panel>
        <PanelHeader title="MCP server" icon={<Bot />}>
          <HealthBadge
            health={serving ? "ok" : "unknown"}
            label={serving ? "serving" : "not serving"}
          />
        </PanelHeader>
        <div className="space-y-3 px-4 pb-4">
          <p className="text-sm text-muted">
            Read-only tools, resources and prompts for AI clients that present a token with the mcp
            audience. The switch applies at once, without a restart.
          </p>
          {field ? (
            <div className="flex flex-wrap items-center gap-3">
              <Switch
                id="mcp-enabled"
                checked={field.value === true}
                disabled={lock !== undefined || pending}
                aria-describedby={lock ? "mcp-enabled-lock" : undefined}
                onCheckedChange={(on) => void toggle(on)}
              />
              <Label htmlFor="mcp-enabled" className="text-sm text-fg">
                Serve MCP at /mcp
              </Label>
              <span className="text-xs text-faint">{sourceText(field)}</span>
              {lock ? (
                <p id="mcp-enabled-lock" className="w-full text-xs text-muted">
                  {lock}
                </p>
              ) : null}
            </div>
          ) : view.isPending ? (
            <p className="text-sm text-muted">Loading settings…</p>
          ) : null}
          {field?.value === true && !serving && me.data?.authMode === "none" ? (
            <p role="status" className="text-xs text-warn">
              dash never serves MCP in auth.mode none, which accepts no token.
            </p>
          ) : null}
        </div>
      </Panel>
      <Panel>
        <PanelHeader title="Connect a client" icon={<Plug />} />
        <div className="space-y-3 px-4 pb-4 text-sm text-fg">
          <p>
            Endpoint: <code className="num break-all text-accent">{endpoint}</code>
          </p>
          <p>
            Create a token with audience <span className="num">mcp</span> in{" "}
            <Link
              href="/settings/tokens"
              className="text-accent underline-offset-4 hover:underline"
            >
              Settings → Tokens
            </Link>
            , then give it to the client as a bearer token.
          </p>
          <div className="space-y-1">
            <p className="text-muted">Claude Code:</p>
            <pre className="overflow-x-auto rounded-lg border border-line bg-panel-hover p-3 text-xs">
              <code>{`claude mcp add --transport http dash ${endpoint} --header "Authorization: Bearer <token>"`}</code>
            </pre>
          </div>
        </div>
      </Panel>
    </Page>
  );
}
