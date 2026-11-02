import { screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import type { Bucket, RustFSUsageData } from "@/api/types";
import { connectStream, type TestStream } from "@/plugins/test-util";
import { pluginInfo, renderApp, stubFetch } from "@/test/harness";
import { ObjectStoragePanel, panels } from "./panels";

const bucket = (name: string, over: Partial<Bucket> = {}): Bucket => ({
  ref: `rustfs:Bucket/${name}`,
  name,
  usedBytes: 5,
  quotaBytes: 10,
  health: "ok",
  ...over,
});

const data = (...buckets: Bucket[]): RustFSUsageData => ({
  buckets,
  capacity: { usedBytes: 1, usableBytes: 2 },
});

describe("the object storage panel", () => {
  let stream: TestStream;
  beforeEach(() => {
    stubFetch();
    stream = connectStream();
  });
  afterEach(() => stream.close());

  // The stream listens only to topics a plugin row lists: send the row before the topic.
  function show(payload?: RustFSUsageData) {
    renderApp(<ObjectStoragePanel />);
    stream.plugins([pluginInfo({ id: "rustfs", topics: ["rustfs.usage"] })]);
    if (payload) stream.send("rustfs.usage", payload);
  }

  it("is the half-width panel in the slot's second place, reading rustfs.usage", () => {
    expect(panels.storage).toEqual([
      expect.objectContaining({ id: "object-storage", order: 20, reads: ["rustfs.usage"] }),
    ]);
    expect(panels.storage?.[0]?.span).toBeUndefined();
  });

  it("draws a skeleton, not an empty list, until the topic arrives", () => {
    show();
    expect(document.querySelector("[aria-busy=true]")).not.toBeNull();
    expect(screen.queryByText("No buckets.")).toBeNull();
    expect(screen.queryByRole("heading", { name: "Object storage" })).toBeNull();

    stream.send("rustfs.usage", data());
    expect(document.querySelector("[aria-busy=true]")).toBeNull();
    expect(screen.getByText("No buckets.")).toBeInTheDocument();
  });

  it("draws each bucket and the RustFS drive", () => {
    show(data(bucket("media"), bucket("backups", { quotaBytes: 0 })));
    expect(screen.getByRole("heading", { name: "Object storage" })).toBeInTheDocument();
    expect(screen.getByText("media")).toBeInTheDocument();
    expect(screen.getByText("backups")).toBeInTheDocument();
    expect(screen.getByText("RustFS drive (usable)")).toBeInTheDocument();
  });

  it("follows the next payload", () => {
    show(data(bucket("media")));
    stream.send("rustfs.usage", data(bucket("photos")));
    expect(screen.queryByText("media")).toBeNull();
    expect(screen.getByText("photos")).toBeInTheDocument();
  });
});
