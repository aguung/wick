import { describe, test, expect, vi } from "vitest";
import { render, screen, fireEvent } from "@testing-library/svelte";
import TicketFields from "../TicketFields.svelte";
import type { TicketField } from "../../types/agents.js";

const defs: TicketField[] = [
  { key: "app_code", label: "App Code", type: "text" },
  { key: "slack", label: "Slack discussion", type: "text" },
  { key: "qticket", label: "Qticketing ticket", type: "text" },
  { key: "priority", label: "Priority", type: "select", options: ["Low", "High"] },
  { key: "changelog", label: "Change Log", type: "text" },
  { key: "owner", label: "Owner", type: "text", required: true },
];
const values = {
  app_code: "locot-uv3dlm3k0cgpbla",
  slack: "https://qiscustech.slack.com/archives/C030CBY48KF/p1790319115225609",
  qticket: "Qticketing ticket : https://support.qiscus.com/tickets/23188",
  owner: "Yoga",
  notion_page_id: "d7f18757-internal",
};

function renderFields(over: Record<string, unknown> = {}) {
  const onSave = vi.fn(async () => {});
  const r = render(TicketFields, { props: { fields: defs, values, onSave, ...over } });
  return { ...r, onSave };
}
const tick = () => new Promise((r) => setTimeout(r, 0));

describe("TicketFields — reading", () => {
  test("only defined fields show; a key nobody defined stays hidden", () => {
    renderFields({ initialVisible: 10 });
    expect(screen.getByText("locot-uv3dlm3k0cgpbla")).toBeTruthy();
    expect(screen.queryByText("d7f18757-internal")).toBeNull();
  });

  test("a URL is a link, with an open-in-new-tab button that is safe", () => {
    renderFields();
    const open = screen.getByTestId("ticket-field-open-slack") as HTMLAnchorElement;
    expect(open.getAttribute("href")).toBe(values.slack);
    expect(open.getAttribute("target")).toBe("_blank");
    expect(open.getAttribute("rel")).toContain("noopener");
    const inline = screen.getByText(values.slack) as HTMLAnchorElement;
    expect(inline.tagName).toBe("A");
    expect(inline.getAttribute("target")).toBe("_blank");
  });

  test("a URL inside text is linked and the text around it kept", () => {
    renderFields();
    const row = screen.getByTestId("ticket-field-qticket");
    expect(row.textContent).toContain("Qticketing ticket :");
    expect((screen.getByTestId("ticket-field-open-qticket") as HTMLAnchorElement).getAttribute("href")).toBe(
      "https://support.qiscus.com/tickets/23188",
    );
  });

  test("a value without a URL gets no open button", () => {
    renderFields();
    expect(screen.queryByTestId("ticket-field-open-app_code")).toBeNull();
  });

  test("an empty field is a slot to fill in", () => {
    renderFields();
    expect(screen.getByTestId("ticket-field-add-priority").textContent).toContain("Add Priority");
  });

  test("a few rows first, the rest behind Show more", async () => {
    renderFields();
    expect(screen.queryByTestId("ticket-field-changelog")).toBeNull();
    const toggle = screen.getByTestId("ticket-fields-toggle");
    expect(toggle.textContent).toContain("Show more (2)");
    await fireEvent.click(toggle);
    expect(screen.getByTestId("ticket-field-changelog")).toBeTruthy();
    expect(screen.getByTestId("ticket-fields-toggle").textContent).toContain("Show less");
  });

  test("no toggle when everything fits", () => {
    renderFields({ fields: defs.slice(0, 2) });
    expect(screen.queryByTestId("ticket-fields-toggle")).toBeNull();
  });
});

describe("TicketFields — editing in place", () => {
  test("Enter saves the trimmed value for that one key", async () => {
    const { onSave } = renderFields();
    await fireEvent.click(screen.getByTestId("ticket-field-edit-app_code"));
    const input = screen.getByTestId("ticket-field-input-app_code") as HTMLInputElement;
    await fireEvent.input(input, { target: { value: "  locot-new  " } });
    await fireEvent.keyDown(input, { key: "Enter" });
    await tick();
    expect(onSave).toHaveBeenCalledWith("app_code", "locot-new");
    expect(screen.getByText("locot-new")).toBeTruthy();
  });

  // Escape removes the input, which fires blur. That blur must not save the
  // cleared draft — it would erase the field.
  test("Escape discards and never saves, even with the blur that follows", async () => {
    const { onSave } = renderFields();
    await fireEvent.click(screen.getByTestId("ticket-field-edit-app_code"));
    const input = screen.getByTestId("ticket-field-input-app_code") as HTMLInputElement;
    await fireEvent.input(input, { target: { value: "typed then abandoned" } });
    await fireEvent.keyDown(input, { key: "Escape" });
    await fireEvent.blur(input);
    await tick();
    expect(onSave).not.toHaveBeenCalled();
    expect(screen.getByText("locot-uv3dlm3k0cgpbla")).toBeTruthy();
  });

  test("an unchanged value does not call the server", async () => {
    const { onSave } = renderFields();
    await fireEvent.click(screen.getByTestId("ticket-field-edit-app_code"));
    await fireEvent.keyDown(screen.getByTestId("ticket-field-input-app_code"), { key: "Enter" });
    await tick();
    expect(onSave).not.toHaveBeenCalled();
  });

  test("the empty slot opens the editor and saves on blur", async () => {
    const { onSave } = renderFields({ initialVisible: 10 });
    await fireEvent.click(screen.getByTestId("ticket-field-add-changelog"));
    const input = screen.getByTestId("ticket-field-input-changelog") as HTMLInputElement;
    await fireEvent.input(input, { target: { value: "v1.2 ships the fix" } });
    await fireEvent.blur(input);
    await tick();
    expect(onSave).toHaveBeenCalledWith("changelog", "v1.2 ships the fix");
  });

  test("a select field saves the picked option", async () => {
    const { onSave } = renderFields();
    await fireEvent.click(screen.getByTestId("ticket-field-add-priority"));
    const sel = screen.getByTestId("ticket-field-input-priority") as HTMLSelectElement;
    await fireEvent.change(sel, { target: { value: "High" } });
    await tick();
    expect(onSave).toHaveBeenCalledWith("priority", "High");
  });

  test("a required field cannot be cleared", async () => {
    const { onSave } = renderFields({ initialVisible: 10 });
    await fireEvent.click(screen.getByTestId("ticket-field-edit-owner"));
    const input = screen.getByTestId("ticket-field-input-owner") as HTMLInputElement;
    await fireEvent.input(input, { target: { value: "   " } });
    await fireEvent.keyDown(input, { key: "Enter" });
    await tick();
    expect(onSave).not.toHaveBeenCalled();
  });

  test("a failed save keeps the editor open with what was typed", async () => {
    const onSave = vi.fn(async () => { throw new Error("nope"); });
    render(TicketFields, { props: { fields: defs, values, onSave } });
    await fireEvent.click(screen.getByTestId("ticket-field-edit-app_code"));
    const input = screen.getByTestId("ticket-field-input-app_code") as HTMLInputElement;
    await fireEvent.input(input, { target: { value: "will fail" } });
    await fireEvent.keyDown(input, { key: "Enter" });
    await tick();
    expect((screen.getByTestId("ticket-field-input-app_code") as HTMLInputElement).value).toBe("will fail");
  });
});
