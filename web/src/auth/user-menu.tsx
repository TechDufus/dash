import { BellRing, KeyRound, Loader2, LogIn, LogOut, UserRound, UsersRound } from "lucide-react";
import { type JSX, useId, useState } from "react";
import { toast } from "sonner";
import { Link } from "wouter";
import { pushAvailable } from "@/api/queries";
import type { Me } from "@/api/types";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import { Button } from "@/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { dropThisBrowser } from "@/settings/devices-api";
import { browserNav, safeNext } from "./handle401";
import { AuthRequestError, type LogoutScope, logout } from "./session";

/** The logouts that ask first, and what each confirmation says it ends (D23). */
const CONFIRM: Record<Exclude<LogoutScope, "this">, { title: string; body: string; go: string }> = {
  user: {
    title: "Log out everywhere?",
    body: "Every session of yours ends, on every device, and every app you connected is disconnected. Your personal access tokens stay.",
    go: "Log out everywhere",
  },
  all: {
    title: "Sign out all users?",
    body: "Every session of every user ends, you included, and every connected app of every user is disconnected. Personal access tokens stay.",
    go: "Sign out all users",
  },
};

/**
 * The topbar's account menu in the session modes (local, oidc): who is signed in, their "Tokens"
 * page, their "Devices" page where push is available, "Log out" (this session), "Log out
 * everywhere" (every session of the user, and their connected apps) and, for an administrator,
 * "Sign out all users" (every session and every connected app of every user). Both of the latter
 * ask first; tokens survive either. Where push is available a help line says what a logout does
 * to it: dash stops pushing to the user's devices (every user's, for Sign out all users) until
 * they subscribe again. Nothing in the other modes, where dash has no session to end. After a
 * logout the browser loads /login; a 401 means the session was gone already, which is the same
 * outcome. The anonymous viewer gets a "Sign in" link instead.
 */
export function UserMenu({ me }: { me: Me }): JSX.Element | null {
  const [pending, setPending] = useState(false);
  const [confirming, setConfirming] = useState<Exclude<LogoutScope, "this"> | null>(null);
  const outHelp = useId();
  const allHelp = useId();
  if (me.authMode !== "local" && me.authMode !== "oidc") return null;
  // The anonymous viewer exists only in the local mode, whose one account is the administrator:
  // a local viewer is therefore the anonymous viewer, who has no session to end.
  if (me.authMode === "local" && me.role === "viewer") {
    return (
      <Button asChild variant="ghost" size="sm">
        <a href={`/login?next=${encodeURIComponent(safeNext(window.location))}`}>
          <LogIn aria-hidden="true" />
          Sign in
        </a>
      </Button>
    );
  }

  async function out(scope: LogoutScope) {
    if (pending) return;
    setPending(true);
    try {
      // While the session still holds: this browser's push subscription, when it is this user's.
      await dropThisBrowser(me.user);
      await logout(scope);
    } catch (err) {
      if (!(err instanceof AuthRequestError && err.status === 401)) {
        setPending(false);
        setConfirming(null);
        toast.error(
          err instanceof AuthRequestError && err.status === 403
            ? "Only an administrator can sign out all users."
            : "Logging out failed. Try again.",
        );
        return;
      }
    }
    browserNav.assign("/login");
  }

  const confirm = confirming ? CONFIRM[confirming] : null;

  return (
    <>
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
          <DropdownMenuItem asChild>
            <Link href="/settings/tokens">
              <KeyRound aria-hidden="true" />
              Tokens
            </Link>
          </DropdownMenuItem>
          {pushAvailable(me) ? (
            <DropdownMenuItem asChild>
              <Link href="/settings/devices">
                <BellRing aria-hidden="true" />
                Devices
              </Link>
            </DropdownMenuItem>
          ) : null}
          <DropdownMenuItem
            disabled={pending}
            aria-describedby={pushAvailable(me) ? outHelp : undefined}
            onSelect={() => void out("this")}
          >
            <LogOut aria-hidden="true" />
            Log out
          </DropdownMenuItem>
          <DropdownMenuItem
            variant="destructive"
            disabled={pending}
            aria-describedby={pushAvailable(me) ? outHelp : undefined}
            onSelect={() => setConfirming("user")}
          >
            <LogOut aria-hidden="true" />
            Log out everywhere
          </DropdownMenuItem>
          {pushAvailable(me) ? (
            <p id={outHelp} className="px-2 pb-1.5 text-xs text-muted">
              Logging out stops push notifications on all your devices.
            </p>
          ) : null}
          {me.role === "admin" ? (
            <>
              <DropdownMenuItem
                variant="destructive"
                disabled={pending}
                aria-describedby={pushAvailable(me) ? allHelp : undefined}
                onSelect={() => setConfirming("all")}
              >
                <UsersRound aria-hidden="true" />
                Sign out all users
              </DropdownMenuItem>
              {pushAvailable(me) ? (
                <p id={allHelp} className="px-2 pb-1.5 text-xs text-muted">
                  Stops push notifications on every user's devices.
                </p>
              ) : null}
            </>
          ) : null}
        </DropdownMenuContent>
      </DropdownMenu>
      <AlertDialog
        open={confirm !== null}
        onOpenChange={(o) => !o && !pending && setConfirming(null)}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{confirm?.title}</AlertDialogTitle>
            <AlertDialogDescription>{confirm?.body}</AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel disabled={pending}>Cancel</AlertDialogCancel>
            <AlertDialogAction
              variant="destructive"
              disabled={pending}
              onClick={(e) => {
                e.preventDefault();
                if (confirming) void out(confirming);
              }}
            >
              {pending && <Loader2 className="animate-spin" />}
              {confirm?.go}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </>
  );
}
