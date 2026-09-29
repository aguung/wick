import { describe, expect, it } from "vitest";
import { normalizeManaged, jobLabel, sessionsNote, isRunning } from "../managedbin";

describe("managedbin client", () => {
  const m = normalizeManaged({
    type: "omp",
    host_label: "linux-x64 · glibc · AVX2",
    current: "18.4.3",
    installed: [
      { version: "18.4.3", current: true, in_use: 1 },
      { version: "18.4.2", in_use: 2, removable: false },
    ],
    latest: { tag: "v18.4.4" },
    update_available: true,
    sessions_on_old: { "18.4.2": 2 },
    job: { phase: "download", done: 50 << 20, total: 200 << 20, tag: "v18.4.4" },
  });

  it("normalizes the wire shape", () => {
    expect(m.hostLabel).toBe("linux-x64 · glibc · AVX2");
    expect(m.latest).toBe("v18.4.4");
    expect(m.installed[1]).toMatchObject({ version: "18.4.2", inUse: 2, removable: false });
    expect(isRunning(m.job)).toBe(true);
  });

  it("labels job progress", () => {
    expect(jobLabel(m.job!)).toBe("Downloading 25% (50 MB / 200 MB)");
    expect(jobLabel({ ...m.job!, phase: "verify" })).toBe("Verifying sha256…");
    expect(jobLabel({ ...m.job!, phase: "error", error: "sha256 mismatch" })).toBe("sha256 mismatch");
    expect(isRunning({ ...m.job!, phase: "done" })).toBe(false);
  });

  it("says which sessions still run an old version", () => {
    expect(sessionsNote(m)).toBe("2 sessions still on v18.4.2");
    expect(sessionsNote({ ...m, sessionsOnOld: {} })).toBe("");
  });
});
