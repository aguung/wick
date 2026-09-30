<script lang="ts">
  // Live model list for an omp/opencode instance: the models its CLI lists
  // (`omp models` / `opencode models`), narrowed by a filter in the shared
  // grammar, with a pinned default. The server caches the list ~10 min;
  // Refresh re-runs the CLI. The preview filters the UNSAVED filter
  // client-side with the same grammar the server applies on save.
  import { onMount } from "svelte";
  import { Select, Button, matchModelFilter, MODEL_FILTER_HELP } from "@wick-fe/common-ui";
  import { apiGetCLIModels, isOpencodeHostedModel, type CLIModel } from "$lib/api.js";

  interface Props {
    /* Reports how many models the CLI listed (the section summary). */
    onCount?: (n: number) => void;
    base: string;
    type: string;
    name: string;
    filter: string;
    pin: string;
    onSaveFilter: (v: string) => void;
    onSavePin: (v: string) => void;
  }
  let { base, type, name, filter, pin, onSaveFilter, onSavePin, onCount }: Props = $props();

  // How many preview rows render before "show more" — the list can be 100+.
  const PREVIEW_LIMIT = 50;

  let models = $state<CLIModel[]>([]);
  let hostedAllowed = $state(true);
  let fetchedAt = $state("");
  let loading = $state(false);
  let err = $state("");
  let draft = $state("");
  let search = $state("");
  let showAll = $state(false);

  $effect(() => { draft = filter; });

  async function fetchModels(refresh: boolean) {
    loading = true;
    err = "";
    try {
      const r = await apiGetCLIModels(base, type, name, refresh);
      models = r.models;
      onCount?.(models.length);
      hostedAllowed = r.hostedAllowed;
      fetchedAt = r.fetchedAt;
      err = r.error ?? "";
    } catch (e) {
      err = e instanceof Error ? e.message : String(e);
    } finally {
      loading = false;
    }
  }
  onMount(() => { void fetchModels(false); });

  const hay = (m: CLIModel) => `${m.id} ${m.desc ?? ""}`;
  // Hosted opencode models only count when the server says they may run.
  const eligible = $derived(models.filter((m) => type !== "opencode" || hostedAllowed || !isOpencodeHostedModel(m.id)));
  const hiddenHosted = $derived(models.length - eligible.length);
  const matched = $derived(eligible.filter((m) => matchModelFilter(hay(m), draft)));
  const effectiveDefault = $derived(matched.some((m) => m.id === pin) ? pin : (matched[0]?.id ?? ""));
  const searched = $derived(search.trim() ? matched.filter((m) => matchModelFilter(hay(m), search)) : matched);
  const shown = $derived(showAll ? searched : searched.slice(0, PREVIEW_LIMIT));
  const dirty = $derived(draft.trim() !== filter.trim());
  const pinOptions = $derived([
    { label: "First match", value: "", description: matched[0]?.id ?? "no model matches" },
    ...matched.map((m) => ({ label: m.id, value: m.id, description: m.desc })),
  ]);

  function saveFilter() { onSaveFilter(draft.trim()); }
</script>

<div class="space-y-3" data-testid="live-models-panel">
  <div>
    <label for="live-model-filter" class="text-xs font-medium text-black-900 dark:text-white-100">Filter</label>
    <div class="mt-1 flex items-center gap-2">
      <input
        id="live-model-filter"
        type="text"
        data-testid="live-models-filter"
        bind:value={draft}
        onkeydown={(e) => { if (e.key === "Enter") saveFilter(); }}
        placeholder="e.g. claude|gpt !mini — empty = all"
        class="w-full rounded-lg border border-white-400 dark:border-navy-600 bg-white-100 dark:bg-navy-800 px-3 py-1.5 text-sm font-mono text-black-900 dark:text-white-100 placeholder:text-black-700 outline-none focus:border-green-500 focus:ring-2 focus:ring-green-200 dark:focus:ring-green-800"
      />
      <Button size="sm" disabled={!dirty} onclick={saveFilter} testid="live-models-save-filter">Save</Button>
    </div>
    <p class="mt-1 text-[11px] text-black-700 dark:text-black-600">{MODEL_FILTER_HELP}</p>
  </div>

  <div class="flex items-center justify-between gap-2 flex-wrap">
    <span class="text-xs text-black-800 dark:text-black-600" data-testid="live-models-count">
      {#if loading && models.length === 0}
        Loading models from the CLI…
      {:else}
        <strong class="text-black-900 dark:text-white-100">{matched.length}</strong> of {eligible.length} models match
        {#if hiddenHosted > 0}<span> · {hiddenHosted} hosted opencode models hidden (enable opencode_allow_hosted or log in to opencode Zen)</span>{/if}
      {/if}
    </span>
    <span class="flex items-center gap-2">
      {#if fetchedAt}<span class="text-[11px] text-black-700 dark:text-black-600">fetched {new Date(fetchedAt).toLocaleTimeString()}</span>{/if}
      <Button size="sm" variant="secondary" disabled={loading} onclick={() => fetchModels(true)} testid="live-models-refresh">{loading ? "Refreshing…" : "Refresh"}</Button>
    </span>
  </div>
  {#if err}
    <p class="text-[11px] text-amber-600 dark:text-amber-400" data-testid="live-models-error">{err}</p>
  {/if}

  <div>
    <span class="text-xs font-medium text-black-900 dark:text-white-100">Default model</span>
    <div class="mt-1">
      <Select value={matched.some((m) => m.id === pin) ? pin : ""} options={pinOptions} onChange={(v) => onSavePin(v)} size="sm" searchable ariaLabel="Default model" />
    </div>
    {#if pin && !matched.some((m) => m.id === pin) && models.length > 0}
      <p class="mt-1 text-[11px] text-amber-600 dark:text-amber-400">Pinned {pin} is no longer in the filtered list — using {effectiveDefault || "nothing"}.</p>
    {/if}
  </div>

  {#if matched.length > 0}
    <div class="rounded-lg border border-white-300 dark:border-navy-600">
      <input
        type="text"
        bind:value={search}
        placeholder="Search the matched models…"
        aria-label="Search matched models"
        class="w-full border-b border-white-300 dark:border-navy-600 bg-transparent px-3 py-1.5 text-xs text-black-900 dark:text-white-100 outline-none"
      />
      <ul class="max-h-56 overflow-y-auto divide-y divide-white-300 dark:divide-navy-600" data-testid="live-models-list">
        {#each shown as m (m.id)}
          <li class="flex items-center justify-between gap-2 px-3 py-1 text-xs">
            <span class="font-mono text-black-900 dark:text-white-100 truncate">{m.id}</span>
            {#if m.id === effectiveDefault}<span class="shrink-0 rounded bg-green-100 dark:bg-green-900 px-1.5 text-[10px] font-medium text-green-700 dark:text-green-300">default</span>{/if}
          </li>
        {/each}
      </ul>
      {#if searched.length > shown.length}
        <button type="button" class="w-full px-3 py-1 text-[11px] text-green-600 dark:text-green-400 hover:underline" onclick={() => (showAll = true)}>Show all {searched.length}</button>
      {/if}
    </div>
  {/if}
</div>
