import { TriangleAlert } from "lucide-react";
import { useMe } from "@/api/queries";
import { Panel } from "@/components/panel";

/**
 * The persistent warning of auth.mode=none: nobody is authenticated, so every visitor acts as an
 * administrator. It has no dismiss control on purpose and shows on every page the shell mounts it
 * on, until the operator sets another mode. It draws nothing while /me loads, in any other mode,
 * and for any other warning (those are for the administrator's own pages).
 */
export function AuthBanner() {
  const me = useMe().data;
  if (!me) return null;
  if (me.authMode !== "none" && !(me.warnings ?? []).includes("auth_none")) return null;
  return (
    <Panel
      role="alert"
      className="mx-4 mt-3 flex items-center gap-2 border-destructive/40 bg-destructive/10 p-3 text-sm font-medium text-destructive md:mx-6"
    >
      <TriangleAlert className="size-4 shrink-0" aria-hidden="true" />
      <p>
        Authentication is off (auth.mode=none): every visitor is an administrator. Set auth.mode to
        protect this dashboard.
      </p>
    </Panel>
  );
}
