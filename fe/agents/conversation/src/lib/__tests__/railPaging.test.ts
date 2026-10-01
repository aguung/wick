import { describe, test, expect } from "vitest";
import { mergeRail, keepPushedOff } from "../railPaging.js";
import { sessionRowBadge } from "../lifecycleCls.js";
import type { TicketSessionRow } from "../types/agents.js";

const row = (id: string): TicketSessionRow => ({ id, label: id, lifecycle: "" });
const ids = (rs: TicketSessionRow[]) => rs.map((r) => r.id);
const none = new Set<string>();

describe("mergeRail", () => {
  test("a chat held on a kept page AND the fresh first page is drawn once", () => {
    // "c" was on page two, then got used and jumped into page one.
    const merged = mergeRail([row("c"), row("a")], [row("b"), row("c"), row("d")], none);
    expect(ids(merged)).toEqual(["c", "a", "b", "d"]);
    // The next page starts at 4 distinct rows, not 2 + 3 = 5 (which would skip one).
    expect(merged.length).toBe(4);
  });

  test("chats this page put on a ticket leave the kept pages", () => {
    expect(ids(mergeRail([row("a")], [row("b"), row("c")], new Set(["b"])))).toEqual(["a", "c"]);
  });

  test("with nothing kept it is the first page as-is", () => {
    const first = [row("a")];
    expect(mergeRail(first, [], none)).toBe(first);
  });
});

describe("keepPushedOff", () => {
  test("a chat pushed off page one by newer activity is kept at the head of the kept pages", () => {
    // page one was [a, b]; "x" (never held) became active: now [x, a]. "b" is
    // now at position 2 — past page one, before the kept page [c, d].
    const more = keepPushedOff([row("a"), row("b")], [row("x"), row("a")], [row("c"), row("d")], none);
    expect(ids(more)).toEqual(["b", "c", "d"]);
    // …so the drawn rail has every row from the top, and the offset (5) is right.
    expect(ids(mergeRail([row("x"), row("a")], more, none))).toEqual(["x", "a", "b", "c", "d"]);
  });

  test("a chat this page just attached is not brought back", () => {
    const more = keepPushedOff([row("a"), row("b")], [row("a")], [row("c")], new Set(["b"]));
    expect(ids(more)).toEqual(["c"]);
  });

  test("nothing kept → nothing to do (plain first-page poll)", () => {
    const more: TicketSessionRow[] = [];
    expect(keepPushedOff([row("a"), row("b")], [row("x")], more, none)).toBe(more);
  });

  test("a row already on a kept page is not duplicated", () => {
    const more = keepPushedOff([row("a"), row("b")], [row("x"), row("a")], [row("b"), row("c")], none);
    expect(ids(more)).toEqual(["b", "c"]);
  });
});

describe("sessionRowBadge", () => {
  test("running states say why the row is pinned to the top", () => {
    expect(sessionRowBadge("working")).toBe("live");
    expect(sessionRowBadge("spawning")).toBe("live");
    expect(sessionRowBadge("subagent")).toBe("sub-agent");
    expect(sessionRowBadge("queued")).toBe("queued");
    expect(sessionRowBadge("idle")).toBe("");
    expect(sessionRowBadge("")).toBe("");
  });
});
