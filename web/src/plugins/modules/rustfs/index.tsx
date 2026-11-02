import type { RustFSUsageData } from "@/api/types";
import { definePlugin, topic } from "../../types";

/** The RustFS buckets and drive; the module draws one panel on the Storage page and has no route. */
export default definePlugin({
  id: "rustfs",
  order: 34,
  topics: { "rustfs.usage": topic<RustFSUsageData>() },
});
