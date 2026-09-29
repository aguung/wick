/* managedbin.ts — client for wick-managed provider binaries
   (/api/managed-binaries). Install/update runs as a server job; the UI
   polls the list for progress. Every write is admin-only server-side. */

import { get, post } from "$lib/api.js";

export type ManagedJob = {
  id: string;
  type: string;
  tag: string;
  version: string;
  phase: string; // resolve | download | verify | probe | switching | done | error
  done: number;
  total: number;
  message: string;
  error: string;
};

export type InstalledVersion = {
  version: string;
  tag: string;
  asset: string;
  sha256: string;
  installedAt: string;
  versionOutput: string;
  current: boolean;
  inUse: number;
  removable: boolean;
};

export type ManagedBinary = {
  type: string;
  binary: string;
  repo: string;
  hostLabel: string;
  enabled: boolean;
  current: string;
  currentPath: string;
  installed: InstalledVersion[];
  latest: string; // tag, "" when unknown
  latestErr: string;
  updateAvailable: boolean;
  job: ManagedJob | null;
  lastJob: ManagedJob | null;
  sessionsOnOld: Record<string, number>;
};

type WireJob = Partial<{
  id: string; type: string; tag: string; version: string; phase: string;
  done: number; total: number; message: string; error: string;
}>;

type WireInstalled = Partial<{
  version: string; tag: string; asset: string; sha256: string; installed_at: string;
  version_output: string; current: boolean; in_use: number; removable: boolean;
}>;

type WireManaged = Partial<{
  type: string; binary: string; repo: string; host_label: string; enabled: boolean;
  current: string; current_path: string; installed: WireInstalled[] | null;
  latest: { tag?: string } | null; latest_err: string; update_available: boolean;
  job: WireJob | null; last_job: WireJob | null; sessions_on_old: Record<string, number> | null;
}>;

function mapJob(w: WireJob | null | undefined): ManagedJob | null {
  if (!w || !w.phase) return null;
  return {
    id: w.id ?? "", type: w.type ?? "", tag: w.tag ?? "", version: w.version ?? "",
    phase: w.phase ?? "", done: w.done ?? 0, total: w.total ?? 0,
    message: w.message ?? "", error: w.error ?? "",
  };
}

export function normalizeManaged(w: WireManaged): ManagedBinary {
  return {
    type: w.type ?? "",
    binary: w.binary ?? "",
    repo: w.repo ?? "",
    hostLabel: w.host_label ?? "",
    enabled: w.enabled ?? false,
    current: w.current ?? "",
    currentPath: w.current_path ?? "",
    installed: (w.installed ?? []).map((i) => ({
      version: i.version ?? "", tag: i.tag ?? "", asset: i.asset ?? "", sha256: i.sha256 ?? "",
      installedAt: i.installed_at ?? "", versionOutput: i.version_output ?? "",
      current: i.current ?? false, inUse: i.in_use ?? 0, removable: i.removable ?? false,
    })),
    latest: w.latest?.tag ?? "",
    latestErr: w.latest_err ?? "",
    updateAvailable: w.update_available ?? false,
    job: mapJob(w.job),
    lastJob: mapJob(w.last_job),
    sessionsOnOld: w.sessions_on_old ?? {},
  };
}

export function isRunning(j: ManagedJob | null): boolean {
  return !!j && j.phase !== "done" && j.phase !== "error";
}

/* jobLabel is the one-line progress text: "Downloading 42% (120 / 286 MB)". */
export function jobLabel(j: ManagedJob): string {
  const mb = (n: number) => `${Math.round(n / (1 << 20))} MB`;
  switch (j.phase) {
    case "resolve":
      return "Looking up release…";
    case "download": {
      if (j.total > 0) {
        const pct = Math.min(100, Math.floor((j.done / j.total) * 100));
        return `Downloading ${pct}% (${mb(j.done)} / ${mb(j.total)})`;
      }
      return `Downloading ${mb(j.done)}`;
    }
    case "verify":
      return "Verifying sha256…";
    case "probe":
      return "Running --version…";
    case "switching":
      return "Switching…";
    case "done":
      return j.message || "Done";
    case "error":
      return j.error || "Failed";
  }
  return j.phase;
}

/* sessionsNote: "2 sessions still on v18.4.2" for every old version in use. */
export function sessionsNote(m: ManagedBinary): string {
  const parts = Object.entries(m.sessionsOnOld)
    .filter(([, n]) => n > 0)
    .map(([v, n]) => `${n} ${n === 1 ? "session" : "sessions"} still on v${v}`);
  return parts.join(" · ");
}

function base(b: string): string {
  return `${b}/api/managed-binaries`;
}

export async function apiManagedList(b: string): Promise<{ types: ManagedBinary[]; isAdmin: boolean }> {
  const r = await get<{ types?: WireManaged[]; is_admin?: boolean }>(base(b));
  return { types: (r.types ?? []).map(normalizeManaged), isAdmin: r.is_admin ?? false };
}

export async function apiManagedReleases(b: string, type: string): Promise<{ tag: string; prerelease: boolean }[]> {
  const r = await get<{ releases?: { tag?: string; prerelease?: boolean }[] }>(`${base(b)}/${encodeURIComponent(type)}/releases`);
  return (r.releases ?? []).map((x) => ({ tag: x.tag ?? "", prerelease: x.prerelease ?? false })).filter((x) => x.tag);
}

export async function apiManagedInstall(b: string, type: string, tag = ""): Promise<void> {
  const q = tag ? `?tag=${encodeURIComponent(tag)}` : "";
  await post(`${base(b)}/${encodeURIComponent(type)}/install${q}`);
}

export async function apiManagedCheck(b: string, type: string): Promise<ManagedBinary> {
  return normalizeManaged(await post<WireManaged>(`${base(b)}/${encodeURIComponent(type)}/check`));
}

export async function apiManagedActivate(b: string, type: string, version: string): Promise<ManagedBinary> {
  return normalizeManaged(await post<WireManaged>(`${base(b)}/${encodeURIComponent(type)}/activate?version=${encodeURIComponent(version)}`));
}

export async function apiManagedRemove(b: string, type: string, version: string): Promise<ManagedBinary> {
  return normalizeManaged(await post<WireManaged>(`${base(b)}/${encodeURIComponent(type)}/remove?version=${encodeURIComponent(version)}`));
}

export async function apiManagedVerify(b: string, type: string): Promise<{ output: string; error: string }> {
  const r = await post<{ output?: string; error?: string }>(`${base(b)}/${encodeURIComponent(type)}/verify`);
  return { output: r?.output ?? "", error: r?.error ?? "" };
}
