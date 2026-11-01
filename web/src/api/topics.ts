// Which payload each SSE topic carries is declared by the modules (plugins/topic-data.ts).
import type { TopicData } from "@/plugins/topic-data";
import type { Envelope, Topic } from "./types";

export type { TopicData };

export type TypedEnvelope<T extends Topic> = Omit<Envelope, "data" | "topic"> & {
  topic: T;
  data: TopicData[T];
};
