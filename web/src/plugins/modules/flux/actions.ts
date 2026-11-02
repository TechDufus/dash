import { toast } from "sonner";
import { api, errorMessage } from "@/api/client";
import type { FluxKind, ReconcileRequest } from "@/api/types";

/** The flux plugin's reconcile action (a framework action: 10 per minute per user, audited). */
export const RECONCILE_PATH = "/api/v1/plugins/flux/actions/reconcile";

/** Flux reconcile request: no dialog, a spinner on the caller then this toast. */
export async function reconcileFlux(
  kind: FluxKind,
  namespace: string,
  name: string,
): Promise<boolean> {
  const req: ReconcileRequest = { kind, namespace, name };
  try {
    await api.post(RECONCILE_PATH, req);
    toast.success(`Reconcile requested: ${kind} ${namespace}/${name}`);
    return true;
  } catch (e) {
    toast.error(errorMessage(e));
    return false;
  }
}
