<script lang="ts">
  /* ManagedBinaryPanel — the "Binary" section for one wick-managed type
     (omp, opencode). Summary on top: active version, host, newest release
     and what to do about it (Download vX, then Activate vX). Below, one
     version list = cached GitHub releases + downloaded versions, each row
     with its own status and action: Download (fetch + verify + store only,
     `current` untouched), Activate (instant switch, sha256 re-checked),
     Remove. A first install activates on its own — nothing to switch from.
     Progress is the server job, so it survives a reload; while it runs
     every action for the type is disabled and shows the job's progress.
     Nothing downloads unless an admin clicks; the server re-checks
     everything (sha256, --version, in-use) regardless of what this shows. */
  import { onDestroy, onMount } from "svelte";
  import { Button, ProgressBar } from "@wick-fe/common-ui";
  import { toastError, toastOk, toastWarn } from "@wick-fe/common-stores";
  import {
    apiManagedList,
    apiManagedDownload,
    apiManagedActivate,
    apiManagedRemove,
    apiManagedCheck,
    apiManagedVerify,
    CheckTooSoonError,
    isRunning,
    jobLabel,
    jobPct,
    jobShort,
    jobVersion,
    latestState,
    sessionsNote,
    versionRows,
    type ManagedBinary,
    type ManagedJob,
  } from "$lib/managedbin.js";

  type Props = {
    base: string;
    type: string;
    /* Compact = inside the Add form: status + first download only. */
    compact?: boolean;
    onChange?: (m: ManagedBinary | null) => void;
  };
  let { base, type, compact = false, onChange }: Props = $props();

  let data = $state<ManagedBinary | null>(null);
  let isAdmin = $state(false);
  let loading = $state(true);
  let busy = $state("");
  let verifyOut = $state<{ output: string; error: string } | null>(null);
  let timer: ReturnType<typeof setTimeout> | null = null;

  function errText(e: unknown): string {
    const msg = e instanceof Error ? e.message : String(e);
    try {
      const j = JSON.parse(msg);
      if (j && typeof j.error === "string") return j.error;
    } catch {
      /* not JSON */
    }
    return msg;
  }

  async function load(): Promise<void> {
    try {
      const r = await apiManagedList(base);
      isAdmin = r.isAdmin;
      const prevRunning = isRunning(data?.job ?? null);
      data = r.types.find((t) => t.type === type) ?? null;
      onChange?.(data);
      if (prevRunning && data && !isRunning(data.job) && data.job) {
        if (data.job.phase === "error") toastError(`${type}: ${data.job.error}`);
        else toastOk(`${type}: ${data.job.message || "done"}`);
      }
    } catch {
      data = null;
    } finally {
      loading = false;
    }
    schedule();
  }

  // Poll fast while a job runs, slowly otherwise (session counts change
  // as old processes finish).
  function schedule(): void {
    if (timer) clearTimeout(timer);
    timer = setTimeout(() => void load(), isRunning(data?.job ?? null) ? 1000 : 15000);
  }

  onMount(() => void load());
  onDestroy(() => { if (timer) clearTimeout(timer); });

  async function act(key: string, f: () => Promise<unknown>): Promise<void> {
    busy = key;
    try {
      await f();
    } catch (e) {
      if (e instanceof CheckTooSoonError) toastWarn(e.message);
      else toastError(errText(e));
    } finally {
      busy = "";
      await load();
    }
  }

  // A 409 already_running is not an error: adopt the job in flight so its
  // progress shows right away, then keep polling it.
  const download = (tag = "") =>
    act("dl-" + (tag || "latest"), async () => {
      const r = await apiManagedDownload(base, type, tag);
      if (data && r.job) data.job = r.job as ManagedJob;
    });
  const activate = (v: string) => act("act-" + v, async () => { await apiManagedActivate(base, type, v); toastOk(`${type} v${v} is now active`); });
  const remove = (v: string) => act("rm-" + v, async () => { await apiManagedRemove(base, type, v); toastOk(`Removed ${type} v${v}`); });
  const check = () => act("check", () => apiManagedCheck(base, type));
  const verify = () => act("verify", async () => { verifyOut = await apiManagedVerify(base, type); });

  const job = $derived(data && isRunning(data.job) ? data.job : null);
  const jobV = $derived(job ? jobVersion(job) : "");
  const locked = $derived(!!job || busy !== "");
  const note = $derived(data ? sessionsNote(data) : "");
  const failed = $derived(data?.lastJob?.phase === "error" && !job ? data.lastJob : null);
  const rows = $derived(data ? versionRows(data) : []);
  const latestAct = $derived(data ? latestState(data) : "");
