<script lang="ts">
  /* The project's own ticket fields, on the rail beside the description:
     read at a glance, edited in place.

     - Only fields the project DEFINES are shown, in the project's order.
       Values under keys nobody defined (a mirror's bookkeeping, a linked
       page id) have no label to show them under and are left alone.
     - A few rows first, the rest behind Show more — the same fold as the
       description, for the same reason: the notes below must stay in view.
     - A URL in a value is a link, with an explicit open-in-new-tab button,
       because a Slack thread or an upstream ticket is usually the next place
       to look.
     - An empty field is a dashed slot, like "Write a note": the place to
       fill it in is where you notice it is missing. */
  import type { TicketField } from "../types/agents.js";
  import { linkify, firstUrl } from "../linkify.js";
  import { toastError } from "@wick-fe/common-stores";

  type Props = {
    fields: TicketField[];
    values: Record<string, string> | null | undefined;
    /* Persists one field. Resolves when saved; rejects to keep the editor
       open with what was typed. */
    onSave: (key: string, value: string) => Promise<void>;
    /* Rows shown before the fold. */
    initialVisible?: number;
  };

  let { fields, values, onSave, initialVisible = 4 }: Props = $props();

  /* Values saved from here, shown until the parent's reload brings them
     back — so a field does not flash its old value in between. */
  let saved = $state<Record<string, string>>({});
  const valueOf = (key: string) => (key in saved ? saved[key] : (values?.[key] ?? ""));

  let showAll = $state(false);
  const hiddenCount = $derived(Math.max(0, fields.length - initialVisible));
  const visible = $derived(showAll ? fields : fields.slice(0, initialVisible));

  let editing = $state<string | null>(null);
  let draft = $state("");
  let saving = $state(false);

  function start(f: TicketField) {
    editing = f.key;
    draft = valueOf(f.key);
  }

  function cancel() {
    editing = null;
    draft = "";
  }

  async function commit(f: TicketField, value = draft) {
    // Escape cancels and removes the input, which then fires blur — without
    // this guard that blur would "save" the cleared draft and erase the field.
    if (saving || editing !== f.key) return;
    const next = value.trim();
    if (next === valueOf(f.key).trim()) {
      cancel();
      return;
    }
    if (f.required && next === "") {
      toastError(`${f.label || f.key} is required`);
      return;
    }
    saving = true;
    try {
      await onSave(f.key, next);
      saved = { ...saved, [f.key]: next };
      cancel();
    } catch (e) {
      toastError(e instanceof Error ? e.message : `Failed to save ${f.label || f.key}`);
    } finally {
      saving = false;
    }
  }

  function focusOnMount(el: HTMLElement) {
    el.focus();
  }
</script>

