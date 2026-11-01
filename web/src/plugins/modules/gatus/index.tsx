import { lazy } from "react";
import type { StatusData } from "@/api/types";
import { definePlugin, topic } from "../../types";

const StatusPage = lazy(() =>
  import("@/pages/status").then((m) => ({ default: () => <m.StatusPage /> })),
);

export default definePlugin({
  id: "gatus",
  order: 70,
  topics: { status: topic<StatusData>() },
  routes: [{ path: "/status", page: StatusPage }],
});
