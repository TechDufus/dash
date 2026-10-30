import { keepPreviousData, QueryClient, useQuery } from "@tanstack/react-query";
import { api } from "./client";
import type { LogLine, Me, PodDetail, Series, VMDetail } from "./types";

export const queryClient = new QueryClient({
  defaultOptions: { queries: { retry: 1, refetchOnWindowFocus: false } },
});

export type SeriesName =
  | "host.power"
  | "host.cpuTemp"
  | "host.cpu"
  | "host.mem"
  | "host.temps"
  | "host.fans"
  | "vm.cpu"
  | "vm.mem"
  | "vm.netIn"
  | "vm.netOut"
  | "vm.diskRead"
  | "vm.diskWrite"
  | "node.cpu"
  | "node.mem"
  | "pod.cpu"
  | "pod.mem";

export type SeriesRange = "15m" | "1h" | "6h" | "24h" | "7d";

export interface SeriesParams {
  vmid?: number | string;
  node?: string;
  ns?: string;
  pod?: string;
}

/** Time series for charts: stale after 30 s and refetched every 30 s. */
export function useSeries(
  name: SeriesName,
  range: SeriesRange,
  params: SeriesParams = {},
  enabled = true,
) {
  return useQuery({
    queryKey: ["series", name, range, params],
    queryFn: () => api.get<Series>("/api/series", { name, range, ...params }),
    staleTime: 30_000,
    refetchInterval: 30_000,
    placeholderData: keepPreviousData,
    enabled,
  });
}

export function useMe() {
  return useQuery({
    queryKey: ["me"],
    queryFn: () => api.get<Me>("/api/me"),
    staleTime: Number.POSITIVE_INFINITY,
  });
}

/** Detail fetched on open. */
export function useVMDetail(vmid: string | number | undefined) {
  return useQuery({
    queryKey: ["vm", String(vmid)],
    queryFn: () => api.get<VMDetail>(`/api/vms/${vmid}`),
    enabled: vmid !== undefined,
    staleTime: 0,
  });
}

export function usePodDetail(ns: string | undefined, name: string | undefined) {
  return useQuery({
    queryKey: ["pod", ns, name],
    queryFn: () =>
      api.get<PodDetail>(
        `/api/pods/${encodeURIComponent(ns ?? "")}/${encodeURIComponent(name ?? "")}`,
      ),
    enabled: !!ns && !!name,
    staleTime: 0,
  });
}

export type LogSince = "5m" | "15m" | "1h" | "6h" | "24h";

export interface LogQuery {
  ns: string;
  pod: string;
  container?: string;
  q?: string;
  since: LogSince;
}

/** Logs are fetched on demand (when the tab is open); never polled. */
export function useLogs(query: LogQuery, enabled: boolean) {
  return useQuery({
    queryKey: ["logs", query],
    queryFn: () => api.get<LogLine[]>("/api/logs", { ...query }),
    enabled,
    staleTime: Number.POSITIVE_INFINITY,
    gcTime: 60_000,
  });
}
