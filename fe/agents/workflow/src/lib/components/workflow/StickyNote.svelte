<script lang="ts">
  // n8n-style sticky note — a `sticky_note` node in graph.nodes. Rendered
  // by Canvas in a layer BEHIND the edge SVG and the node cards so a note
  // can frame a group of steps. Owns its own drag + corner resize (pointer
  // capture, deltas divided by the canvas zoom) and an inline markdown
  // editor on double-click. Position lives in node._canvas like any node.
  import { tick } from "svelte";
  import { renderMarkdown } from "@wick-fe/common-md";
  import type { Node, StickyNoteColor } from "$lib/types/workflow";
  import { STICKY_NOTE_W, STICKY_NOTE_H } from "$lib/stores/editor";

  const NOTE_COLORS: StickyNoteColor[] = ["yellow", "green", "blue", "purple", "red", "gray"];
  const MIN_W = 120;
  const MIN_H = 60;

  let {
    note,
    zoom = 1,
    selected = false,
    locked = false,
    onselect,
    onpatch,
    ondelete,
  }: {
    note: Node;
    zoom?: number;
    selected?: boolean;
    locked?: boolean;
    onselect?: () => void;
    onpatch?: (patch: Partial<Node>) => void;
    ondelete?: () => void;
  } = $props();

  let editing = $state(false);
  let draft = $state("");
  let textareaEl: HTMLTextAreaElement | undefined = $state();

  const x = $derived(note._canvas?.x ?? 0);
  const y = $derived(note._canvas?.y ?? 0);
  const width = $derived(note.width || STICKY_NOTE_W);
  const height = $derived(note.height || STICKY_NOTE_H);
  const color = $derived(note.color || "yellow");
  const html = $derived(renderMarkdown(note.content ?? ""));

  // One gesture at a time: "move" drags the note, "resize" drags the
  // bottom-right corner. Start values snapshot the note so the delta is
  // applied to a fixed origin (no drift from rounding per move).
  let gesture: {
    kind: "move" | "resize";
    startX: number;
    startY: number;
    x: number;
    y: number;
    w: number;
    h: number;
  } | null = null;

  function begin(e: PointerEvent, kind: "move" | "resize") {
    if (e.button !== 0) return;
    e.stopPropagation();
    onselect?.();
    if (locked || editing) return;
    e.preventDefault();
    (e.currentTarget as HTMLElement).setPointerCapture(e.pointerId);
    gesture = { kind, startX: e.clientX, startY: e.clientY, x, y, w: width, h: height };
  }

  function move(e: PointerEvent) {
    if (!gesture) return;
    const dx = (e.clientX - gesture.startX) / zoom;
    const dy = (e.clientY - gesture.startY) / zoom;
    if (gesture.kind === "move") {
      onpatch?.({ _canvas: { x: Math.round(gesture.x + dx), y: Math.round(gesture.y + dy) } });
    } else {
      onpatch?.({
        width: Math.max(MIN_W, Math.round(gesture.w + dx)),
        height: Math.max(MIN_H, Math.round(gesture.h + dy)),
      });
    }
  }

  function end(e: PointerEvent) {
    if (!gesture) return;
    gesture = null;
    const el = e.currentTarget as HTMLElement;
    if (el.hasPointerCapture(e.pointerId)) el.releasePointerCapture(e.pointerId);
  }

  async function startEdit() {
    if (locked) return;
    draft = note.content ?? "";
    editing = true;
    await tick();
    textareaEl?.focus();
  }

  function commitEdit() {
    if (!editing) return;
    editing = false;
    if (draft !== (note.content ?? "")) onpatch?.({ content: draft });
  }
</script>

<div
  class="sticky-note sticky-{color} absolute rounded-md border shadow-sm flex flex-col"
  class:sticky-selected={selected}
  style="left: {x}px; top: {y}px; width: {width}px; height: {height}px;"
  data-note-id={note.id}
  role="presentation"
  onpointerdown={(e) => begin(e, "move")}
  onpointermove={move}
  onpointerup={end}
  onpointercancel={end}
  ondblclick={(e) => { e.stopPropagation(); startEdit(); }}
