import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, fireEvent, waitFor } from "@testing-library/svelte";
import LiveModelsPanel from "../LiveModelsPanel.svelte";
import * as api from "$lib/api.js";

vi.mock("$lib/api.js", async (importOriginal) => {
  const orig = await importOriginal<typeof import("$lib/api.js")>();
  return { ...orig, apiGetCLIModels: vi.fn() };
});

const models = [
  { id: "opencode/big-pickle" },
  { id: "openai/gpt-5.5" },
  { id: "openai/gpt-5.5-mini" },
  { id: "anthropic/claude-sonnet" },
  { id: "google/gemini-3" },
];

function mockList(hostedAllowed: boolean) {
  vi.mocked(api.apiGetCLIModels).mockResolvedValue({
    models, offered: [], hostedAllowed, fetchedAt: "2026-09-29T00:00:00Z",
  });
}

function renderPanel(props: Partial<{ filter: string; pin: string; type: string }> = {}) {
  const onSaveFilter = vi.fn();
  const onSavePin = vi.fn();
  render(LiveModelsPanel, {
    props: { base: "", type: props.type ?? "opencode", name: "oc", filter: props.filter ?? "", pin: props.pin ?? "", onSaveFilter, onSavePin },
  });
  return { onSaveFilter, onSavePin };
}

describe("LiveModelsPanel", () => {
  beforeEach(() => vi.mocked(api.apiGetCLIModels).mockReset());

  it("hides hosted opencode models unless allowed and counts matches", async () => {
    mockList(false);
    renderPanel();
    await waitFor(() => expect(screen.getByTestId("live-models-count").textContent).toMatch(/4\s+of 4 models match/));
    expect(screen.getByTestId("live-models-count").textContent).toContain("1 hosted opencode models hidden");
    expect(screen.getByTestId("live-models-list").textContent).not.toContain("opencode/big-pickle");
  });

  it("previews the unsaved filter with the shared grammar and saves it", async () => {
    mockList(true);
    const { onSaveFilter } = renderPanel();
    await waitFor(() => expect(screen.getByTestId("live-models-list")).toBeTruthy());
    const input = screen.getByTestId("live-models-filter") as HTMLInputElement;
    await fireEvent.input(input, { target: { value: "claude|gpt !mini" } });
    const list = screen.getByTestId("live-models-list").textContent ?? "";
    expect(list).toContain("openai/gpt-5.5");
    expect(list).toContain("anthropic/claude-sonnet");
    expect(list).not.toContain("gpt-5.5-mini");
    expect(list).not.toContain("gemini");
    // first match is the default when nothing is pinned
    expect(screen.getByTestId("live-models-count").textContent).toMatch(/2\s+of 5/);
    await fireEvent.click(screen.getByTestId("live-models-save-filter"));
    expect(onSaveFilter).toHaveBeenCalledWith("claude|gpt !mini");
  });

  it("marks the pinned model as default and refreshes on demand", async () => {
    mockList(true);
    renderPanel({ pin: "anthropic/claude-sonnet" });
    await waitFor(() => expect(screen.getByTestId("live-models-list").textContent).toMatch(/claude-sonnet\s*default/));
    await fireEvent.click(screen.getByTestId("live-models-refresh"));
    expect(api.apiGetCLIModels).toHaveBeenLastCalledWith("", "opencode", "oc", true);
  });

  it("shows the refresh error over a stale list", async () => {
    vi.mocked(api.apiGetCLIModels).mockResolvedValue({ models, offered: [], hostedAllowed: true, fetchedAt: "", error: "opencode models: boom" });
    renderPanel();
    await waitFor(() => expect(screen.getByTestId("live-models-error").textContent).toContain("boom"));
  });
});
