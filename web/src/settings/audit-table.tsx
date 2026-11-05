import type { AuditEntry } from "@/api/types";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { formatDateTime } from "@/lib/format";
import { cn } from "@/lib/utils";

const TH = "h-9 px-3 text-xs font-medium text-muted";
const TD = "px-3 py-2 align-top";

/** What a record applied to: the target object, else the plugin, then the field paths. */
function subject(e: AuditEntry): string {
  const parts: string[] = [];
  const t = e.target;
  if (t && (t.kind || t.name)) {
    const name = [t.namespace, t.name].filter(Boolean).join("/");
    parts.push([t.kind, name].filter(Boolean).join(" "));
  } else if (e.plugin) {
    parts.push(e.plugin);
  }
  if (e.fields?.length) parts.push(e.fields.join(", "));
  return parts.join(" · ");
}

/** The outcome column: ok, error with its text, a denial's reason, or nothing for an intent. */
function outcome(e: AuditEntry): { text: string; bad: boolean } {
  if (e.kind === "denial") return { text: e.reason ?? "denied", bad: true };
  if (e.result === "error") return { text: e.error ? `error: ${e.error}` : "error", bad: true };
  return { text: e.result ?? "", bad: false };
}

/** Who acted, with the peer when the record has one (denials). */
function actor(e: AuditEntry): string {
  const who = e.actor.user ?? e.actor.type ?? "";
  return e.peer ? `${who} (${e.peer})` : who;
}

/** One page or more of audit records, newest first, as the server sent them. */
export function AuditTable({ records }: { records: AuditEntry[] }) {
  return (
    <Table>
      <TableHeader>
        <TableRow className="border-line hover:bg-transparent">
          <TableHead className={TH}>Time</TableHead>
          <TableHead className={TH}>Kind</TableHead>
          <TableHead className={TH}>Action</TableHead>
          <TableHead className={TH}>Subject</TableHead>
          <TableHead className={TH}>Actor</TableHead>
          <TableHead className={TH}>Result</TableHead>
        </TableRow>
      </TableHeader>
      <TableBody>
        {records.map((e, i) => {
          const o = outcome(e);
          return (
            // Records have no unique key: an intent and its outcome share the id.
            // biome-ignore lint/suspicious/noArrayIndexKey: the list is append-only, newest first
            <TableRow key={`${e.time}/${e.kind}/${e.id ?? ""}/${i}`} className="border-line">
              <TableCell className={`${TD} num text-muted`}>{formatDateTime(e.time)}</TableCell>
              <TableCell className={`${TD} text-muted`}>{e.kind}</TableCell>
              <TableCell className={`${TD} font-medium text-fg`}>{e.action ?? ""}</TableCell>
              <TableCell className={`${TD} max-w-xl whitespace-normal break-words`}>
                {subject(e)}
              </TableCell>
              <TableCell className={TD}>{actor(e)}</TableCell>
              <TableCell
                className={cn(
                  TD,
                  "max-w-md whitespace-normal break-words",
                  o.bad ? "text-destructive" : "text-muted",
                )}
              >
                {o.text}
              </TableCell>
            </TableRow>
          );
        })}
      </TableBody>
    </Table>
  );
}
