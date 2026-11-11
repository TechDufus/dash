import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useMemo } from "react";
import { ApiError, api } from "@/api/client";
import type { History, Host, Sample, Stats } from "./model.gen";

/** How much history the charts keep, in seconds: what the plugin keeps. */
export const HISTORY_SECONDS = 3600;

export const historyKey = (name: string) => ["ssh", "history", name] as const;

export const historyPath = (name: string) =>
  `/api/v1/plugins/ssh/hosts/${encodeURIComponent(name)}/history`;

/** A sample of the topic cut down to what the history holds. */
export function toSample(s: Stats): Sample {
  return {
    t: Math.floor(Date.parse(s.sampledAt) / 1000),
    cpuRatio: s.cpuRatio,
    memRatio: s.memTotalBytes > 0 ? s.memUsedBytes / s.memTotalBytes : 0,
    load1: s.load1,
    netRxBytesPerSec: s.netRxBytesPerSec,
    netTxBytesPerSec: s.netTxBytesPerSec,
    diskReadBytesPerSec: s.diskReadBytesPerSec,
    diskWriteBytesPerSec: s.diskWriteBytesPerSec,
    ...(s.cpuTempC != null ? { cpuTempC: s.cpuTempC } : {}),
  };
}

/**
 * `base` with `more` merged in: one sample per time (a later one replaces an earlier one of the
 * same second), oldest first, and nothing older than HISTORY_SECONDS before the newest.
 */
export function mergeSamples(base: readonly Sample[], more: readonly Sample[]): Sample[] {
  const byT = new Map<number, Sample>();
  for (const s of base) if (Number.isFinite(s.t)) byT.set(s.t, s);
  for (const s of more) if (Number.isFinite(s.t)) byT.set(s.t, s);
  const all = [...byT.values()].sort((a, b) => a.t - b.t);
  const newest = all.at(-1)?.t;
  if (newest === undefined) return all;
  const from = newest - HISTORY_SECONDS;
  const first = all.findIndex((s) => s.t >= from);
  return first <= 0 ? all : all.slice(first);
}

/**
 * GET .../hosts/{name}/history, read once per host and kept: the topic's samples extend it in
 * place (useFeedHistories), so a new sample never refetches it. A 404 is a host the plugin does
 * not know (yet): no history, not an error.
 */
export function useHostHistory(name: string, enabled: boolean) {
  return useQuery({
    queryKey: historyKey(name),
    queryFn: async (): Promise<History> => {
      try {
        return await api.get<History>(historyPath(name));
      } catch (e) {
        if (e instanceof ApiError && e.status === 404) return { intervalSeconds: 0, samples: [] };
        throw e;
      }
    },
    enabled,
    staleTime: Number.POSITIVE_INFINITY,
    refetchOnWindowFocus: false,
    refetchOnReconnect: false,
  });
}

/**
 * Appends each host's latest sample to its history once that history has been read: one write
 * per new sample time, never a fetch. A sample the cache already holds changes nothing.
 */
export function useFeedHistories(hosts: readonly Host[] | undefined): void {
  const qc = useQueryClient();
  // The samples as one string, so the effect runs when a sample time changes and only then.
  const latest = useMemo(
    () =>
      (hosts ?? [])
        .filter((h) => h.stats)
        .map((h) => `${h.name}\u0000${h.stats?.sampledAt}`)
        .join("\u0001"),
    [hosts],
  );
  // biome-ignore lint/correctness/useExhaustiveDependencies: `latest` is what changes; `hosts` is read for it
  useEffect(() => {
    for (const h of hosts ?? []) {
      if (!h.stats) continue;
      const sample = toSample(h.stats);
      if (!Number.isFinite(sample.t)) continue;
      qc.setQueryData<History>(historyKey(h.name), (old) => {
        if (!old || old.samples.some((s) => s.t === sample.t)) return old;
        return { ...old, samples: mergeSamples(old.samples, [sample]) };
      });
    }
  }, [latest, qc]);
}
