import { useSyncExternalStore } from "react";
import type { Alert, Silence, WorkloadKind } from "./types";

/** A workload that can be restarted (subset of Workload plus what the dialog needs). */
export interface RestartTarget {
  kind: WorkloadKind;
  namespace: string;
  name: string;
  protected: boolean;
}

interface UiState {
  restart: RestartTarget | null;
  silence: Alert | null;
  expire: Silence | null;
  palette: boolean;
}

let state: UiState = { restart: null, silence: null, expire: null, palette: false };
const listeners = new Set<() => void>();

function set(patch: Partial<UiState>) {
  state = { ...state, ...patch };
  for (const l of listeners) l();
}

function subscribe(l: () => void) {
  listeners.add(l);
  return () => {
    listeners.delete(l);
  };
}

export function useUi(): UiState {
  return useSyncExternalStore(subscribe, () => state);
}

export const ui = {
  openRestart: (restart: RestartTarget) => set({ restart, palette: false }),
  closeRestart: () => set({ restart: null }),
  openSilence: (silence: Alert) => set({ silence }),
  closeSilence: () => set({ silence: null }),
  openExpire: (expire: Silence) => set({ expire }),
  closeExpire: () => set({ expire: null }),
  setPalette: (palette: boolean) => set({ palette }),
  togglePalette: () => set({ palette: !state.palette }),
};
