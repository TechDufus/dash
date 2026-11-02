import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { BackupAge, backupHealth } from "./backup-age";

const HOUR = 3600;

// [age in seconds, health, badge]: the thresholds are 36 h and 72 h, both exclusive.
const cases: [string, number | undefined, string, string | undefined][] = [
  ["a fresh run", 1 * HOUR, "ok", "Fresh"],
  ["exactly 36 h", 36 * HOUR, "ok", "Fresh"],
  ["one second past 36 h", 36 * HOUR + 1, "warn", "Stale >36 h"],
  ["exactly 72 h", 72 * HOUR, "warn", "Stale >36 h"],
  ["one second past 72 h", 72 * HOUR + 1, "crit", "Stale >72 h"],
  ["no age", undefined, "unknown", undefined],
];

describe("backup freshness", () => {
  for (const [what, age, health, badge] of cases) {
    it(`is ${health} for ${what}`, () => {
      expect(backupHealth(age)).toBe(health);
      render(<BackupAge seconds={age} />);
      if (badge) expect(screen.getByText(badge)).toBeInTheDocument();
      else expect(screen.getByText("No success recorded")).toBeInTheDocument();
    });
  }

  it("says how long ago the run was", () => {
    render(<BackupAge seconds={3 * HOUR} />);
    expect(screen.getByText(/ago$/)).toBeInTheDocument();
  });
});
