/* Scroll geometry for the conversation thread (DetailView.svelte), kept pure
   so it can be unit-tested without a browser. All positions are in the
   scroller's CONTENT coordinates (0 = top of the scrollable content).

   The thread follows the claude.ai pattern: it never follows growing content
   by itself. When the user sends, their new bubble is anchored near the top
   of the viewport and the reply grows into the empty space below it. That
   space is a spacer after the real content, sized so the latest turn (from
   the last user bubble down) fills at least one viewport — so the anchor is
   reachable even when the reply is still short, and a reply that shrinks is
   absorbed by the spacer instead of clamping scrollTop. Older turns never get
   blank space: the spacer only ever pads after the latest one. */

/** Content below the viewport worth a Jump button, in px. */
export const JUMP_THRESHOLD = 80;

/** Height of the spacer after the real content. `userTop` is the top of the
    last user bubble (null when there is none), `gap` the breathing room kept
    above an anchored bubble. Shrinks to 0 once the latest turn is taller
    than the viewport. */
export function spacerHeight(clientHeight: number, contentBottom: number, userTop: number | null, gap: number): number {
  if (userTop === null) return 0;
  return Math.max(0, Math.ceil(clientHeight - gap - (contentBottom - userTop)));
}

/** scrollTop that anchors the last user bubble just below the top edge. */
export function anchorTop(userTop: number, gap: number): number {
  return Math.max(0, Math.floor(userTop - gap));
}

/** scrollTop that shows the end of the real content (open / Jump to latest). */
export function latestTop(contentBottom: number, clientHeight: number): number {
  return Math.max(0, Math.ceil(contentBottom - clientHeight));
}

/** Whether real content (not the spacer) continues below the viewport by more
    than the threshold — the only time the Jump button is worth showing. */
export function jumpVisible(contentBottom: number, scrollTop: number, clientHeight: number): boolean {
  return contentBottom - (scrollTop + clientHeight) > JUMP_THRESHOLD;
}