<ul class="flex flex-col gap-2" data-testid="ticket-fields-list">
  {#each visible as f (f.key)}
    {@const value = valueOf(f.key)}
    {@const url = firstUrl(value)}
    <li data-testid="ticket-field-{f.key}">
      <div class="flex items-center gap-1.5">
        <span class="text-[10px] font-medium uppercase tracking-wide text-black-700 dark:text-black-600">
          {f.label || f.key}{#if f.required}<span class="text-neg-400" aria-label="required"> *</span>{/if}
        </span>
        {#if value && editing !== f.key}
          <span class="ml-auto flex items-center gap-1">
            {#if url}
              <a
                href={url}
                target="_blank"
                rel="noopener noreferrer"
                data-testid="ticket-field-open-{f.key}"
                title="Open in new tab"
                aria-label="Open {f.label || f.key} in a new tab"
                class="rounded p-0.5 text-black-700 transition-colors hover:bg-white-200 hover:text-green-600 dark:text-black-600 dark:hover:bg-navy-600 dark:hover:text-green-400"
              >
                <svg viewBox="0 0 16 16" class="h-3.5 w-3.5" fill="none" stroke="currentColor" stroke-width="1.6" aria-hidden="true">
                  <path d="M9 3h4v4M13 3 7.5 8.5M11 9.5V12a1 1 0 0 1-1 1H4a1 1 0 0 1-1-1V6a1 1 0 0 1 1-1h2.5" stroke-linecap="round" stroke-linejoin="round"></path>
                </svg>
              </a>
            {/if}
            <button
              type="button"
              data-testid="ticket-field-edit-{f.key}"
              onclick={() => start(f)}
              title="Edit {f.label || f.key}"
              aria-label="Edit {f.label || f.key}"
              class="rounded p-0.5 text-black-700 transition-colors hover:bg-white-200 hover:text-green-600 dark:text-black-600 dark:hover:bg-navy-600 dark:hover:text-green-400"
            >
              <svg viewBox="0 0 16 16" class="h-3.5 w-3.5" fill="none" stroke="currentColor" stroke-width="1.6" aria-hidden="true">
                <path d="M10.5 3.5l2 2L6 12H4v-2l6.5-6.5z" stroke-linejoin="round"></path>
              </svg>
            </button>
          </span>
        {/if}
      </div>

      {#if editing === f.key}
        {#if f.type === "select"}
          <select
            use:focusOnMount
            value={draft}
            disabled={saving}
            data-testid="ticket-field-input-{f.key}"
            aria-label={f.label || f.key}
            onchange={(e) => commit(f, (e.target as HTMLSelectElement).value)}
            onkeydown={(e) => { if (e.key === "Escape") cancel(); }}
            onblur={cancel}
            class="mt-1 w-full rounded-lg border border-white-400 bg-white-100 px-2 py-1.5 text-xs text-black-900 outline-none focus:border-green-500 dark:border-navy-600 dark:bg-navy-700 dark:text-white-100"
          >
            <option value="">—</option>
            {#each f.options ?? [] as o (o)}
              <option value={o}>{o}</option>
            {/each}
          </select>
        {:else}
          <input
            use:focusOnMount
            bind:value={draft}
            disabled={saving}
            data-testid="ticket-field-input-{f.key}"
            aria-label={f.label || f.key}
            onkeydown={(e) => {
              if (e.key === "Enter") { e.preventDefault(); commit(f); }
              else if (e.key === "Escape") cancel();
            }}
            onblur={() => commit(f)}
            class="mt-1 w-full rounded-lg border border-white-400 bg-white-100 px-2 py-1 text-xs text-black-900 outline-none focus:border-green-500 dark:border-navy-600 dark:bg-navy-700 dark:text-white-100"
          />
        {/if}
      {:else if value}
        <p class="mt-0.5 whitespace-pre-wrap break-words text-xs text-black-900 dark:text-white-100">
          {#each linkify(value) as seg, i (i)}
            {#if seg.kind === "url"}
              <a
                href={seg.url}
                target="_blank"
                rel="noopener noreferrer"
                class="break-all text-green-600 underline decoration-green-600/40 underline-offset-2 hover:decoration-green-600 dark:text-green-400"
              >{seg.url}</a>
            {:else}{seg.text}{/if}
          {/each}
        </p>
      {:else}
        <button
          type="button"
          data-testid="ticket-field-add-{f.key}"
          onclick={() => start(f)}
          class="mt-1 flex w-full items-center justify-center gap-1.5 rounded-lg border border-dashed border-white-400 px-3 py-1.5 text-xs font-medium text-black-700 transition-colors hover:border-green-500 hover:text-green-600 dark:border-navy-600 dark:text-black-600 dark:hover:text-green-400"
        >
          <svg viewBox="0 0 16 16" class="h-3.5 w-3.5" fill="none" stroke="currentColor" stroke-width="1.6" aria-hidden="true">
            <path d="M8 3.5v9M3.5 8h9" stroke-linecap="round"></path>
          </svg>
          Add {f.label || f.key}
        </button>
      {/if}
    </li>
  {/each}
</ul>

{#if hiddenCount > 0}
  <button
    type="button"
    data-testid="ticket-fields-toggle"
    class="mt-2 text-[11px] font-medium text-green-600 hover:underline dark:text-green-400"
    onclick={() => { showAll = !showAll; }}
  >{showAll ? "Show less" : `Show more (${hiddenCount})`}</button>
{/if}
