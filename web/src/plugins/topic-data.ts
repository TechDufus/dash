import type { OverviewData, PluginsData, Topic } from "@/api/types";
import type { MODULES } from "./registry";
import { type TopicDecl, topic } from "./types";

// Which payload type each SSE topic carries, derived from what declares it: the core topics here,
// every other topic in its module's `topics` (registry.ts lists the modules). The Go side names
// the topics (internal/model Topic*), so the check below fails to compile when the two disagree.

/** The topics dash itself publishes (the core.* names). */
export const CORE_TOPICS = {
  "core.plugins": topic<PluginsData>(),
  "core.overview": topic<OverviewData>(),
} as const;

type ModuleTopics = (typeof MODULES)[number]["topics"];
type Merge<U> = (U extends unknown ? (k: U) => void : never) extends (k: infer I) => void
  ? I
  : never;
type Decl<M> = { [K in keyof M]: M[K] extends TopicDecl<infer T> ? T : never };

/** The payload (Envelope.data) of every topic, by name. */
export type TopicData = Decl<typeof CORE_TOPICS> & Decl<Merge<ModuleTopics>>;

// A Go Topic* constant that no module or core declares, and a declared topic that Go does not
// publish, are both compile errors.
type Exact = [Topic] extends [keyof TopicData]
  ? [keyof TopicData] extends [Topic]
    ? true
    : never
  : never;
export const _topicsExact: Exact = true;
