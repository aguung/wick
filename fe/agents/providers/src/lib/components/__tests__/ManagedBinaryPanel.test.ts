import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, fireEvent } from "@testing-library/svelte";
import ManagedBinaryPanel from "../ManagedBinaryPanel.svelte";
import * as mb from "$lib/managedbin.js";

vi.mock("$lib/managedbin.js", async (importOriginal) => {
  const orig = await importOriginal<typeof import("$lib/managedbin.js")>();
  return {
    ...orig,
    apiManagedList: vi.fn(),
    apiManagedInstall: vi.fn(),
    apiManagedActivate: vi.fn(),
    apiManagedRemove: vi.fn(),
    apiManagedCheck: vi.fn(),
    apiManagedVerify: vi.fn(),
    apiManagedReleases: vi.fn(),
  };
});
vi.mock("@wick-fe/common-stores", () => ({
  toastOk: vi.fn(),
  toastError: vi.fn(),
  toasts: { subscribe: vi.fn(() => vi.fn()) },
}));

const base = mb.normalizeManaged({
  type: "omp",
  enabled: true,
  host_label: "linux-x64 · glibc · AVX2",
  current: "18.4.3",
  current_path: "/data/providers/bin/omp/versions/18.4.3/omp",
  installed: [
    { version: "18.4.3", current: true, sha256: "afcecdff1f421f3c", installed_at: "2026-09-29T00:00:00Z" },
    { version: "18.4.2", in_use: 2, removable: false, sha256: "55016ef5317af556" },
    { version: "18.4.1", removable: true, sha256: "1111" },
  ],
  latest: { tag: "v18.4.4" },
  update_available: true,
  sessions_on_old: { "18.4.2": 2 },
});

beforeEach(() => {
  vi.resetAllMocks();
});

describe("ManagedBinaryPanel", () => {
  it("shows host, active version, update flag and sessions on an old version", async () => {
    vi.mocked(mb.apiManagedList).mockResolvedValue({ types: [base], isAdmin: true });
    render(ManagedBinaryPanel, { props: { base: "", type: "omp" } });
    expect((await screen.findByTestId("managed-current")).textContent).toContain("v18.4.3");
    expect(screen.getByTestId("managed-host").textContent).toBe("linux-x64 · glibc · AVX2");
    expect(screen.getByTestId("managed-update-available")).toBeTruthy();
    expect(screen.getByTestId("managed-sessions-old").textContent).toBe("2 sessions still on v18.4.2");
    expect(screen.getAllByTestId("managed-version-row")).toHaveLength(3);
  });

  it("update + rollback call the API; removal disabled for current and in-use", async () => {
    vi.mocked(mb.apiManagedList).mockResolvedValue({ types: [base], isAdmin: true });
    vi.mocked(mb.apiManagedInstall).mockResolvedValue(undefined);
    vi.mocked(mb.apiManagedActivate).mockResolvedValue(base);
    render(ManagedBinaryPanel, { props: { base: "", type: "omp" } });
    await fireEvent.click(await screen.findByText("Update to v18.4.4"));
    expect(mb.apiManagedInstall).toHaveBeenCalledWith("", "omp", "");
    const rows = screen.getAllByTestId("managed-version-row");
    const removeBtns = rows.map((r) => Array.from(r.querySelectorAll("button")).find((b) => b.textContent?.includes("Remove")) as HTMLButtonElement);
    expect(removeBtns[0].disabled).toBe(true); // current
    expect(removeBtns[1].disabled).toBe(true); // in use
    expect(removeBtns[2].disabled).toBe(false);
    await fireEvent.click(Array.from(rows[1].querySelectorAll("button")).find((b) => b.textContent?.includes("Use this version"))!);
    expect(mb.apiManagedActivate).toHaveBeenCalledWith("", "omp", "18.4.2");
  });

  it("not installed: offers Download from GitHub; non-admins get no buttons", async () => {
    const empty = { ...base, current: "", installed: [], updateAvailable: false, sessionsOnOld: {} };
    vi.mocked(mb.apiManagedList).mockResolvedValue({ types: [empty], isAdmin: true });
    const { unmount } = render(ManagedBinaryPanel, { props: { base: "", type: "omp", compact: true } });
    expect(await screen.findByTestId("managed-not-installed")).toBeTruthy();
    expect(screen.getByText("Download from GitHub")).toBeTruthy();
    unmount();
    vi.mocked(mb.apiManagedList).mockResolvedValue({ types: [empty], isAdmin: false });
    render(ManagedBinaryPanel, { props: { base: "", type: "omp" } });
    await screen.findByTestId("managed-not-installed");
    expect(screen.queryByText("Download from GitHub")).toBeNull();
  });

  it("renders job progress while installing", async () => {
    const job = { ...base, job: { id: "j", type: "omp", tag: "v18.4.4", version: "18.4.4", phase: "download", done: 1 << 20, total: 4 << 20, message: "", error: "" } };
    vi.mocked(mb.apiManagedList).mockResolvedValue({ types: [job], isAdmin: true });
    render(ManagedBinaryPanel, { props: { base: "", type: "omp" } });
    expect((await screen.findByTestId("managed-job")).textContent).toContain("Downloading 25%");
  });
});
