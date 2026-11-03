import { LogOut, UserRound } from "lucide-react";
import { type JSX, useState } from "react";
import { toast } from "sonner";
import type { Me } from "@/api/types";
import { Button } from "@/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { browserNav } from "./handle401";
import { AuthRequestError, logout } from "./session";

/**
 * The topbar's account menu in the session modes (local, oidc): who is signed in, "Log out" (this
 * session) and, for an administrator, "Log out everywhere" (every session of every user). Nothing in
 * the other modes, where dash has no session to end. After a logout the browser loads /login; a 401
 * means the session was gone already, which is the same outcome.
 */
export function UserMenu({ me }: { me: Me }): JSX.Element | null {
  const [pending, setPending] = useState(false);
  if (me.authMode !== "local" && me.authMode !== "oidc") return null;

  async function out(scope: "this" | "all") {
    if (pending) return;
    setPending(true);
    try {
      await logout(scope);
    } catch (err) {
      if (!(err instanceof AuthRequestError && err.status === 401)) {
        setPending(false);
        toast.error(
          err instanceof AuthRequestError && err.status === 403
            ? "Only an administrator can log everyone out."
            : "Logging out failed. Try again.",
        );
        return;
      }
    }
    browserNav.assign("/login");
  }

  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button variant="ghost" size="sm" aria-label={`Account: ${me.user}`}>
          <UserRound aria-hidden="true" />
          <span className="hidden max-w-32 truncate sm:inline">{me.user}</span>
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="w-56">
        <DropdownMenuLabel>
          <span className="block truncate text-fg">{me.user}</span>
          <span className="block text-xs font-normal text-muted">{me.role}</span>
        </DropdownMenuLabel>
        <DropdownMenuSeparator />
        <DropdownMenuItem disabled={pending} onSelect={() => void out("this")}>
          <LogOut aria-hidden="true" />
          Log out
        </DropdownMenuItem>
        {me.role === "admin" ? (
          <DropdownMenuItem
            variant="destructive"
            disabled={pending}
            onSelect={() => void out("all")}
          >
            <LogOut aria-hidden="true" />
            Log out everywhere
          </DropdownMenuItem>
        ) : null}
      </DropdownMenuContent>
    </DropdownMenu>
  );
}
