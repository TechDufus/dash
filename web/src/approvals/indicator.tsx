import { ShieldQuestion } from "lucide-react";
import type { JSX } from "react";
import { Link } from "wouter";
import { approvalsAvailable } from "@/api/queries";
import { useTopicData } from "@/api/stream";
import type { ApprovalInfo, Me } from "@/api/types";
import { Button } from "@/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { approveRoute } from "./api";

/** A request's target as the list shows it: `media/jellyfin`, or the name alone. */
function targetPath(a: ApprovalInfo): string {
  return a.target.namespace ? `${a.target.namespace}/${a.target.name}` : a.target.name;
}

/**
 * The topbar's pending approvals (design O.3, D56): a badge with the count of the user's
 * requests waiting for a decision, from the per-user core.approvals topic, opening a list that
 * links to each request's /approve page. dash publishes the topic only to the user whose
 * requests changed, so a user without any has no envelope: that is zero, and zero shows nothing.
 * Nothing either for a principal dash offers no approvals.
 */
export function ApprovalsIndicator({ me }: { me: Me }): JSX.Element | null {
  const pending = useTopicData("core.approvals")?.pending ?? [];
  if (!approvalsAvailable(me) || pending.length === 0) return null;
  const n = pending.length;
  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button
          variant="ghost"
          size="sm"
          aria-label={`${n} pending ${n === 1 ? "approval" : "approvals"}`}
          className="text-warn hover:text-warn"
        >
          <ShieldQuestion aria-hidden="true" />
          <span className="num rounded-full bg-warn/15 px-1.5 text-xs font-semibold">{n}</span>
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="w-72">
        <DropdownMenuLabel>Agents waiting for your approval</DropdownMenuLabel>
        <DropdownMenuSeparator />
        {pending.map((a) => (
          <DropdownMenuItem key={a.id} asChild>
            <Link href={approveRoute(a.id)} className="flex-col items-start gap-0.5">
              <span className="text-fg">
                {a.title} <span className="num text-muted">{targetPath(a)}</span>
              </span>
              <span className="text-xs text-muted">
                {a.clientName || a.credentialName} · {a.plugin}
              </span>
            </Link>
          </DropdownMenuItem>
        ))}
      </DropdownMenuContent>
    </DropdownMenu>
  );
}
