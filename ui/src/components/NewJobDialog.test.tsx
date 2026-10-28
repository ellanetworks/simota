import { afterEach, describe, expect, it, vi } from "vitest";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import NewJobDialog from "@/components/NewJobDialog";
import { card, job, page } from "@/test/fixtures";
import { json, renderWithClient, stubApi } from "@/test/render";

const lab = card();
const spare = card({
  iccid: "8949440000001659216",
  msisdn: "+15550000002",
  description: "",
});
const blank = card({ iccid: "8949440000001659224", msisdn: "" });

type Call = { path: string; body: unknown };

// serve answers the cards, and the jobs that create answers, or its error.
const serve = (
  create: (body: { iccids: string[] }) => Response = (body) =>
    json(201, {
      result: {
        items: body.iccids.map((iccid, i) => job({ id: i + 1, iccid })),
      },
    }),
) => {
  const calls: Call[] = [];
  const cardLists: URL[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(
      stubApi({
        "/api/v1/cards": (url) => {
          cardLists.push(url);
          return json(200, { result: page(url, [lab, spare, blank]) });
        },
        "/api/v1/jobs": (url, init) => {
          const body = JSON.parse(String(init?.body));
          calls.push({ path: url.pathname, body });
          return create(body);
        },
      }),
    ),
  );
  return { calls, cardLists };
};

afterEach(() => {
  vi.unstubAllGlobals();
});

const open = async (iccids?: string[]) => {
  const onClose = vi.fn();
  renderWithClient(<NewJobDialog iccids={iccids} onClose={onClose} />);
  const dialog = screen.getByRole("dialog", { name: "New Job" });
  // The cards are loaded once the input says what they need.
  await within(dialog).findByText(
    "A card needs an MSISDN, and may have one job in progress.",
  );
  return { dialog, onClose };
};

const options = async (dialog: HTMLElement) => {
  fireEvent.click(within(dialog).getByRole("button", { name: "Open" }));
  return within(await screen.findByRole("listbox")).getAllByRole("option");
};

const pick = async (dialog: HTMLElement, iccid: string) => {
  const option = (await options(dialog)).find((o) =>
    o.textContent?.startsWith(iccid),
  )!;
  fireEvent.click(option);
};

const choose = (dialog: HTMLElement, name: RegExp, option: string) => {
  fireEvent.mouseDown(within(dialog).getByRole("combobox", { name }));
  fireEvent.click(screen.getByRole("option", { name: option }));
};

const type = (dialog: HTMLElement, name: RegExp, value: string) =>
  fireEvent.change(within(dialog).getByRole("textbox", { name }), {
    target: { value },
  });

const create = (dialog: HTMLElement) =>
  within(dialog).getByRole("button", { name: "Create" });

