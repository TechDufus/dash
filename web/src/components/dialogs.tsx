import { Loader2, RotateCw } from "lucide-react";
import { useEffect, useState } from "react";
import { createSilence, expireSilence, restartWorkload } from "@/api/actions";
import { useTopic } from "@/api/stream";
import { ui, useUi } from "@/api/ui-store";
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
import { Checkbox } from "@/components/ui/checkbox";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Textarea } from "@/components/ui/textarea";

/** Restart confirmation; in a protected namespace the workload name must be typed. */
function RestartDialog() {
  const { restart } = useUi();
  const [typed, setTyped] = useState("");
  const [busy, setBusy] = useState(false);
  // Reset the typed text whenever a different target opens.
  const targetKey = restart ? `${restart.kind}/${restart.namespace}/${restart.name}` : "";
  // biome-ignore lint/correctness/useExhaustiveDependencies: reset on target change only
  useEffect(() => setTyped(""), [targetKey]);
  const needsConfirm = restart?.protected === true;
  const canGo = !!restart && (!needsConfirm || typed === restart.name) && !busy;
  return (
    <AlertDialog open={!!restart} onOpenChange={(o) => !o && !busy && ui.closeRestart()}>
      <AlertDialogContent className="data-[size=default]:sm:max-w-md">
        <AlertDialogHeader>
          <AlertDialogTitle>
            Restart {restart?.kind} {restart?.namespace}/{restart?.name}?
          </AlertDialogTitle>
          <AlertDialogDescription>
            This starts a rolling restart: Kubernetes replaces the pods one at a time using the
            workload's own update strategy. Brief interruptions are possible for single-replica
            workloads.
          </AlertDialogDescription>
        </AlertDialogHeader>
        {needsConfirm && restart && (
          <div className="space-y-2">
            <p className="text-xs text-warn">{restart.namespace} is a protected namespace.</p>
            <label htmlFor="restart-confirm" className="block text-sm text-muted">
              Type <span className="num text-fg">{restart.name}</span> to confirm
            </label>
            <Input
              id="restart-confirm"
              autoComplete="off"
              spellCheck={false}
              value={typed}
              onChange={(e) => setTyped(e.target.value)}
              className="num"
            />
          </div>
        )}
        <AlertDialogFooter>
          <AlertDialogCancel disabled={busy}>Cancel</AlertDialogCancel>
          <AlertDialogAction
            disabled={!canGo}
            onClick={async (e) => {
              e.preventDefault();
              if (!restart) return;
              setBusy(true);
              const ok = await restartWorkload({
                kind: restart.kind,
                namespace: restart.namespace,
                name: restart.name,
                confirm: typed,
              });
              setBusy(false);
              if (ok) ui.closeRestart();
            }}
          >
            {busy ? <Loader2 className="animate-spin" /> : <RotateCw />}
            Restart
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}

const PREFERRED_DURATION = "4h";

/** Silence dialog: choose labels to match (alertname is locked), a duration and a comment. */
function SilenceDialog() {
  const { silence } = useUi();
  // The lengths the alertmanager plugin offers (plugins.alertmanager.silenceDurations).
  const durations = useTopic("alertmanager.alerts")?.data.silenceDurations ?? [];
  const initialDuration = durations.includes(PREFERRED_DURATION)
    ? PREFERRED_DURATION
    : (durations[0] ?? "");
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const [duration, setDuration] = useState<string>(initialDuration);
  const [comment, setComment] = useState("");
  const [busy, setBusy] = useState(false);
  const fp = silence?.fingerprint ?? "";
  // biome-ignore lint/correctness/useExhaustiveDependencies: reset on a newly opened alert only
  useEffect(() => {
    setSelected(new Set(["alertname"]));
    setDuration(initialDuration);
    setComment("");
  }, [fp]);
  const labels = Object.entries(silence?.labels ?? {}).sort(([a], [b]) => {
    if (a === "alertname") return -1;
    if (b === "alertname") return 1;
    return a.localeCompare(b);
  });
  const valid =
    comment.trim().length >= 1 &&
    comment.length <= 500 &&
    selected.has("alertname") &&
    duration !== "";
  return (
    <Dialog open={!!silence} onOpenChange={(o) => !o && !busy && ui.closeSilence()}>
      <DialogContent className="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>Silence {silence?.name}</DialogTitle>
          <DialogDescription>
            Alertmanager will stop notifying for alerts matching the selected labels until the
            silence expires.
          </DialogDescription>
        </DialogHeader>
        <div className="space-y-4">
          <fieldset className="space-y-1.5">
            <legend className="mb-1 text-xs font-medium text-muted">Match labels</legend>
            <div className="max-h-48 space-y-1 overflow-y-auto rounded-lg border border-line bg-panel p-2">
              {labels.map(([k, v]) => {
                const locked = k === "alertname";
                const id = `silence-${k}`;
                return (
                  <label
                    key={k}
                    htmlFor={id}
                    className="flex cursor-pointer items-center gap-2 rounded px-1.5 py-1 text-sm hover:bg-panel-hover"
                  >
                    <Checkbox
                      id={id}
                      checked={selected.has(k)}
                      disabled={locked}
                      onCheckedChange={(c) =>
                        setSelected((prev) => {
                          const next = new Set(prev);
                          if (c) next.add(k);
                          else next.delete(k);
                          return next;
                        })
                      }
                    />
                    <span className="num text-muted">{k}</span>
                    <span className="num truncate text-fg">{v}</span>
                    {locked && <span className="ml-auto text-[11px] text-faint">required</span>}
                  </label>
                );
              })}
            </div>
          </fieldset>
          <div className="space-y-1.5">
            <label htmlFor="silence-duration" className="text-xs font-medium text-muted">
              Duration
            </label>
            <Select value={duration} onValueChange={setDuration}>
              <SelectTrigger id="silence-duration" className="w-32">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {durations.map((d) => (
                  <SelectItem key={d} value={d}>
                    {d}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
          <div className="space-y-1.5">
            <label htmlFor="silence-comment" className="text-xs font-medium text-muted">
              Comment (required)
            </label>
            <Textarea
              id="silence-comment"
              value={comment}
              maxLength={500}
              onChange={(e) => setComment(e.target.value)}
              placeholder="Why is this being silenced?"
            />
          </div>
        </div>
        <DialogFooter>
          <Button variant="outline" disabled={busy} onClick={() => ui.closeSilence()}>
            Cancel
          </Button>
          <Button
            disabled={!valid || busy}
            onClick={async () => {
              if (!silence) return;
              setBusy(true);
              const ok = await createSilence({
                matchers: labels
                  .filter(([k]) => selected.has(k))
                  .map(([name, value]) => ({ name, value })),
                duration,
                comment: comment.trim(),
              });
              setBusy(false);
              if (ok) ui.closeSilence();
            }}
          >
            {busy && <Loader2 className="animate-spin" />}
            Create silence
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

function ExpireDialog() {
  const { expire } = useUi();
  const [busy, setBusy] = useState(false);
  return (
    <AlertDialog open={!!expire} onOpenChange={(o) => !o && !busy && ui.closeExpire()}>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>Expire this silence?</AlertDialogTitle>
          <AlertDialogDescription>
            Matching alerts will notify again immediately.
            {expire?.comment ? ` Comment: "${expire.comment}"` : ""}
          </AlertDialogDescription>
        </AlertDialogHeader>
        <AlertDialogFooter>
          <AlertDialogCancel disabled={busy}>Keep</AlertDialogCancel>
          <AlertDialogAction
            disabled={busy}
            onClick={async (e) => {
              e.preventDefault();
              if (!expire) return;
              setBusy(true);
              const ok = await expireSilence(expire.id);
              setBusy(false);
              if (ok) ui.closeExpire();
            }}
          >
            {busy && <Loader2 className="animate-spin" />}
            Expire
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}

/** Mounted once; opened through the `ui` store from anywhere (rows, palette). */
export function GlobalDialogs() {
  return (
    <>
      <RestartDialog />
      <SilenceDialog />
      <ExpireDialog />
    </>
  );
}
