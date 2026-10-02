import { describe, test, expect } from "vitest";
import { spacerHeight, anchorTop, latestTop, jumpVisible, JUMP_THRESHOLD } from "../threadScroll.js";

describe("spacerHeight", () => {
  test("pads a short latest turn up to one viewport", () => {
    // viewport 800, gap 24, latest turn = 3000..3200 (200px tall)
    expect(spacerHeight(800, 3200, 3000, 24)).toBe(576);
  });
  test("is gone once the latest turn outgrows the viewport", () => {
    expect(spacerHeight(800, 4000, 3000, 24)).toBe(0);
  });
  test("no user turn → no blank space", () => {
    expect(spacerHeight(800, 3200, null, 24)).toBe(0);
  });
  test("content + spacer stays constant while the reply grows (no clamp, no jump)", () => {
    const total = (bottom: number) => bottom + spacerHeight(800, bottom, 3000, 24);
    expect(total(3200)).toBe(total(3500));
    // and a reply that shrinks back (an iframe collapsing) is absorbed too
    expect(total(3600)).toBe(total(3300));
  });
  test("the anchored position is always reachable", () => {
    const bottom = 3100, gap = 24, client = 800;
    const scrollMax = bottom + spacerHeight(client, bottom, 3000, gap) - client;
    expect(scrollMax).toBeGreaterThanOrEqual(anchorTop(3000, gap));
  });
});

describe("anchorTop / latestTop", () => {
  test("anchors the bubble a gap below the top edge", () => {
    expect(anchorTop(3000, 24)).toBe(2976);
    expect(anchorTop(10, 24)).toBe(0);
  });
  test("latest = end of real content at the viewport bottom", () => {
    expect(latestTop(5000, 800)).toBe(4200);
    expect(latestTop(500, 800)).toBe(0);
  });
});

describe("jumpVisible", () => {
  test("hidden right after sending: the anchored bubble and empty spacer are all that is below", () => {
    // bubble anchored at 2976, real content ends at 3200, viewport 800
    expect(jumpVisible(3200, 2976, 800)).toBe(false);
  });
  test("appears only once the reply grows past the fold by more than the threshold", () => {
    expect(jumpVisible(2976 + 800 + JUMP_THRESHOLD, 2976, 800)).toBe(false);
    expect(jumpVisible(2976 + 800 + JUMP_THRESHOLD + 1, 2976, 800)).toBe(true);
  });
  test("the spacer never counts: at the end of real content it is hidden however tall the spacer is", () => {
    expect(jumpVisible(4200 + 800, 4200, 800)).toBe(false);
  });
  test("an artifact growing on screen shows it; shrinking back hides it", () => {
    expect(jumpVisible(5000 + 2080, 4200, 800)).toBe(true);
    expect(jumpVisible(5000, 4200, 800)).toBe(false);
  });
});
