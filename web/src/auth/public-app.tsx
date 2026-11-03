import { QueryClientProvider } from "@tanstack/react-query";
import type { JSX } from "react";
import { useLocation } from "wouter";
import { queryClient } from "@/api/queries";
import { Toaster } from "@/components/ui/sonner";
import { isPublicPath as isPublic } from "./handle401";
import { LoginPage } from "./login-page";
import { SetupPage } from "./setup-page";

/** True for the paths the public shell serves: /login, /setup and anything below them. */
export function isPublicPath(pathname: string): boolean {
  return isPublic(pathname);
}

function PublicRoutes() {
  const [path] = useLocation();
  return path === "/setup" || path.startsWith("/setup/") ? <SetupPage /> : <LoginPage />;
}

/**
 * The shell of the signed-out pages. It is all a visitor without a session loads: no event stream,
 * no plugin list, no navigation, only the page and its own Toaster.
 */
export function PublicApp(): JSX.Element {
  return (
    <QueryClientProvider client={queryClient}>
      <PublicRoutes />
      <Toaster position="bottom-right" />
    </QueryClientProvider>
  );
}