>
  {#if selected && !locked && !editing}
    <!-- Selection toolbar: colour presets + delete. Sits just above the
         note; stopPropagation keeps a swatch click from starting a drag. -->
    <div
      class="absolute -top-9 left-0 flex items-center gap-1 rounded-md px-1.5 py-1 shadow bg-white-100 dark:bg-navy-700 border border-white-300 dark:border-navy-600"
      role="toolbar"
      aria-label="Sticky note"
      tabindex="-1"
      onpointerdown={(e) => e.stopPropagation()}
      ondblclick={(e) => e.stopPropagation()}
    >
      {#each NOTE_COLORS as c (c)}
        <button
          type="button"
          class="sticky-swatch sticky-{c} h-5 w-5 rounded-full border"
          class:sticky-swatch-active={c === color}
          title={c}
          aria-label="Color {c}"
          aria-pressed={c === color}
          onclick={() => onpatch?.({ color: c })}
        ></button>
      {/each}
      <span class="mx-0.5 h-4 w-px bg-white-300 dark:bg-navy-600"></span>
      <button
        type="button"
        class="h-6 w-6 rounded flex items-center justify-center text-black-700 dark:text-white-100 hover:bg-white-200 dark:hover:bg-navy-600 hover:text-rose-500"
        title="Delete note (Del)"
        aria-label="Delete note"
        onclick={() => ondelete?.()}
      >
        <svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round"><path d="M3 6h18M8 6V4h8v2M19 6l-1 14H6L5 6"/></svg>
      </button>
    </div>
  {/if}

  {#if editing}
    <textarea
      bind:this={textareaEl}
      bind:value={draft}
      class="sticky-text flex-1 w-full resize-none bg-transparent px-3 py-2 text-sm font-mono outline-none"
      placeholder="Markdown: # heading, **bold**, *italic*, - list, `code`, [link](https://…)"
      onpointerdown={(e) => e.stopPropagation()}
      onblur={commitEdit}
      onkeydown={(e) => {
        e.stopPropagation();
        if (e.key === "Escape" || ((e.ctrlKey || e.metaKey) && e.key === "Enter")) {
          e.preventDefault();
          textareaEl?.blur();
        }
      }}
    ></textarea>
  {:else}
    <div class="sticky-md sticky-text flex-1 overflow-hidden px-3 py-2 text-sm select-none" class:cursor-move={!locked}>
      {#if (note.content ?? "").trim()}
        {@html html}
      {:else}
        <p class="opacity-60 italic">Double-click to edit</p>
      {/if}
    </div>
  {/if}

  {#if !locked && !editing}
    <div
      class="sticky-resize absolute bottom-0 right-0 h-4 w-4 cursor-nwse-resize"
      role="presentation"
      title="Resize"
      onpointerdown={(e) => begin(e, "resize")}
      onpointermove={move}
      onpointerup={end}
      onpointercancel={end}
    ></div>
  {/if}
</div>

<style>
  /* Colour presets. Light mode: pastel paper + dark ink. Dark mode: a
     translucent tint of the same hue over the canvas + light ink, so a
     note never turns into a bright block with unreadable text. Plain
     CSS instead of Tailwind tokens because most of these hues are not in
     the shared palette (tailwind.config.js replaces the defaults). */
  .sticky-yellow { --sn-bg: rgb(254 249 195 / 0.92); --sn-bd: #facc15; --sn-fg: #422006; --sn-dbg: rgb(234 179 8 / 0.16); --sn-dbd: rgb(234 179 8 / 0.55); --sn-dfg: #fef9c3; }
  .sticky-green  { --sn-bg: rgb(220 252 231 / 0.92); --sn-bd: #4ade80; --sn-fg: #14532d; --sn-dbg: rgb(34 197 94 / 0.15);  --sn-dbd: rgb(34 197 94 / 0.5);  --sn-dfg: #dcfce7; }
  .sticky-blue   { --sn-bg: rgb(219 234 254 / 0.92); --sn-bd: #60a5fa; --sn-fg: #1e3a8a; --sn-dbg: rgb(59 130 246 / 0.17); --sn-dbd: rgb(59 130 246 / 0.55); --sn-dfg: #dbeafe; }
  .sticky-purple { --sn-bg: rgb(237 233 254 / 0.92); --sn-bd: #a78bfa; --sn-fg: #3b0764; --sn-dbg: rgb(139 92 246 / 0.19); --sn-dbd: rgb(139 92 246 / 0.55); --sn-dfg: #ede9fe; }
  .sticky-red    { --sn-bg: rgb(254 226 226 / 0.92); --sn-bd: #f87171; --sn-fg: #7f1d1d; --sn-dbg: rgb(239 68 68 / 0.16);  --sn-dbd: rgb(239 68 68 / 0.5);  --sn-dfg: #fee2e2; }
  .sticky-gray   { --sn-bg: rgb(241 245 249 / 0.92); --sn-bd: #94a3b8; --sn-fg: #0f172a; --sn-dbg: rgb(148 163 184 / 0.15); --sn-dbd: rgb(148 163 184 / 0.45); --sn-dfg: #e2e8f0; }

  .sticky-note { background: var(--sn-bg); border-color: var(--sn-bd); color: var(--sn-fg); }
  :global(.dark) .sticky-note { background: var(--sn-dbg); border-color: var(--sn-dbd); color: var(--sn-dfg); }
  .sticky-selected { box-shadow: 0 0 0 2px #10b981; }

  .sticky-swatch { background: var(--sn-bg); border-color: var(--sn-bd); }
  :global(.dark) .sticky-swatch { background: var(--sn-dbd); border-color: var(--sn-dbd); }
  .sticky-swatch-active { box-shadow: 0 0 0 2px #10b981; }

  .sticky-text { color: inherit; }
  .sticky-text::placeholder { color: inherit; opacity: 0.5; }

  /* renderMarkdown hard-codes chat colours on p/li/code; inherit the
     note ink instead so every preset stays readable in both themes. */
  .sticky-md :global(*) { color: inherit; }
  .sticky-md :global(p),
  .sticky-md :global(li) { font-size: 0.8125rem; line-height: 1.4; margin: 0.15rem 0; }
  .sticky-md :global(h1) { font-size: 1.15rem; font-weight: 700; margin: 0.1rem 0 0.3rem; }
  .sticky-md :global(h2) { font-size: 1rem; font-weight: 700; margin: 0.1rem 0 0.25rem; }
  .sticky-md :global(h3),
  .sticky-md :global(h4) { font-size: 0.875rem; font-weight: 600; margin: 0.1rem 0 0.2rem; }
  .sticky-md :global(ul) { list-style: disc; padding-left: 1.1rem; }
  .sticky-md :global(ol) { list-style: decimal; padding-left: 1.1rem; }
  .sticky-md :global(code) { background: rgb(0 0 0 / 0.08); }
  :global(.dark) .sticky-md :global(code) { background: rgb(255 255 255 / 0.12); }
  .sticky-md :global(a) { text-decoration: underline; }

  .sticky-resize {
    background: linear-gradient(135deg, transparent 50%, var(--sn-bd) 50%);
    border-bottom-right-radius: 0.375rem;
    opacity: 0.7;
  }
</style>
