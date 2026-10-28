import { afterEach, describe, expect, it, vi } from "vitest";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import Jobs from "@/pages/Jobs";
import type { Job } from "@/queries/jobs";
import { card, job, page } from "@/test/fixtures";
import { json, renderWithClient, stubApi } from "@/test/render";

const read = job({
  id: 3,
  status: "succeeded",
  tar: "b00001",
  kvn: 1,
  step: 2,
  file_size: 30,
  counter: 7,
  message_id: "42",
  por_status: "por_ok",
  status_word: "9000",
  entries: [
    {
      slot: 1,
      mcc: "001",
      mnc: "01",
      access: ["eutran_wb_s1", "eutran_nb_s1", "ngran"],
      access_raw: "7000",
    },
    { slot: 2 },
  ],
  command: "027000001e15060119b00010000001",
  response: "027100000e0ab0001000000100009000",
});
const set = job({
  id: 2,
  operation: "oplmn_set",
  slot: 2,
  mcc: "999",
  mnc: "99",
  access: ["eutran_wb_s1", "eutran_nb_s1", "ngran"],
  access_raw: "7000",
  status: "sent",
});
const unconfirmed = job({
  id: 4,
  operation: "oplmn_set",
  slot: 3,
  mcc: "001",
  mnc: "01",
  access: ["ngran"],
  access_raw: "1000",
  tar: "b00020",
  kvn: 2,
  step: 1,
  counter: 9,
  status: "unconfirmed",
  message_id: "43",
  por_status: "por_ok",
  status_word: "6310",
  warning: true,
  entries: [
    { slot: 3, mcc: "001", mnc: "01", access: ["ngran"], access_raw: "1000" },
  ],
  error: "The card sent no PoR in the delivery report.",
});
const clear = job({
  id: 1,
  operation: "oplmn_clear",
  slot: 2,
  status: "failed",
  por_status: "por_ok",
  status_word: "6a82",
  error: "The card answered 6a82: file not found.",
});

// serve is the jobs API over jobs, newest first. It records the list requests.
const serve = (
  jobs: Job[] = [unconfirmed, read, set, clear],
  smscSet = true,
) => {
  const lists: URL[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(
      stubApi({
        "/api/v1/jobs": (url, init) => {
          if (init?.method === "POST") {
            return json(201, { result: { items: [job({ id: 9 })] } });
          }
          lists.push(url);
          const iccid = url.searchParams.get("iccid");
          return json(200, {
            result: page(
              url,
              iccid ? jobs.filter((j) => j.iccid === iccid) : jobs,
            ),
          });
        },
        ...Object.fromEntries(
          jobs.map((j) => [
            `/api/v1/jobs/${j.id}`,
            () => json(200, { result: j }),
          ]),
        ),
        "/api/v1/cards": (url) => json(200, { result: page(url, [card()]) }),
        "/api/v1/smsc": () =>
          json(200, {
            result: {
              host: smscSet ? "127.0.0.1" : "",
              originator: smscSet ? "+15550000000" : "",
              validity_seconds: 86400,
              password_set: smscSet,
            },
          }),
      }),
    ),
  );
  return lists;
};

afterEach(() => {
  vi.unstubAllGlobals();
});

const grid = () => screen.getByRole("grid", { name: "Jobs" });

const row = (id: number) =>
  grid().querySelector(`[data-id="${id}"]`) as HTMLElement;

const rowCells = (id: number) =>
  within(row(id))
    .getAllByRole("gridcell")
    .map((c) => c.textContent);

const fields = (container: HTMLElement) =>
  Object.fromEntries(
    Array.from(container.querySelectorAll("dt")).map((dt) => [
      dt.textContent,
      dt.nextElementSibling?.textContent,
    ]),
  );

