import { screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import type { CNPGCluster, CNPGClustersData } from "@/api/types";
import { connectStream, type TestStream } from "@/plugins/test-util";
import { pluginInfo, renderApp, stubFetch } from "@/test/harness";
import { CnpgPanel, panels } from "./panels";

const HOUR = 3600;

const cluster = (name: string, over: Partial<CNPGCluster> = {}): CNPGCluster => ({
  ref: `cnpg:Cluster/db/${name}`,
  namespace: "db",
  name,
  instances: 3,
  readyInstances: 3,
  phase: "Cluster in healthy state",
  lastBackupAgeSeconds: HOUR,
  links: [],
  ...over,
});

const data = (...clusters: CNPGCluster[]): CNPGClustersData => ({ clusters });

describe("the CNPG panel", () => {
  let stream: TestStream;
  beforeEach(() => {
    stubFetch();
    stream = connectStream();
  });
  afterEach(() => stream.close());

  // The stream listens only to topics a plugin row lists: send the row before the topic.
  function show(payload?: CNPGClustersData) {
    renderApp(<CnpgPanel />);
    stream.plugins([pluginInfo({ id: "cnpg", topics: ["cnpg.clusters"] })]);
    if (payload) stream.send("cnpg.clusters", payload);
  }

  it("is the half-width panel in the slot's fourth place, reading cnpg.clusters", () => {
    expect(panels.storage).toEqual([
      expect.objectContaining({ id: "cnpg", order: 40, reads: ["cnpg.clusters"] }),
    ]);
    expect(panels.storage?.[0]?.span).toBeUndefined();
  });

  it("draws a skeleton, not an empty list, until the topic arrives", () => {
    show();
    expect(document.querySelector("[aria-busy=true]")).not.toBeNull();
    expect(screen.queryByText("No CNPG clusters.")).toBeNull();
    expect(screen.queryByRole("heading", { name: "Postgres clusters (CNPG)" })).toBeNull();

    stream.send("cnpg.clusters", data());
    expect(document.querySelector("[aria-busy=true]")).toBeNull();
    expect(screen.getByText("No CNPG clusters.")).toBeInTheDocument();
  });

  it("draws each cluster with its ready instances, phase and last backup", () => {
    show(data(cluster("main", { readyInstances: 2 })));
    expect(screen.getByRole("heading", { name: "Postgres clusters (CNPG)" })).toBeInTheDocument();
    expect(screen.getByText("main")).toBeInTheDocument();
    expect(screen.getByText("2/3")).toBeInTheDocument();
    expect(screen.getByText("Cluster in healthy state")).toBeInTheDocument();
    expect(screen.getByText("Fresh")).toBeInTheDocument();
  });

  it("says when a cluster has no recorded backup", () => {
    show(data(cluster("main", { lastBackupAgeSeconds: undefined })));
    expect(screen.getByText("No success recorded")).toBeInTheDocument();
  });

  it("follows the next payload", () => {
    show(data(cluster("main")));
    stream.send("cnpg.clusters", data(cluster("other")));
    expect(screen.queryByText("main")).toBeNull();
    expect(screen.getByText("other")).toBeInTheDocument();
  });
});
