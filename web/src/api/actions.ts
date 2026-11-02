import { toast } from "sonner";
import { api, errorMessage } from "./client";
import type { RestartRequest, SilenceRequest, SilenceResponse } from "./types";

/** Rolling restart; the server message is shown verbatim on failure. Returns true on success. */
export async function restartWorkload(req: RestartRequest): Promise<boolean> {
  try {
    await api.post("/api/v1/plugins/kubernetes/actions/restart", req);
    toast.success(`Restart requested: ${req.kind} ${req.namespace}/${req.name}`);
    return true;
  } catch (e) {
    toast.error(errorMessage(e));
    return false;
  }
}

const SILENCE_ACTIONS = "/api/v1/plugins/alertmanager/actions";

export async function createSilence(req: SilenceRequest): Promise<boolean> {
  try {
    const res = await api.post<SilenceResponse>(`${SILENCE_ACTIONS}/silence.create`, req);
    toast.success(`Silence created`, { description: res.silenceID });
    return true;
  } catch (e) {
    toast.error(errorMessage(e));
    return false;
  }
}

export async function expireSilence(id: string): Promise<boolean> {
  try {
    await api.post(`${SILENCE_ACTIONS}/silence.expire`, { id });
    toast.success("Silence expired", { description: id });
    return true;
  } catch (e) {
    toast.error(errorMessage(e));
    return false;
  }
}
