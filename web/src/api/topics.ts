// Handwritten: maps each SSE topic (internal/model Topic*) to the type of its Envelope.data.
import type {
  AlertsData,
  AppsData,
  ClusterData,
  Envelope,
  FluxData,
  HostData,
  OverviewData,
  StatusData,
  StorageData,
  Topic,
  VMsData,
} from "./types";

export interface TopicData {
  overview: OverviewData;
  host: HostData;
  vms: VMsData;
  cluster: ClusterData;
  flux: FluxData;
  storage: StorageData;
  alerts: AlertsData;
  status: StatusData;
  apps: AppsData;
}

// Compile-time check that TopicData covers exactly the Go Topic union.
type _exact = [Topic] extends [keyof TopicData]
  ? [keyof TopicData] extends [Topic]
    ? true
    : never
  : never;
export const _topicsExact: _exact = true;

export const TOPICS = [
  "overview",
  "host",
  "vms",
  "cluster",
  "flux",
  "storage",
  "alerts",
  "status",
  "apps",
] as const satisfies readonly Topic[];

export type TypedEnvelope<T extends Topic> = Omit<Envelope, "data" | "topic"> & {
  topic: T;
  data: TopicData[T];
};
