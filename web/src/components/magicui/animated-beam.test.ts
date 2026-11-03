import { describe, expect, it } from "vitest";
import { type BeamRect, beamPath } from "./animated-beam";

// jsdom has no layout, so the cards are fixed rects in page coordinates: the desktop topology's
// three columns, 48 px gutters, five VM cards stacked 12 px apart next to a centred host card.
const container: BeamRect = { left: 100, top: 50, width: 860, height: 640 };
const host: BeamRect = { left: 116, top: 310, width: 240, height: 127 };
const vms: BeamRect[] = [0, 1, 2, 3, 4].map((i) => ({
  left: 404,
  top: 50 + i * 130,
  width: 240,
  height: 118,
}));
const groups: BeamRect[] = [
  { left: 692, top: 125, width: 240, height: 176 },
  { left: 692, top: 313, width: 240, height: 152 },
  { left: 692, top: 477, width: 240, height: 72 },
];
const cards = [host, ...vms, ...groups];

const right = (r: BeamRect) => r.left + r.width - container.left;
const left = (r: BeamRect) => r.left - container.left;
const top = (r: BeamRect) => r.top - container.top;
const bottom = (r: BeamRect) => r.top + r.height - container.top;

type Point = [number, number];

/** Samples an SVG path of one M and one Q or C segment (the old and the new beam shapes). */
function sample(d: string, steps = 200): Point[] {
  const nums = (d.match(/-?\d+(\.\d+)?/g) ?? []).map(Number);
  const pts: Point[] = [];
  for (let i = 0; i < nums.length; i += 2) pts.push([nums[i], nums[i + 1]]);
  const at = (t: number): Point => {
    if (pts.length === 3) {
      const [p0, p1, p2] = pts;
      const f = (k: 0 | 1) => (1 - t) ** 2 * p0[k] + 2 * (1 - t) * t * p1[k] + t ** 2 * p2[k];
      return [f(0), f(1)];
    }
    const [p0, p1, p2, p3] = pts;
    const f = (k: 0 | 1) =>
      (1 - t) ** 3 * p0[k] + 3 * (1 - t) ** 2 * t * p1[k] + 3 * (1 - t) * t ** 2 * p2[k] + t ** 3 * p3[k];
    return [f(0), f(1)];
  };
  return Array.from({ length: steps + 1 }, (_, i) => at(i / steps));
}

const hostBeams = vms.map((vm, i) => beamPath(host, vm, container, { fromSlot: { index: i, count: vms.length } }));

describe("beamPath", () => {
  it("leaves the right edge of the from card and enters the left edge of the to card", () => {
    const spread = 6;
    hostBeams.forEach(({ d }, i) => {
      const points = sample(d);
      const [sx, sy] = points[0];
      const [ex, ey] = points[points.length - 1];
      expect(sx).toBe(right(host));
      expect(Math.abs(sy - (top(host) + host.height / 2))).toBeLessThanOrEqual(spread * 2);
      expect(ex).toBe(left(vms[i]));
      expect(ey).toBe(top(vms[i]) + vms[i].height / 2); // one beam per VM: its edge centre
    });
  });

  it("stays in the gutter and never crosses a card, also to the farthest card of a column", () => {
    const beams = [
      ...hostBeams,
      ...groups.map((g, gi) =>
        beamPath(vms[0], g, container, {
          fromSlot: { index: gi, count: groups.length },
          toSlot: { index: 0, count: 5 },
        }),
      ),
      beamPath(vms[4], groups[0], container, { toSlot: { index: 4, count: 5 } }),
    ];
    for (const { d } of beams) {
      const points = sample(d);
      const [x0] = points[0];
      const [x1] = points[points.length - 1];
      for (const [x, y] of points) {
        expect(x).toBeGreaterThanOrEqual(x0);
        expect(x).toBeLessThanOrEqual(x1);
        expect(x1 - x0).toBeLessThanOrEqual(48); // a gutter, not a column
        const inside = cards.find((c) => x > left(c) && x < right(c) && y > top(c) && y < bottom(c));
        expect(inside).toBeUndefined();
      }
    }
  });

  it("spreads the endpoints of a fan apart, in order and inside the card", () => {
    const fans: [BeamRect, number][] = [
      [host, 5],
      [groups[2], 12], // a short card with many beams: closer together, still on the card
    ];
    for (const [card, count] of fans) {
      const ys = Array.from({ length: count }, (_, index) => {
        const out = sample(beamPath(card, groups[0], container, { fromSlot: { index, count } }).d);
        const into = sample(beamPath(vms[0], card, container, { toSlot: { index, count } }).d);
        expect(into[into.length - 1][1]).toBe(out[0][1]); // fan-in mirrors fan-out
        return out[0][1];
      });
      expect(new Set(ys).size).toBe(count);
      expect([...ys].sort((a, b) => a - b)).toEqual(ys); // slot order is top-to-bottom order
      for (const y of ys) {
        expect(y).toBeGreaterThan(top(card));
        expect(y).toBeLessThan(bottom(card));
      }
    }
  });
});
