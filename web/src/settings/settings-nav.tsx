import { KeyRound } from "lucide-react";
import type { JSX } from "react";
import { Link } from "wouter";
import { canManageTokens, settingsAvailable, tokensAvailable, useMe } from "@/api/queries";
import { Page } from "@/components/page";
import { Panel, PanelHeader } from "@/components/panel";

/** The settings pages besides /settings itself, and who may open each. */
const PAGES = [
  {
    href: "/settings/audit",
    label: "Audit log",
    note: "Settings changes, actions and refused requests.",
    admin: true,
  },
  {
    href: "/settings/tokens",
    label: "Tokens",
    note: "Personal access tokens for scripts and agents.",
    admin: false,
  },
];

/**
 * The settings pages /me may open: an administrator every one (none in demo mode, which has no
 * settings API, so the audit log is not offered there), anyone else only Tokens, and that only
 * where tokens are kept. An administrator still finds Tokens where they are not: the page says
 * why.
 */
function usePages() {
  const me = useMe().data;
  const settings = settingsAvailable(me);
  const tokens = me?.role === "admin" ? canManageTokens(me) : tokensAvailable(me);
  return PAGES.filter((p) => (p.admin ? settings : tokens));
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
