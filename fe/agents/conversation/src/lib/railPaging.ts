/* Paging for the ticket board's untracked rail.

   The poll re-reads only the FIRST page; older pages are fetched once, on
   scroll, and kept. Two things keep that honest while the order moves under
   it (a chat used right now jumps to the top):

   - the next page starts at the number of DISTINCT rows drawn. A chat that
     moved from a kept page into the fresh first page is held twice but sits
     at one position; counting it twice would skip a row nobody saw.
   - a chat pushed off the first page by newer activity is still in the list,
     one position lower — at the head of the kept pages. The fresh first page
     no longer has it and the next page starts past it, so it is kept rather
     than lost from the rail for the rest of the visit.

   `tracked` is the chats this page just put on a ticket: gone from the rail
   even though a kept page still carries them. */
import type { TicketSessionRow } from "./types/agents.js";

export function mergeRail(
  first: TicketSessionRow[],
  more: TicketSessionRow[],
  tracked: ReadonlySet<string>,
): TicketSessionRow[] {
  if (more.length === 0) return first;
  const seen = new Set(first.map((r) => r.id));
  return [...first, ...more.filter((r) => !seen.has(r.id) && !tracked.has(r.id))];
}

export function keepPushedOff(
  prevFirst: TicketSessionRow[],
  nextFirst: TicketSessionRow[],
  more: TicketSessionRow[],
  tracked: ReadonlySet<string>,
): TicketSessionRow[] {
  if (more.length === 0) return more;
  const now = new Set(nextFirst.map((r) => r.id));
  const held = new Set(more.map((r) => r.id));
  const pushedOff = prevFirst.filter((r) => !now.has(r.id) && !held.has(r.id) && !tracked.has(r.id));
  return pushedOff.length === 0 ? more : [...pushedOff, ...more];
}