</script>

<div data-testid="managed-binary-panel" data-type={type} class="rounded-xl border border-white-300 dark:border-navy-600 bg-white-100 dark:bg-navy-700 {compact ? 'p-3' : 'p-5'} space-y-3">
  <div class="flex flex-wrap items-center gap-2">
    <h3 class="text-sm font-semibold text-black-900 dark:text-white-100">Binary · {type}</h3>
    <span class="rounded bg-white-300 dark:bg-navy-600 px-1.5 py-0.5 text-[11px] font-medium text-black-800 dark:text-black-600">managed by wick</span>
    {#if data?.hostLabel}
      <span data-testid="managed-host" class="text-[11px] text-black-700 dark:text-black-600">{data.hostLabel}</span>
    {/if}
  </div>

  {#if loading}
    <p class="text-xs text-black-700 dark:text-black-600">Checking…</p>
  {:else if !data}
    <p class="text-xs text-black-700 dark:text-black-600">Managed binaries are not available for {type}.</p>
  {:else}
    {#if !data.enabled}
      <p class="text-xs text-black-700 dark:text-black-600">Disabled in config (providers.managed_binaries.{type}.enabled).</p>
    {/if}
    <div class="flex flex-wrap items-center gap-x-4 gap-y-1 text-xs">
      {#if data.current}
        <span data-testid="managed-current" class="text-black-900 dark:text-white-100">Active <span class="font-mono font-medium">v{data.current}</span></span>
      {:else}
        <span data-testid="managed-not-installed" class="text-neg-400 font-medium">Binary not installed</span>
      {/if}
      {#if data.latest}
        <span class="text-black-800 dark:text-black-600" title={data.latestCheckedAt ? `checked ${new Date(data.latestCheckedAt).toLocaleString()}` : ""}>Latest on GitHub <span class="font-mono">{data.latest}</span></span>
      {/if}
      {#if data.updateAvailable}
        <span data-testid="managed-update-available" class="rounded bg-cau-100 dark:bg-cau-400/20 px-1.5 py-0.5 text-[11px] font-medium text-cau-400">update available {data.latest}</span>
      {/if}
      {#if note}
        <span data-testid="managed-sessions-old" class="text-black-800 dark:text-black-600">{note}</span>
      {/if}
    </div>
    {#if data.currentPath && !compact}
      <p class="font-mono text-[11px] text-black-700 dark:text-black-600 break-all">{data.currentPath}</p>
    {/if}
    {#if data.latestErr && !compact}
      <p data-testid="managed-latest-err" class="text-[11px] text-black-700 dark:text-black-600">Last GitHub check failed: {data.latestErr}</p>
    {/if}

    {#if job}
      <ProgressBar class="max-w-md" testid="managed-job" pct={jobPct(job)} label={jobLabel(job)} />
    {:else if failed}
      <p data-testid="managed-job-error" class="rounded-lg border border-neg-400 px-3 py-2 text-xs text-neg-400">Last download failed ({failed.tag || "latest"}): {failed.error}. The active version was not changed.</p>
    {/if}

    {#if isAdmin && data.enabled}
      <div class="flex flex-wrap items-center gap-2">
        {#if !data.current}
          <Button variant="primary" testid="managed-download-first" disabled={locked} onclick={() => download()}>{job ? jobShort(job) : "Download from GitHub"}</Button>
        {:else if latestAct === "download"}
          <Button variant="primary" testid="managed-download-latest" disabled={locked} onclick={() => download(data!.latest)}>{job ? jobShort(job) : `Download ${data.latest}`}</Button>
        {:else if latestAct === "activate"}
          <Button variant="primary" testid="managed-activate-latest" disabled={locked} onclick={() => activate(data!.latestVersion)}>{job ? jobShort(job) : `Activate ${data.latest}`}</Button>
        {/if}
        {#if !compact}
          <Button variant="secondary" testid="managed-check" disabled={locked} onclick={check}>{busy === "check" ? "Checking…" : "Check for update"}</Button>
          {#if data.current}
            <Button variant="secondary" disabled={locked} onclick={verify}>Re-check --version</Button>
          {/if}
        {/if}
      </div>
    {/if}

    {#if verifyOut}
      <p data-testid="managed-verify" class="font-mono text-[11px] {verifyOut.error ? 'text-neg-400' : 'text-black-800 dark:text-black-600'}">
        wick ran --version → {verifyOut.output || "(no output)"}{verifyOut.error ? ` — ${verifyOut.error}` : ""}
      </p>
    {/if}

    {#if !compact && rows.length > 0}
      <div class="space-y-1">
        <p class="text-[11px] font-semibold tracking-wide text-black-700 dark:text-black-600">VERSIONS</p>
        <ul data-testid="managed-version-list" class="divide-y divide-white-300 dark:divide-navy-600 text-xs">
          {#each rows as r (r.version)}
            {@const rowJob = job && jobV === r.version ? job : null}
            <li data-testid="managed-version-row" data-version={r.version} data-status={r.status} class="py-2 space-y-1.5">
              <div class="flex flex-wrap items-center gap-x-3 gap-y-1">
                <span class="font-mono font-medium text-black-900 dark:text-white-100">v{r.version}</span>
                {#if r.status === "active"}
                  <span class="rounded bg-pos-100 dark:bg-pos-400/20 px-1.5 py-0.5 text-[11px] font-medium text-pos-400">active</span>
                {:else if r.status === "downloaded"}
                  <span class="rounded bg-white-300 dark:bg-navy-600 px-1.5 py-0.5 text-[11px] font-medium text-black-800 dark:text-black-600">downloaded</span>
                {:else}
                  <span class="text-[11px] text-black-700 dark:text-black-600">not downloaded</span>
                {/if}
                {#if r.latest}
                  <span class="rounded bg-white-300 dark:bg-navy-600 px-1.5 py-0.5 text-[11px] font-medium text-black-800 dark:text-black-600">latest</span>
                {/if}
                {#if r.prerelease}
                  <span class="rounded bg-white-300 dark:bg-navy-600 px-1.5 py-0.5 text-[11px] font-medium text-black-800 dark:text-black-600">pre</span>
                {/if}
                {#if r.installed}
                  <span class="text-black-700 dark:text-black-600">{r.installed.installedAt ? new Date(r.installed.installedAt).toLocaleString() : ""}</span>
                  <span class="font-mono text-black-700 dark:text-black-600" title={r.installed.sha256}>sha256 {r.installed.sha256.slice(0, 12)}</span>
                  {#if r.installed.inUse > 0}
                    <span data-testid="managed-row-inuse" class="text-black-800 dark:text-black-600">{r.installed.inUse} {r.installed.inUse === 1 ? "session" : "sessions"} still using it</span>
                  {/if}
                {:else if r.published}
                  <span class="text-black-700 dark:text-black-600">{new Date(r.published).toLocaleDateString()}</span>
                {/if}
                {#if isAdmin && data.enabled}
                  <span class="ml-auto flex gap-2">
                    {#if r.status === "not_downloaded"}
                      <Button variant="secondary" testid="managed-row-download" disabled={locked} onclick={() => download(r.tag)}>{rowJob ? jobShort(rowJob) : "Download"}</Button>
                    {:else}
                      {#if r.status === "downloaded"}
                        <Button variant="secondary" testid="managed-row-activate" disabled={locked} onclick={() => activate(r.version)}>Activate</Button>
                      {/if}
                      <Button
                        variant="secondary"
                        testid="managed-row-remove"
                        disabled={!r.installed?.removable || locked}
                        title={r.status === "active" ? "The active version cannot be removed" : (r.installed?.inUse ?? 0) > 0 ? "Still used by a running session" : ""}
                        onclick={() => remove(r.version)}
                      >Remove</Button>
                    {/if}
                  </span>
                {/if}
              </div>
              {#if rowJob}
                <ProgressBar class="max-w-md" testid="managed-row-progress" pct={jobPct(rowJob)} label={jobLabel(rowJob)} />
              {/if}
            </li>
          {/each}
        </ul>
      </div>
    {/if}
  {/if}
</div>
