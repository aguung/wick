<script lang="ts">
  /* ManagedBinaryPanel — the "Binary" section for one wick-managed type
     (omp, opencode): active version, host, newest release, Install/Update
     (latest or a picked version), live job progress, installed versions
     with rollback/remove, and how many sessions still run an old file.
     Nothing downloads unless an admin clicks; the server re-checks
     everything (sha256, --version, in-use) regardless of what this shows. */
  import { onDestroy, onMount } from "svelte";
  import { Button, Select } from "@wick-fe/common-ui";
  import { toastError, toastOk } from "@wick-fe/common-stores";
  import {
    apiManagedList,
    apiManagedInstall,
    apiManagedActivate,
    apiManagedRemove,
    apiManagedCheck,
    apiManagedVerify,
    apiManagedReleases,
    isRunning,
    jobLabel,
    sessionsNote,
    type ManagedBinary,
  } from "$lib/managedbin.js";

  type Props = {
    base: string;
    type: string;
    /* Compact = inside the Add form: status + Install only. */
    compact?: boolean;
    onChange?: (m: ManagedBinary | null) => void;
  };
  let { base, type, compact = false, onChange }: Props = $props();

  let data = $state<ManagedBinary | null>(null);
  let isAdmin = $state(false);
  let loading = $state(true);
  let busy = $state("");
  let pickTag = $state("");
  let releases = $state<{ tag: string; prerelease: boolean }[]>([]);
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
        else toastOk(`${type}: ${data.job.message || "installed"}`);
      }
    } catch (e) {
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
      toastError(errText(e));
    } finally {
      busy = "";
      await load();
    }
  }

  const install = (tag = "") => act("install", () => apiManagedInstall(base, type, tag));
  const activate = (v: string) => act("act-" + v, async () => { await apiManagedActivate(base, type, v); toastOk(`${type} v${v} is now active`); });
  const remove = (v: string) => act("rm-" + v, async () => { await apiManagedRemove(base, type, v); toastOk(`Removed ${type} v${v}`); });
  const check = () => act("check", () => apiManagedCheck(base, type));
  const verify = () => act("verify", async () => { verifyOut = await apiManagedVerify(base, type); });

  async function loadReleases(): Promise<void> {
    if (releases.length > 0) return;
    try {
      releases = await apiManagedReleases(base, type);
    } catch (e) {
      toastError(errText(e));
    }
  }

  const running = $derived(isRunning(data?.job ?? null));
  const note = $derived(data ? sessionsNote(data) : "");
  const failed = $derived(data?.lastJob?.phase === "error" && !running ? data.lastJob : null);
  const installedTags = $derived(new Set((data?.installed ?? []).map((i) => i.tag)));
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
        <span class="text-black-800 dark:text-black-600">Latest on GitHub <span class="font-mono">{data.latest}</span></span>
      {/if}
      {#if data.updateAvailable}
        <span data-testid="managed-update-available" class="rounded bg-cau-100 dark:bg-cau-400/20 px-1.5 py-0.5 text-[11px] font-medium text-cau-400">update available</span>
      {/if}
      {#if note}
        <span data-testid="managed-sessions-old" class="text-black-800 dark:text-black-600">{note}</span>
      {/if}
    </div>
    {#if data.currentPath && !compact}
      <p class="font-mono text-[11px] text-black-700 dark:text-black-600 break-all">{data.currentPath}</p>
    {/if}

    {#if running && data.job}
      <div data-testid="managed-job" class="space-y-1">
        <p class="text-xs text-black-900 dark:text-white-100">{data.job.tag || "latest"} · {jobLabel(data.job)}</p>
        {#if data.job.phase === "download" && data.job.total > 0}
          <div class="h-2 w-full rounded-full bg-white-300 dark:bg-navy-600 overflow-hidden">
            <div class="h-full rounded-full bg-green-500" style={`width: ${Math.min(100, (data.job.done / data.job.total) * 100)}%`}></div>
          </div>
        {/if}
      </div>
    {:else if failed}
      <p data-testid="managed-job-error" class="rounded-lg border border-neg-400 px-3 py-2 text-xs text-neg-400">Last install failed ({failed.tag || "latest"}): {failed.error}. The active version was not changed.</p>
    {/if}

    {#if isAdmin && data.enabled}
      <div class="flex flex-wrap items-center gap-2">
        {#if !data.current}
          <Button variant="primary" disabled={running || busy !== ""} onclick={() => install()}>Download from GitHub</Button>
        {:else if data.updateAvailable}
          <Button variant="primary" disabled={running || busy !== ""} onclick={() => install()}>Update to {data.latest}</Button>
        {/if}
        {#if !compact}
          <Button variant="secondary" disabled={busy !== ""} onclick={check}>{busy === "check" ? "Checking…" : "Check for update"}</Button>
          {#if data.current}
            <Button variant="secondary" disabled={busy !== ""} onclick={verify}>Re-check --version</Button>
          {/if}
          <div class="flex items-center gap-2" data-testid="managed-pick-version">
            <div role="presentation" onfocusin={() => void loadReleases()} onmouseenter={() => void loadReleases()}>
              <Select
                class="w-48"
                size="sm"
                ariaLabel="Pick a version"
                placeholder="Pick a version…"
                value={pickTag}
                options={releases.map((r) => ({
                  label: r.tag,
                  value: r.tag,
                  ...(installedTags.has(r.tag) ? { badge: "installed" } : r.prerelease ? { badge: "pre" } : {}),
                }))}
                onChange={(v) => { pickTag = v; }}
              />
            </div>
            <Button variant="secondary" disabled={!pickTag || running || busy !== ""} onclick={() => install(pickTag)}>Install</Button>
          </div>
        {/if}
      </div>
    {/if}

    {#if verifyOut}
      <p data-testid="managed-verify" class="font-mono text-[11px] {verifyOut.error ? 'text-neg-400' : 'text-black-800 dark:text-black-600'}">
        wick ran --version → {verifyOut.output || "(no output)"}{verifyOut.error ? ` — ${verifyOut.error}` : ""}
      </p>
    {/if}

    {#if !compact && data.installed.length > 0}
      <div class="space-y-1">
        <p class="text-[11px] font-semibold tracking-wide text-black-700 dark:text-black-600">INSTALLED VERSIONS</p>
        <ul class="divide-y divide-white-300 dark:divide-navy-600 text-xs">
          {#each data.installed as v (v.version)}
            <li data-testid="managed-version-row" class="flex flex-wrap items-center gap-x-3 gap-y-1 py-2">
              <span class="font-mono font-medium text-black-900 dark:text-white-100">v{v.version}</span>
              {#if v.current}
                <span class="rounded bg-pos-100 dark:bg-pos-400/20 px-1.5 py-0.5 text-[11px] font-medium text-pos-400">active</span>
              {/if}
              <span class="text-black-700 dark:text-black-600">{v.installedAt ? new Date(v.installedAt).toLocaleString() : ""}</span>
              <span class="font-mono text-black-700 dark:text-black-600" title={v.sha256}>sha256 {v.sha256.slice(0, 12)}</span>
              {#if v.inUse > 0}
                <span class="text-black-800 dark:text-black-600">{v.inUse} running</span>
              {/if}
              {#if isAdmin}
                <span class="ml-auto flex gap-2">
                  {#if !v.current}
                    <Button variant="secondary" disabled={busy !== "" || running} onclick={() => activate(v.version)}>Use this version</Button>
                  {/if}
                  <Button
                    variant="secondary"
                    disabled={!v.removable || busy !== "" || running}
                    title={v.current ? "The active version cannot be removed" : v.inUse > 0 ? "Still used by a running session" : ""}
                    onclick={() => remove(v.version)}
                  >Remove</Button>
                </span>
              {/if}
            </li>
          {/each}
        </ul>
      </div>
    {/if}
  {/if}
</div>
