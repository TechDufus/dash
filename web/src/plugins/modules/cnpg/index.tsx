import type { CNPGClustersData } from "@/api/types";
import { definePlugin, topic } from "../../types";

/** The CloudNativePG clusters; the module draws one panel on the Storage page and has no route. */
export default definePlugin({
  id: "cnpg",
  order: 32,
  topics: { "cnpg.clusters": topic<CNPGClustersData>() },
});
