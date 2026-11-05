import { KeyRound } from "lucide-react";
import type { JSX } from "react";
import { Link } from "wouter";
import {
  canManageTokens,
  connectedAppsAvailable,
  pushAvailable,
  settingsAvailable,
  tokensAvailable,
  useMe,
} from "@/api/queries";
import type { Me } from "@/api/types";
import { Page } from "@/components/page";
import { Panel, PanelHeader } from "@/components/panel";

/**
 * Who may open a settings page: the settings API's administrator, a token user, an OAuth user,
 * a push user.
 */
type Gate = "admin" | "tokens" | "oauth" | "push";

/** The settings pages besides /settings itself, and who may open each. */
const PAGES: { href: string; label: string; note: string; gate: Gate }[] = [
  {
    href: "/settings/audit",
    label: "Audit log",
    note: "Settings changes, actions and refused requests.",
    gate: "admin",
  },
  {
    href: "/settings/mcp",
    label: "MCP",
    note: "The MCP server for AI clients.",
    gate: "admin",
  },
  {
    href: "/settings/tokens",
    label: "Tokens",
    note: "Personal access tokens for scripts and agents.",
    gate: "tokens",
  },
  {
    href: "/settings/connected-apps",
    label: "Connected apps",
    note: "Apps you let in with the consent screen.",
    gate: "oauth",
  },
  {
    href: "/settings/devices",
    label: "Devices",
    note: "Push notifications on your phones and browsers, and the app's QR code.",
    gate: "push",
  },
];

function opens(me: Me | undefined, gate: Gate): boolean {
  if (gate === "admin") return settingsAvailable(me);
  // An administrator still finds Tokens where they are not kept: the page says why.
  if (gate === "tokens") return me?.role === "admin" ? canManageTokens(me) : tokensAvailable(me);
  if (gate === "oauth") return connectedAppsAvailable(me);
  return pushAvailable(me);
}

/**
 * The settings pages /me may open: an administrator every one (none in demo mode, which has no
 * settings API, so the audit log is not offered there), anyone else only their own pages, Tokens
 * where tokens are kept. Connected apps is offered to everyone only where the OAuth server is on
 * (capabilities.oauth), Devices only where push is available (capabilities.push).
 */
function usePages() {
  const me = useMe().data;
  return PAGES.filter((p) => opens(me, p.gate));
}

/** The links of the /settings header. */
export function SettingsLinks(): JSX.Element {
  return (
    <>
      {usePages().map((p) => (
        <Link
          key={p.href}
          href={p.href}
          className="text-sm text-accent underline-offset-4 hover:underline"
        >
          {p.label}
        </Link>
      ))}
    </>
  );
}

/**
 * /settings for a user who is not an administrator: only the pages they may use (D8). The
 * settings API itself stays the administrator's, so this page reads nothing from it.
 */
export function UserSettingsPage(): JSX.Element {
  return (
    <Page>
      <h1 className="text-lg font-semibold text-fg">Settings</h1>
      <Panel>
        <PanelHeader title="Your settings" icon={<KeyRound />} />
        <ul className="divide-y divide-line">
          {usePages().map((p) => (
            <li key={p.href} className="flex flex-wrap items-center gap-3 px-4 py-3">
              <Link
                href={p.href}
                className="text-sm font-medium text-accent underline-offset-4 hover:underline"
              >
                {p.label}
              </Link>
              <span className="text-sm text-muted">{p.note}</span>
            </li>
          ))}
        </ul>
      </Panel>
    </Page>
  );
}
