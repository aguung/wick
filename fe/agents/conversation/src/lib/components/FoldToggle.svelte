<script lang="ts">
  /* The "Show more / Show less" at the BOTTOM of a folded section. At the
     bottom because that is where the reader runs out of text — a toggle in
     the header reads as a setting, and nobody guesses there is more below. */
  type Props = {
    open: boolean;
    /* How many items the fold hides, when it hides a countable list. */
    hidden?: number;
    testid?: string;
    onclick: () => void;
  };
  let { open, hidden, testid, onclick }: Props = $props();
</script>

<button
  type="button"
  data-testid={testid}
  aria-expanded={open}
  {onclick}
  class="mt-1.5 flex w-full items-center justify-center gap-1 rounded-md py-1 text-[11px] font-medium text-green-600 transition-colors hover:bg-white-200 dark:text-green-400 dark:hover:bg-navy-600"
>
  {open ? "Show less" : hidden ? `Show more (${hidden})` : "Show more"}
  <svg viewBox="0 0 16 16" class="h-3 w-3 transition-transform {open ? 'rotate-180' : ''}" fill="none" stroke="currentColor" stroke-width="1.8" aria-hidden="true">
    <path d="M4 6l4 4 4-4" stroke-linecap="round" stroke-linejoin="round"></path>
  </svg>
</button>