describe("NewJobDialog", () => {
  it("offers the first 100 cards, and disables those without an MSISDN", async () => {
    const { cardLists } = serve();
    const { dialog } = await open();

    expect(cardLists.map((u) => u.search)).toEqual(["?page=1&per_page=100"]);
    const [first, second, third] = await options(dialog);
    expect(first).toHaveTextContent(
      "8949440000001659208+15550000001 · Lab phone",
    );
    expect(second).toHaveTextContent("8949440000001659216+15550000002");
    expect(third).toHaveTextContent("8949440000001659224no MSISDN · Lab phone");
    expect(third).toHaveAttribute("aria-disabled", "true");
    expect(first).not.toHaveAttribute("aria-disabled", "true");

    fireEvent.click(third);
    expect(within(dialog).queryByText(blank.iccid)).not.toBeInTheDocument();
    expect(create(dialog)).toBeDisabled();
  });

  it("reads the roaming lists of the cards", async () => {
    const { calls } = serve();
    const { dialog, onClose } = await open();

    expect(create(dialog)).toBeDisabled();
    await pick(dialog, lab.iccid);
    await pick(dialog, spare.iccid);
    fireEvent.click(create(dialog));

    await waitFor(() => expect(onClose).toHaveBeenCalled());
    expect(calls).toEqual([
      {
        path: "/api/v1/jobs",
        body: { iccids: [lab.iccid, spare.iccid], operation: "oplmn_read" },
      },
    ]);
    expect(screen.getByText("2 jobs queued.")).toBeInTheDocument();
  });

  it("sets an entry", async () => {
    const { calls } = serve();
    const { dialog, onClose } = await open([lab.iccid]);

    choose(dialog, /^Operation/, "Set roaming list entry");
    type(dialog, /^Slot/, "2");
    type(dialog, /^MCC/, "999");
    type(dialog, /^MNC/, "99");
    const checked = within(dialog)
      .getAllByRole("checkbox")
      .filter((c) => (c as HTMLInputElement).checked)
      .map((c) => c.closest("label")?.textContent);
    expect(checked).toEqual(["E-UTRAN WB-S1", "E-UTRAN NB-S1", "NG-RAN"]);
    fireEvent.click(within(dialog).getByRole("checkbox", { name: "GSM" }));
    fireEvent.click(create(dialog));

    await waitFor(() => expect(onClose).toHaveBeenCalled());
    expect(calls[0].body).toEqual({
      iccids: [lab.iccid],
      operation: "oplmn_set",
      slot: 2,
      mcc: "999",
      mnc: "99",
      access: ["eutran_wb_s1", "eutran_nb_s1", "ngran", "gsm"],
    });
    expect(screen.getByText("1 job queued.")).toBeInTheDocument();
  });

  it("offers the other access technologies on demand", async () => {
    const { calls } = serve();
    const { dialog, onClose } = await open([lab.iccid]);

    choose(dialog, /^Operation/, "Set roaming list entry");
    type(dialog, /^Slot/, "1");
    type(dialog, /^MCC/, "001");
    type(dialog, /^MNC/, "01");
    const labels = () =>
      within(dialog)
        .getAllByRole("checkbox")
        .filter((c) => c.closest(".MuiCollapse-hidden") === null)
        .map((c) => c.closest("label")?.textContent);
    expect(labels()).toEqual([
      "UTRAN",
      "E-UTRAN WB-S1",
      "E-UTRAN NB-S1",
      "NG-RAN",
      "GSM",
    ]);

    const more = within(dialog).getByRole("button", {
      name: "More access technologies",
    });
    expect(more).toHaveAttribute("aria-expanded", "false");
    fireEvent.click(more);
    await waitFor(() =>
      expect(labels()).toEqual([
        "UTRAN",
        "E-UTRAN WB-S1",
        "E-UTRAN NB-S1",
        "NG-RAN",
        "GSM",
        "Satellite NG-RAN",
        "Satellite E-UTRAN WB-S1",
        "Satellite E-UTRAN NB-S1",
        "EC-GSM-IoT",
        "GSM COMPACT",
        "cdma2000 HRPD",
        "cdma2000 1xRTT",
      ]),
    );
    fireEvent.click(
      within(dialog).getByRole("checkbox", { name: "Satellite NG-RAN" }),
    );
    fireEvent.click(
      within(dialog).getByRole("checkbox", { name: "cdma2000 1xRTT" }),
    );
    fireEvent.click(within(dialog).getByRole("checkbox", { name: "UTRAN" }));
    fireEvent.click(create(dialog));

    await waitFor(() => expect(onClose).toHaveBeenCalled());
    // The access technologies are sent in the order of the API, whatever the order they were checked in.
    expect(calls[0].body).toMatchObject({
      access: [
        "utran",
        "eutran_wb_s1",
        "eutran_nb_s1",
        "ngran",
        "satellite_ngran",
        "cdma2000_1xrtt",
      ],
    });
  });

  it("clears an entry, with its slot only", async () => {
    const { calls } = serve();
    const { dialog, onClose } = await open([lab.iccid]);

    choose(dialog, /^Operation/, "Set roaming list entry");
    type(dialog, /^MCC/, "999");
    type(dialog, /^MNC/, "99");
    choose(dialog, /^Operation/, "Clear roaming list entry");
    expect(
      within(dialog).queryByRole("textbox", { name: /^MCC/ }),
    ).not.toBeInTheDocument();
    type(dialog, /^Slot/, "51");
    fireEvent.click(create(dialog));

    await waitFor(() => expect(onClose).toHaveBeenCalled());
    expect(calls[0].body).toEqual({
      iccids: [lab.iccid],
      operation: "oplmn_clear",
      slot: 51,
    });
  });

  it.each([
    ["slot 0", { Slot: "0" }],
    ["slot 52", { Slot: "52" }],
    ["no slot", { Slot: "" }],
    ["an MCC of 2 digits", { MCC: "99" }],
    ["an MCC with a letter", { MCC: "99a" }],
    ["an MNC of 1 digit", { MNC: "9" }],
    ["an MNC of 4 digits", { MNC: "9999" }],
    ["no access technology", { access: "none" }],
  ])("disables Create for %s", async (_name, change) => {
    serve();
    const { dialog } = await open([lab.iccid]);

    choose(dialog, /^Operation/, "Set roaming list entry");
    type(dialog, /^Slot/, "2");
    type(dialog, /^MCC/, "999");
    type(dialog, /^MNC/, "99");
    expect(create(dialog)).toBeEnabled();

    for (const [label, value] of Object.entries(change)) {
      if (label === "access") {
        fireEvent.click(
          within(dialog).getByRole("checkbox", { name: "E-UTRAN WB-S1" }),
        );
        fireEvent.click(
          within(dialog).getByRole("checkbox", { name: "E-UTRAN NB-S1" }),
        );
        fireEvent.click(
          within(dialog).getByRole("checkbox", { name: "NG-RAN" }),
        );
        expect(
          within(dialog).getByText("Choose at least one."),
        ).toBeInTheDocument();
      } else {
        type(dialog, new RegExp(`^${label}`), value);
      }
    }
    expect(create(dialog)).toBeDisabled();
  });

  it("disables Create for a clear without a valid slot", async () => {
    serve();
    const { dialog } = await open([lab.iccid]);

    choose(dialog, /^Operation/, "Clear roaming list entry");
    expect(create(dialog)).toBeDisabled();
    type(dialog, /^Slot/, "52");
    expect(within(dialog).getByText("1 to 51.")).toBeInTheDocument();
    expect(create(dialog)).toBeDisabled();
    type(dialog, /^Slot/, "1");
    expect(create(dialog)).toBeEnabled();
  });

  it.each([
    [409, "A card has a job in progress"],
    [409, "Set the SMSC before creating jobs"],
    [400, "card 8949440000001659208 has no MSISDN"],
  ])("keeps the dialog open on a %i", async (status, error) => {
    serve(() => json(status, { error }));
    const { dialog, onClose } = await open([lab.iccid]);

    fireEvent.click(create(dialog));

    expect(
      await within(dialog).findByText(`Could not create the jobs: ${error}`),
    ).toBeInTheDocument();
    expect(onClose).not.toHaveBeenCalled();
  });
});
