/* accounts.ts — client-side helpers for the provider types where one
   instance = one account (omp, opencode). Mirrors the BE rules in
   internal/agents/provider/accounts.go so the add form can preview what
   the server will pin; the server stays the authority. */

export const ACCOUNT_ISOLATED = new Set(["omp", "opencode"]);

export type TypeInfo = { label: string; desc: string };

/* Short description per type for the Add provider picker. Unknown types
   fall back to the bare key. */
export const TYPE_INFO: Record<string, TypeInfo> = {
  claude: { label: "Claude Code", desc: "Anthropic's claude CLI" },
  codex: { label: "Codex", desc: "OpenAI's codex CLI" },
  gemini: { label: "Gemini CLI", desc: "Google's gemini CLI (experimental)" },
  omp: { label: "oh-my-pi (omp)", desc: "omp CLI — ChatGPT or Claude login, one omp profile per instance" },
  opencode: { label: "opencode", desc: "opencode CLI — own data dir per instance; Claude subscriptions not supported" },
  wick: { label: "Wick", desc: "Built-in engine" },
};

export function typeLabel(t: string): string {
  const i = TYPE_INFO[t];
  return i ? `${i.label} — ${i.desc}` : t;
}

/* suggestName picks `type`, then `type_2`, `type_3`, … — the first not
   taken. '_' because instance names only allow [A-Za-z0-9_]. */
export function suggestName(type: string, taken: string[]): string {
  const used = new Set(taken);
  if (!used.has(type)) return type;
  for (let i = 2; i < 1000; i++) {
    const n = `${type}_${i}`;
    if (!used.has(n)) return n;
  }
  return "";
}

/* defaultOMPProfile mirrors provider.DefaultOMPProfile: "wick-<name>" in
   omp's accepted alphabet ^[a-z0-9][a-z0-9._-]{0,63}$. */
export function defaultOMPProfile(name: string): string {
  let n = name.trim().toLowerCase().replace(/[^a-z0-9._-]+/g, "-").replace(/^[-.]+|[-.]+$/g, "");
  if (!n) n = "default";
  let p = `wick-${n}`;
  if (p.length > 64) p = p.slice(0, 64).replace(/[-.]+$/, "");
  return p;
}

export function validOMPProfile(p: string): boolean {
  return /^[a-z0-9][a-z0-9._-]{0,63}$/.test(p);
}

/* accountStorePreview is what the form shows before the instance exists. */
export function accountStorePreview(type: string, name: string): string {
  if (type === "omp") return `profile ${defaultOMPProfile(name || "omp")}`;
  if (type === "opencode") return `<wick data>/providers/opencode/${name || "opencode"}`;
  return "";
}
