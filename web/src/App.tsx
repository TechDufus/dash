import { QueryClientProvider } from "@tanstack/react-query";
import { MotionConfig } from "motion/react";
import { Route, Switch } from "wouter";
import { queryClient } from "@/api/queries";
import { Page } from "@/components/page";
import { Panel } from "@/components/panel";
import { TooltipProvider } from "@/components/ui/tooltip";
import { AlertsPage } from "@/pages/alerts";
import { AppsPage } from "@/pages/apps";
import { ClusterPage } from "@/pages/cluster";
import { GitOpsPage } from "@/pages/gitops";
import { HardwarePage } from "@/pages/hardware";
import { OverviewPage } from "@/pages/overview";
import { StatusPage } from "@/pages/status";
import { StoragePage } from "@/pages/storage";
import { VMsPage } from "@/pages/vms";
import { Shell } from "@/shell/shell";

function NotFound() {
  return (
    <Page>
      <Panel className="p-10 text-center text-sm text-muted">
        Nothing here. Pick a page from the rail.
      </Panel>
    </Page>
  );
}

export function App() {
  return (
    <QueryClientProvider client={queryClient}>
      <MotionConfig reducedMotion="user">
        <TooltipProvider delayDuration={150}>
          <Shell>
            <Switch>
              <Route path="/">
                <OverviewPage />
              </Route>
              <Route path="/vms/:vmid?">{(p) => <VMsPage vmid={p.vmid} />}</Route>
              <Route path="/cluster/*?">
                <ClusterPage />
              </Route>
              <Route path="/gitops">
                <GitOpsPage />
              </Route>
              <Route path="/hardware">
                <HardwarePage />
              </Route>
              <Route path="/storage">
                <StoragePage />
              </Route>
              <Route path="/alerts">
                <AlertsPage />
              </Route>
              <Route path="/status">
                <StatusPage />
              </Route>
              <Route path="/apps">
                <AppsPage />
              </Route>
              <Route>
                <NotFound />
              </Route>
            </Switch>
          </Shell>
        </TooltipProvider>
      </MotionConfig>
    </QueryClientProvider>
  );
}
