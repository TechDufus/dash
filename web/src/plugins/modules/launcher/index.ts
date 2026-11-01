import type { LauncherData } from "@/api/types";
import { definePlugin, topic } from "../../types";

// The launcher has no page of its own: its Infrastructure section is a panel on Apps and the
// overview (slot contributions arrive with the panel slots). Its links are the launcher.links
// topic.
export default definePlugin({
  id: "launcher",
  order: 90,
  topics: { "launcher.links": topic<LauncherData>() },
});
