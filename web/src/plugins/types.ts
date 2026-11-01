import type { LucideIcon } from "lucide-react";
import type { ComponentType, LazyExoticComponent } from "react";
import type { TopicData, TypedEnvelope } from "@/api/topics";
import type { OverviewData, Topic } from "@/api/types";

declare const topicBrand: unique symbol;

/** A phantom declaration: which payload type a topic carries. The runtime value is `{}`. */
export interface TopicDecl<T> {
  readonly [topicBrand]?: T;
}
export const topic = <T>(): TopicDecl<T> => ({});
export type TopicMap = Readonly<Record<string, TopicDecl<unknown>>>;

export interface RouteProps {
  params: Readonly<Record<string, string | undefined>>;
}

export interface ModuleRoute {
  /** A wouter pattern, e.g. "/vms/:vmid?" or "/cluster/*?". */
  path: string;
  /** A lazy page: its own chunk. The adapter turns route params into the page's props. */
  page: LazyExoticComponent<ComponentType<RouteProps>>;
}

export interface PaletteItem {
  id: string;
  /** cmdk search text, e.g. `vm ${name} ${vmid}`. */
  value: string;
  label: string;
  /** Right-aligned muted text. */
  hint?: string;
  icon?: LucideIcon;
  onSelect(): void;
}

export interface PaletteGroup {
  id: string;
  heading: string;
  items: readonly PaletteItem[];
}

export interface PaletteCtx {
  /** The current payload of a topic (not reactive: the palette re-renders on every stream change while open). */
  topic<T extends Topic>(name: T): TopicData[T] | undefined;
  /** Navigates and closes the palette. */
  go(to: string): void;
  close(): void;
}

export interface PluginModule<Id extends string = string, T extends TopicMap = TopicMap> {
  /** Equals the Go Manifest.ID and the directory name under modules/. */
  id: Id;
  /** Rail position. Default: after every numbered entry, in registry order. */
  order?: number;
  /** The topics the module's pages read, typed. Names are the published names (legacy ones until their G3 leaf). */
  topics: T;
  routes?: readonly ModuleRoute[];
  /** A badge component per nav path, drawn on the rail entry (alertmanager: the firing count). */
  badge?: Readonly<Record<string, ComponentType>>;
  /** Command-palette groups. Groups with the same id merge across modules; the first heading wins. */
  palette?(ctx: PaletteCtx): readonly PaletteGroup[];
  /** Icons for the Link.kind values this module's links carry. */
  linkIcons?: Readonly<Record<string, LucideIcon>>;
}

export function definePlugin<const Id extends string, const T extends TopicMap>(
  m: PluginModule<Id, T>,
): PluginModule<Id, T> {
  return m;
}

// --- slots (S3) ---
export type SlotId = "overview.kpis" | "storage" | "apps.sources";

export interface SlotProps {
  "overview.kpis": { data: OverviewData };
  storage: { env: TypedEnvelope<"storage"> | undefined };
  "apps.sources": { query: string; compact?: boolean };
}

export interface PanelContribution<S extends SlotId = SlotId> {
  /** Unique within the module. */
  id: string;
  /** Position within the slot. Default: the module's `order`, then declaration order. */
  order?: number;
  /** `storage` only: a full row or half a row (default half). */
  span?: "full" | "half";
  /** Topics the panel reads. It is hidden when any is unavailable, so it never waits for a topic nobody publishes. */
  reads?: readonly Topic[];
  component: ComponentType<SlotProps[S]>;
}

export type ModulePanels = { [S in SlotId]?: readonly PanelContribution<S>[] };
// --- /slots (S3) ---
