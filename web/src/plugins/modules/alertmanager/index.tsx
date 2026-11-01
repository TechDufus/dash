import { lazy } from "react";
import type { AlertsData } from "@/api/types";
import { definePlugin, topic } from "../../types";
import { AlertsBadge } from "./badge";

const AlertsPage = lazy(() =>
  import("@/pages/alerts").then((m) => ({ default: () => <m.AlertsPage /> })),
);

export default definePlugin({
  id: "alertmanager",
  order: 60,
  topics: { alerts: topic<AlertsData>() },
  routes: [{ path: "/alerts", page: AlertsPage }],
  badge: { "/alerts": AlertsBadge },
});