describe("Jobs", () => {
  it("lists the jobs with their operation and status", async () => {
    serve();
    renderWithClient(<Jobs />);

    await screen.findByText("Read roaming list");
    expect(rowCells(3)).toEqual([
      "2026-10-28 17:34:53",
      "8949440000001659208",
      "Read roaming list",
      "succeeded",
      "por_ok",
      "2026-10-28 17:34:54",
    ]);
    expect(rowCells(2).slice(2, 5)).toEqual([
      "Set slot 2 to 999/99 (E-UTRAN WB-S1, E-UTRAN NB-S1, NG-RAN)",
      "sent",
      "—",
    ]);
    expect(rowCells(1).slice(2, 4)).toEqual(["Clear slot 2", "failed"]);
    expect(
      within(row(3)).getByText("succeeded").closest(".MuiChip-root"),
    ).toHaveClass("MuiChip-colorSuccess");
    expect(
      within(row(2)).getByText("sent").closest(".MuiChip-root"),
    ).toHaveClass("MuiChip-colorInfo");
    expect(
      within(row(1)).getByText("failed").closest(".MuiChip-root"),
    ).toHaveClass("MuiChip-colorError");
    expect(
      within(row(4)).getByText("unconfirmed").closest(".MuiChip-root"),
    ).toHaveClass("MuiChip-colorWarning");
  });

  it("explains a status on hover", async () => {
    serve();
    renderWithClient(<Jobs />);
    await screen.findByText("Read roaming list");

    fireEvent.mouseOver(within(row(3)).getByText("succeeded"));

    expect(await screen.findByRole("tooltip")).toHaveTextContent(
      "Succeeded: written on the card; the phone uses it after its next USIM initialisation or REFRESH.",
    );
  });

  it("pages the jobs on the server", async () => {
    const many = Array.from({ length: 30 }, (_, i) => job({ id: 30 - i }));
    const lists = serve(many);
    renderWithClient(<Jobs />);

    expect(await screen.findByText("1–25 of 30")).toBeInTheDocument();
    expect(row(30)).not.toBeNull();
    expect(row(5)).toBeNull();

    fireEvent.click(screen.getByRole("button", { name: "Go to next page" }));

    await waitFor(() => expect(row(5)).not.toBeNull());
    expect(screen.getByText("26–30 of 30")).toBeInTheDocument();
    expect(lists.map((u) => u.search)).toEqual([
      "?page=1&per_page=25",
      "?page=2&per_page=25",
    ]);
  });

  it("filters by card", async () => {
    const other = job({ id: 5, iccid: "8949440000001659216" });
    const lists = serve([other, read]);
    renderWithClient(<Jobs />);
    await screen.findByText("1–2 of 2");

    const filter = screen.getByRole("textbox", { name: "Card" });
    fireEvent.change(filter, { target: { value: "89494400" } });
    expect(
      screen.getByText("An ICCID, of 18 to 20 digits."),
    ).toBeInTheDocument();

    fireEvent.change(filter, { target: { value: other.iccid } });
    expect(await screen.findByText("1–1 of 1")).toBeInTheDocument();
    expect(row(5)).not.toBeNull();
    expect(lists.map((u) => u.searchParams.get("iccid"))).toEqual([
      null,
      other.iccid,
    ]);
  });

  it("shows what the card read in the drawer", async () => {
    serve();
    renderWithClient(<Jobs />);
    await screen.findByText("Read roaming list");

    fireEvent.click(within(row(3)).getByText("Read roaming list"));

    const drawer = screen.getByRole("complementary", { name: "Job 3" });
    expect(fields(drawer)).toEqual({
      Card: "8949440000001659208",
      Operation: "Read roaming list",
      TAR: "b00001",
      "Key set": "1",
      Created: "2026-10-28 17:34:53",
      Updated: "2026-10-28 17:34:54",
      Step: "2",
      Counter: "7",
      "File size": "30",
      "SMSC message": "42",
      "PoR status": "por_ok",
      "Status word": "9000",
    });
    const entries = within(drawer).getByRole("table", {
      name: "Roaming list (2)",
    });
    expect(
      within(entries)
        .getAllByRole("row")
        .map((r) =>
          Array.from(r.querySelectorAll("th, td")).map((c) => c.textContent),
        ),
    ).toEqual([
      ["Slot", "PLMN", "Access technologies"],
      ["1", "001/01", "E-UTRAN WB-S1, E-UTRAN NB-S1, NG-RAN (7000)"],
      ["2", "—", "—"],
    ]);
    expect(within(drawer).queryByRole("alert")).not.toBeInTheDocument();

    expect(within(drawer).queryByText(read.command!)).not.toBeInTheDocument();
    fireEvent.click(
      within(drawer).getByRole("button", { name: "Show packets" }),
    );
    expect(within(drawer).getByText(read.command!)).toBeInTheDocument();
    expect(within(drawer).getByText(read.response!)).toBeInTheDocument();

    fireEvent.keyDown(window, { key: "Escape" });
    await waitFor(() =>
      expect(
        screen.queryByRole("complementary", { name: "Job 3" }),
      ).not.toBeInTheDocument(),
    );
  });

  it("shows why a job failed", async () => {
    serve();
    renderWithClient(<Jobs />);
    await screen.findByText("Clear slot 2");

    fireEvent.click(within(row(1)).getByText("Clear slot 2"));

    const drawer = screen.getByRole("complementary", { name: "Job 1" });
    expect(within(drawer).getByRole("alert")).toHaveTextContent(
      "The card answered 6a82: file not found.",
    );
    expect(fields(drawer)).toMatchObject({
      "SMSC message": "—",
      "PoR status": "por_ok",
      "Status word": "6a82",
    });
    expect(within(drawer).getByText("Slot read back")).toBeInTheDocument();
    expect(within(drawer).getByText("Not read.")).toBeInTheDocument();
  });

  it("shows an unconfirmed set, its warning and the slot read back", async () => {
    serve();
    renderWithClient(<Jobs />);
    await screen.findByText("Clear slot 2");

    fireEvent.click(within(row(4)).getByText(/^Set slot 3/));

    const drawer = screen.getByRole("complementary", { name: "Job 4" });
    const alert = within(drawer).getByRole("alert");
    expect(alert).toHaveTextContent(
      "The card sent no PoR in the delivery report.",
    );
    expect(alert).toHaveClass("MuiAlert-colorWarning");
    expect(fields(drawer)).toEqual({
      Card: "8949440000001659208",
      Operation: "Set slot 3 to 001/01 (NG-RAN)",
      "Access technologies": "NG-RAN (1000)",
      TAR: "b00020",
      "Key set": "2",
      Created: "2026-10-28 17:34:53",
      Updated: "2026-10-28 17:34:54",
      Step: "1",
      Counter: "9",
      "SMSC message": "43",
      "PoR status": "por_ok",
      "Status word": "6310 (warning)",
    });
    const entries = within(drawer).getByRole("table", {
      name: "Slot read back",
    });
    expect(
      within(entries)
        .getAllByRole("row")
        .map((r) =>
          Array.from(r.querySelectorAll("th, td")).map((c) => c.textContent),
        ),
    ).toEqual([
      ["Slot", "PLMN", "Access technologies"],
      ["3", "001/01", "NG-RAN (1000)"],
    ]);
  });

  it("disables New Job until the SMSC is set", async () => {
    serve(undefined, false);
    renderWithClient(<Jobs />);
    await screen.findByText("Read roaming list");

    const button = screen.getByRole("button", { name: "New Job" });
    await waitFor(() => expect(button).toBeDisabled());
    fireEvent.mouseOver(button.parentElement!);
    expect(await screen.findByRole("tooltip")).toHaveTextContent(
      "Set the SMSC first",
    );
  });

  it("enables New Job once the SMSC is set", async () => {
    serve();
    renderWithClient(<Jobs />);
    await screen.findByText("Read roaming list");

    await waitFor(() =>
      expect(
        vi.mocked(fetch).mock.calls.some(([u]) => String(u) === "/api/v1/smsc"),
      ).toBe(true),
    );
    expect(screen.getByRole("button", { name: "New Job" })).toBeEnabled();
  });

  it("refreshes the list after queuing a job", async () => {
    const lists = serve();
    renderWithClient(<Jobs />);
    await screen.findByText("Read roaming list");
    fireEvent.click(screen.getByRole("button", { name: "New Job" }));
    const dialog = screen.getByRole("dialog", { name: "New Job" });
    await waitFor(() =>
      expect(
        vi
          .mocked(fetch)
          .mock.calls.some(([u]) => String(u).startsWith("/api/v1/cards")),
      ).toBe(true),
    );
    fireEvent.click(within(dialog).getByRole("button", { name: "Open" }));
    fireEvent.click(await screen.findByRole("option", { name: /^8949/ }));
    fireEvent.click(within(dialog).getByRole("button", { name: "Create" }));

    expect(await screen.findByText("1 job queued.")).toBeInTheDocument();
    await waitFor(() => expect(lists).toHaveLength(2));
  });
});
