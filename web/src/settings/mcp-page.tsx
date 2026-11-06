import { useQueryClient } from "@tanstack/react-query";
import { Bot, ClipboardCopy, Plug } from "lucide-react";
import { type JSX, useRef, useState } from "react";
import { toast } from "sonner";
import { Link } from "wouter";
import { api, errorMessage } from "@/api/client";
import { useMe } from "@/api/queries";
import type { SettingsView } from "@/api/types";
import { Page } from "@/components/page";
import { Panel, PanelHeader } from "@/components/panel";
import { HealthBadge } from "@/components/status";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import { copyElementText } from "@/lib/clipboard";
import { AgentPolicyPanel } from "./agent-policy";
import { invalidateSettings, settingsPaths, useSettingsView } from "./api";
import { liveLockText, sourceText } from "./list-locked";

/** The setting this page switches; it applies live, without a restart. */
export const MCP_ENABLED = "mcp.enabled";
const PUBLIC_URL = "server.publicURL";
const DIRECT_URL = "server.directURL";

/** The value of a general string setting, or "" when it is unset. */
function generalString(view: SettingsView | undefined, path: string): string {
  const v = view?.general.find((f) => f.path === path)?.value;
  return typeof v === "string" ? v : "";
}

/**
 * The MCP endpoint: server.directURL plus /mcp when it is set (bearer clients reach dash there,
 * past the identity proxy), else server.publicURL (else this page's origin) plus /mcp.
 */
function endpointOf(view: SettingsView | undefined): string {
  const base =
    generalString(view, DIRECT_URL) || generalString(view, PUBLIC_URL) || window.location.origin;
  return `${base.replace(/\/+$/, "")}/mcp`;
}

/**
 * /settings/mcp: the switch of the MCP server (mcp.enabled, written through PUT general with the
 * revision of the last read), whether dash serves it now (/me's capabilities.mcp), the agent
 * policy of each plugin with action tools (AgentPolicyPanel), and how to connect a client. The
 * route's gate shows it to administrators only, never in demo mode, which has no settings API.
 */
export function McpPage(): JSX.Element {
  const me = useMe();
  const view = useSettingsView();
  const queryClient = useQueryClient();
  const [pending, setPending] = useState(false);
  const field = view.data?.general.find((f) => f.path === MCP_ENABLED);
  const serving = me.data?.capabilities.mcp === true;
  const endpoint = endpointOf(view.data);
  const lock = field
    ? liveLockText(field, view.data?.store.readonly === true, "switch")
    : undefined;
  const command = `claude mcp add --transport http dash ${endpoint} --header "Authorization: Bearer <token>"`;
  const commandRef = useRef<HTMLElement>(null);

  /** Copies the command inside the click: writeText, else the shown text through execCommand. */
  function copyCommand() {
    const refused = () => toast.error("Copying failed: select the command and copy it yourself");
    const clipboard = globalThis.navigator?.clipboard;
    if (typeof clipboard?.writeText === "function") {
      clipboard.writeText(command).then(() => toast.success("Command copied"), refused);
    } else if (commandRef.current && copyElementText(commandRef.current)) {
      toast.success("Command copied");
    } else {
      refused();
    }
  }

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
            Tools, resources and prompts for AI clients that present a token with the mcp audience.
            Reading tools answer at once; plugin action tools follow each plugin's agent policy.
            {lock ? null : " The switch applies at once, without a restart."}
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
              {lock ? null : <span className="text-xs text-faint">{sourceText(field)}</span>}
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
      {view.data ? <AgentPolicyPanel view={view.data} /> : null}
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
            <div className="flex items-center justify-between gap-2">
              <p className="text-muted">Claude Code:</p>
              <Button type="button" variant="outline" size="xs" onClick={copyCommand}>
                <ClipboardCopy aria-hidden="true" />
                Copy command
              </Button>
            </div>
            <pre className="rounded-lg border border-line bg-panel-hover p-3 text-xs whitespace-pre-wrap wrap-anywhere">
              <code ref={commandRef}>{command}</code>
            </pre>
          </div>
        </div>
      </Panel>
    </Page>
  );
}
