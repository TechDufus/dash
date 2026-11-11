import { act, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";
import { Link, Router, useLocation, useSearchParams } from "wouter";
import { navigateHash, useHashRoute } from "./router";

function Probe() {
  const [path, navigate] = useLocation();
  const [params, setParams] = useSearchParams();
  return (
    <div>
      <p data-testid="path">{path}</p>
      <p data-testid="tab">{params.get("tab") ?? ""}</p>
      <button type="button" onClick={() => setParams({ tab: "events" })}>
        events
      </button>
      <button type="button" onClick={() => navigate("/vms")}>
        vms
      </button>
      <Link href="/cluster?tab=pods">pods</Link>
    </div>
  );
}

function renderProbe() {
  render(
    <Router hook={useHashRoute}>
      <Probe />
    </Router>,
  );
}

afterEach(() => {
  history.replaceState(null, "", "/");
});

describe("useHashRoute", () => {
  it("keeps path and query together in the fragment", () => {
    history.replaceState(null, "", "/demo/#/cluster?tab=pods");
    renderProbe();
    expect(screen.getByTestId("path")).toHaveTextContent("/cluster");
    expect(screen.getByTestId("tab")).toHaveTextContent("pods");
  });

  it("is the overview without a fragment", () => {
    history.replaceState(null, "", "/demo/");
    renderProbe();
    expect(screen.getByTestId("path")).toHaveTextContent(/^\/$/);
  });

  it("writes search params into the fragment and drops them on a navigation without", () => {
    history.replaceState(null, "", "/demo/#/cluster?tab=pods");
    renderProbe();
    act(() => screen.getByRole("button", { name: "events" }).click());
    expect(location.pathname).toBe("/demo/");
    expect(location.search).toBe("");
    expect(location.hash).toBe("#/cluster?tab=events");
    expect(screen.getByTestId("tab")).toHaveTextContent("events");

    act(() => screen.getByRole("button", { name: "vms" }).click());
    expect(location.hash).toBe("#/vms");
    expect(screen.getByTestId("path")).toHaveTextContent("/vms");
    expect(screen.getByTestId("tab")).toHaveTextContent("");
  });

  it("gives links a fragment href and follows them", () => {
    history.replaceState(null, "", "/demo/");
    renderProbe();
    const link = screen.getByRole("link", { name: "pods" });
    expect(link).toHaveAttribute("href", "#/cluster?tab=pods");
    act(() => link.click());
    expect(screen.getByTestId("path")).toHaveTextContent("/cluster");
    expect(screen.getByTestId("tab")).toHaveTextContent("pods");
  });

  it("follows a fragment the browser changes (back, forward, an edited address)", () => {
    history.replaceState(null, "", "/demo/");
    renderProbe();
    act(() => navigateHash("/alerts"));
    expect(screen.getByTestId("path")).toHaveTextContent("/alerts");
    act(() => navigateHash("/status", { replace: true }));
    expect(location.hash).toBe("#/status");
    act(() => {
      history.replaceState(null, "", "/demo/#/gitops");
      dispatchEvent(new HashChangeEvent("hashchange"));
    });
    expect(screen.getByTestId("path")).toHaveTextContent("/gitops");
  });
});
