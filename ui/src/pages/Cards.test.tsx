import { afterEach, describe, expect, it, vi } from "vitest";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import Cards from "@/pages/Cards";
import type { Card } from "@/queries/cards";
import { json, renderWithClient, stubApi } from "@/test/render";

const IMSI = "001010000000001";

const card: Card = {
  iccid: "8949440000001659208",
  imsi: IMSI,
  msisdn: "+15550000001",
  description: "Lab phone",
  tar: "b00001",
  kvn: 2,
  key_sets: [
    { version: 1, algorithm: "3des", cc_length: 8, counter: 3 },
    { version: 2, algorithm: "aes", cc_length: 4, counter: 0 },
  ],
  created_at: "2026-10-01T12:00:00.000Z",
  updated_at: "2026-10-02T08:30:00.123Z",
};

const cardPath = `/api/v1/cards/${card.iccid}`;

type Call = { method: string; path: string; body?: unknown };

type Route = (url: URL, init?: RequestInit) => Response;

// serve is the cards API over a list of cards that its writes change. A route in overrides answers instead.
const serve = (
  overrides: Record<string, Route> = {},
  initial: Card[] = [card],
) => {
  let cards = initial;
  const calls: Call[] = [];
  const recorded =
    (route: Route): Route =>
    (url, init) => {
      const method = init?.method ?? "GET";
      if (method !== "GET") {
        const text = init?.body === undefined ? undefined : String(init.body);
        calls.push({
          method,
          path: url.pathname + url.search,
          ...(text === undefined
            ? {}
            : {
                body: url.pathname.endsWith("/import")
                  ? text
                  : JSON.parse(text),
              }),
        });
      }
      return (overrides[url.pathname] ?? route)(url, init);
    };

  vi.stubGlobal(
    "fetch",
    vi.fn(
      stubApi({
        "/api/v1/cards": recorded((url, init) => {
          if (init?.method === "POST") {
            return json(201, { result: card });
          }
          return json(200, { result: page(url, cards) });
        }),
        [cardPath]: recorded((_url, init) => {
          if (init?.method === "DELETE") {
            cards = [];
            return json(200, {
              result: { message: "Card deleted successfully" },
            });
          }
          const updated = { ...card, ...JSON.parse(String(init?.body)) };
          cards = [updated];
          return json(200, { result: updated });
        }),
        "/api/v1/smsc": recorded(() =>
          json(200, {
            result: {
              host: "127.0.0.1",
              originator: "+15550000000",
              validity_seconds: 86400,
              password_set: true,
            },
          }),
        ),
        "/api/v1/cards/import": recorded(() =>
          json(200, { result: { created: 2, updated: 1 } }),
        ),
      }),
    ),
  );
  return calls;
};

// page is the page of cards that url asks for, as the server paginates them.
const page = (url: URL, cards: Card[]) => {
  const number = Number(url.searchParams.get("page") ?? 1);
  const perPage = Number(url.searchParams.get("per_page") ?? 25);
  return {
    items: cards.slice((number - 1) * perPage, number * perPage),
    page: number,
    per_page: perPage,
    total_count: cards.length,
  };
};

const fail = (status: number, error: string) => () => json(status, { error });

afterEach(() => {
  vi.unstubAllGlobals();
});

// grid is the grid once the first page of cards has loaded, which the count in the heading tells.
const grid = async () => {
  await screen.findByRole("heading", { name: /^Cards \(\d+\)$/ });
  return screen.getByRole("grid", { name: "Cards" });
};

const rowCells = (iccid: string) =>
  within(
    screen
      .getByRole("grid", { name: "Cards" })
      .querySelector(`[data-id="${iccid}"]`) as HTMLElement,
  )
    .getAllByRole("gridcell")
    .map((c) => c.textContent);

const textbox = (dialog: HTMLElement, name: RegExp) =>
  within(dialog).getByRole("textbox", { name }) as HTMLInputElement;

const type = (dialog: HTMLElement, name: RegExp, value: string) =>
  fireEvent.change(textbox(dialog, name), { target: { value } });

// choose picks an option of a select.
const choose = (dialog: HTMLElement, name: RegExp, option: string) => {
  fireEvent.mouseDown(within(dialog).getByRole("combobox", { name }));
  fireEvent.click(screen.getByRole("option", { name: option }));
};

const openAdd = async () => {
  await grid();
  fireEvent.click(screen.getByRole("button", { name: "Add" }));
  return screen.getByRole("dialog", { name: "Add Card" });
};

const openEdit = async () => {
  await grid();
  fireEvent.click(screen.getByRole("button", { name: `Edit ${card.iccid}` }));
  return screen.getByRole("dialog", { name: "Edit Card" });
};

const openImport = async () => {
  await grid();
  fireEvent.click(screen.getByRole("button", { name: "Import" }));
  return screen.getByRole("dialog", { name: "Import Cards" });
};

// chooseFile chooses a key file, and the algorithm of its key sets unless told none.
const chooseFile = (
  dialog: HTMLElement,
  content: string,
  algorithm: "3DES" | "AES" | null = "3DES",
) => {
  pickFile(dialog, content);
  if (algorithm) choose(dialog, /^Algorithm/, algorithm);
};

const pickFile = (dialog: HTMLElement, content: string) =>
  fireEvent.change(within(dialog).getByLabelText("Choose File"), {
    target: {
      files: [new File([content], "cards.csv", { type: "text/csv" })],
    },
  });

const KEY_16 = "00".repeat(16);

// fillValid fills the add dialog with a valid 3DES card.
const fillValid = (dialog: HTMLElement) => {
  type(dialog, /^ICCID/, card.iccid);
  type(dialog, /^IMSI/, IMSI);
  type(dialog, /^KIc/, KEY_16);
  type(dialog, /^KID/, "11".repeat(16));
};

describe("Cards", () => {
  it("lists the cards, without their keys", async () => {
    serve();
    renderWithClient(<Cards />);

    await grid();
    expect(
      screen.getByRole("heading", { name: "Cards (1)" }),
    ).toBeInTheDocument();
    // IMSI and Updated are hidden by default; the key set in use is marked.
    expect(rowCells(card.iccid).slice(0, 5)).toEqual([
      "8949440000001659208",
      "+15550000001",
      "Lab phone",
      "b00001",
      "1 (3DES, counter 3)2 (AES, CC 4, counter 0) · in use",
    ]);
  });

  it("pages the cards on the server", async () => {
    const many = Array.from({ length: 30 }, (_, i) => ({
      ...card,
      iccid: `89494400000016592${String(i).padStart(2, "0")}`,
    }));
    serve({}, many);
    renderWithClient(<Cards />);

    await grid();
    expect(
      await screen.findByRole("heading", { name: "Cards (30)" }),
    ).toBeInTheDocument();
    expect(screen.getByText("1–25 of 30")).toBeInTheDocument();
    expect(screen.getByText(many[24].iccid)).toBeInTheDocument();
    expect(screen.queryByText(many[25].iccid)).not.toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "Go to next page" }));

    expect(await screen.findByText(many[25].iccid)).toBeInTheDocument();
    expect(screen.getByText("26–30 of 30")).toBeInTheDocument();
    const lists = vi
      .mocked(fetch)
      .mock.calls.map(([url]) => String(url))
      .filter((url) => url.startsWith("/api/v1/cards?"));
    expect(lists).toEqual([
      "/api/v1/cards?page=1&per_page=25",
      "/api/v1/cards?page=2&per_page=25",
    ]);
  });

  it("shows why the cards failed to load", async () => {
    serve({ "/api/v1/cards": fail(500, "database is locked") });
    renderWithClient(<Cards />);

    expect(await screen.findByText("Failed to load cards")).toBeInTheDocument();
    expect(screen.getByText("database is locked")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Retry" })).toBeInTheDocument();
  });
});

describe("Add", () => {
  it("adds a card with its key set", async () => {
    const calls = serve();
    renderWithClient(<Cards />);
    const dialog = await openAdd();
    const submit = within(dialog).getByRole("button", { name: "Add" });

    fillValid(dialog);
    expect(submit).toBeEnabled();
    fireEvent.click(submit);

    await waitFor(() => expect(calls).toHaveLength(1));
    expect(calls[0]).toEqual({
      method: "POST",
      path: "/api/v1/cards",
      body: {
        iccid: card.iccid,
        imsi: IMSI,
        msisdn: "",
        description: "",
        tar: "b00001",
        kvn: 1,
        key_sets: [
          {
            version: 1,
            algorithm: "3des",
            kic: KEY_16,
            kid: "11".repeat(16),
          },
        ],
      },
    });
    await waitFor(() =>
      expect(screen.queryByRole("dialog")).not.toBeInTheDocument(),
    );
    expect(screen.getByText("Card added successfully.")).toBeInTheDocument();
  });

  it("adds an AES key set with its checksum length", async () => {
    const calls = serve();
    renderWithClient(<Cards />);
    const dialog = await openAdd();

    expect(
      within(dialog).getByText(
        "RFM TAR that reaches ADF.USIM (TS 101 220), e.g. b00001; sysmoISIM: b00011",
      ),
    ).toBeInTheDocument();
    fillValid(dialog);
    expect(
      within(dialog).queryByRole("textbox", { name: /^KIK/ }),
    ).not.toBeInTheDocument();
    expect(
      within(dialog).queryByRole("combobox", { name: /^CC Length/ }),
    ).not.toBeInTheDocument();
    choose(dialog, /^Algorithm/, "AES");
    expect(
      within(dialog).getByRole("combobox", { name: /^CC Length/ }),
    ).toHaveTextContent("8");
    choose(dialog, /^CC Length/, "4");
    type(dialog, /^ICCID/, `${card.iccid}F`);
    type(dialog, /^TAR/, "b00020");
    fireEvent.click(within(dialog).getByRole("button", { name: "Add" }));

    await waitFor(() => expect(calls).toHaveLength(1));
    expect(calls[0].body).toEqual({
      iccid: `${card.iccid}F`,
      imsi: IMSI,
      msisdn: "",
      description: "",
      tar: "b00020",
      kvn: 1,
      key_sets: [
        {
          version: 1,
          algorithm: "aes",
          cc_length: 4,
          kic: KEY_16,
          kid: "11".repeat(16),
        },
      ],
    });
  });

  it.each([
    ["ICCID of 17 digits", { ICCID: "89494400000016592" }],
    ["ICCID of 21 digits", { ICCID: "894944000000165920812" }],
    ["ICCID with a letter", { ICCID: "89494400000016592A8" }],
    ["ICCID of 17 digits and an F", { ICCID: "89494400000016592F" }],
    ["empty ICCID", { ICCID: "" }],
    ["IMSI of 5 digits", { IMSI: "00101" }],
    ["IMSI of 16 digits", { IMSI: "0010100000000012" }],
    ["MSISDN without +", { MSISDN: "15550000001" }],
    ["MSISDN of 16 digits", { MSISDN: "+1234567890123456" }],
    ["TAR of 5 characters", { TAR: "b0001" }],
    ["TAR that is not hex", { TAR: "g00001" }],
    ["TAR of the UICC shared file system", { TAR: "b00000" }],
    ["TAR past b0011f", { TAR: "b00120" }],
    ["empty TAR", { TAR: "" }],
    ["version 0", { Version: "0" }],
    ["version 16", { Version: "16" }],
    ["3DES KIc of 8 bytes", { KIc: "00".repeat(8) }],
    ["3DES KIc of 24 bytes", { KIc: "00".repeat(24) }],
    ["3DES KID of 15 bytes", { KID: "11".repeat(15) }],
    ["empty KID", { KID: "" }],
    ["KIc that is not hex", { KIc: `${"00".repeat(15)}zz` }],
    ["AES KIc of 20 bytes", { aes: true, KIc: "00".repeat(20) }],
    ["AES KID of 33 bytes", { aes: true, KID: "11".repeat(33) }],
  ] as [string, Record<string, string | boolean>][])(
    "disables Add for: %s",
    async (_name, { aes, ...fields }) => {
      serve();
      renderWithClient(<Cards />);
      const dialog = await openAdd();
      const submit = within(dialog).getByRole("button", { name: "Add" });

      fillValid(dialog);
      if (aes) choose(dialog, /^Algorithm/, "AES");
      expect(submit).toBeEnabled();

      for (const [label, value] of Object.entries(fields)) {
        type(dialog, new RegExp(`^${label}`), String(value));
      }
      expect(submit).toBeDisabled();
    },
  );

  it.each([
    ["no IMSI", { IMSI: "" }],
    ["an E.164 MSISDN", { MSISDN: "+15550000001" }],
    ["an upper-case TAR", { TAR: "B00001" }],
    ["the TAR b00020", { TAR: "b00020" }],
    ["the TAR b0011f", { TAR: "b0011f" }],
    ["an ICCID with a trailing F", { ICCID: "8949440000001659208F" }],
    ["key set version 15", { Version: "15" }],
  ])("takes %s", async (_name, fields) => {
    serve();
    renderWithClient(<Cards />);
    const dialog = await openAdd();

    fillValid(dialog);
    for (const [label, value] of Object.entries(fields)) {
      type(dialog, new RegExp(`^${label}`), value);
    }
    expect(within(dialog).getByRole("button", { name: "Add" })).toBeEnabled();
  });

  it.each([16, 24, 32])("takes AES keys of %i bytes", async (bytes) => {
    serve();
    renderWithClient(<Cards />);
    const dialog = await openAdd();
    const submit = within(dialog).getByRole("button", { name: "Add" });

    fillValid(dialog);
    choose(dialog, /^Algorithm/, "AES");
    type(dialog, /^KIc/, "00".repeat(bytes));
    type(dialog, /^KID/, "11".repeat(bytes));
    expect(submit).toBeEnabled();
  });

  it("disables a 3DES key that was a valid AES key", async () => {
    serve();
    renderWithClient(<Cards />);
    const dialog = await openAdd();
    const submit = within(dialog).getByRole("button", { name: "Add" });

    fillValid(dialog);
    choose(dialog, /^Algorithm/, "AES");
    type(dialog, /^KIc/, "00".repeat(32));
    expect(submit).toBeEnabled();
    choose(dialog, /^Algorithm/, "3DES");
    expect(submit).toBeDisabled();
  });

  it.each([
    [400, "invalid ICCID"],
    [409, "card 8949440000001659208 exists"],
  ])("keeps the dialog open on a %i", async (status, error) => {
    serve({
      "/api/v1/cards": (url, init) =>
        init?.method === "POST"
          ? json(status, { error })
          : json(200, { result: page(url, [card]) }),
    });
    renderWithClient(<Cards />);
    const dialog = await openAdd();

    fillValid(dialog);
    fireEvent.click(within(dialog).getByRole("button", { name: "Add" }));

    expect(
      await within(dialog).findByText(`Could not add the card: ${error}`),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("dialog", { name: "Add Card" }),
    ).toBeInTheDocument();
  });
});

describe("Edit", () => {
  it("fills the dialog from the card", async () => {
    serve();
    renderWithClient(<Cards />);
    const dialog = await openEdit();

    expect(textbox(dialog, /^ICCID/).value).toBe(card.iccid);
    expect(textbox(dialog, /^ICCID/)).toBeDisabled();
    expect(textbox(dialog, /^IMSI/).value).toBe(IMSI);
    expect(textbox(dialog, /^MSISDN/).value).toBe(card.msisdn);
    expect(textbox(dialog, /^Description/).value).toBe(card.description);
    expect(textbox(dialog, /^TAR/).value).toBe(card.tar);
    expect(
      within(dialog).getByRole("combobox", { name: /^Key Set/ }),
    ).toHaveTextContent("2 (AES)");
  });

  it("offers only the card's key sets", async () => {
    serve();
    renderWithClient(<Cards />);
    const dialog = await openEdit();

    fireEvent.mouseDown(
      within(dialog).getByRole("combobox", { name: /^Key Set/ }),
    );
    expect(
      within(screen.getByRole("listbox"))
        .getAllByRole("option")
        .map((o) => o.textContent),
    ).toEqual(["1 (3DES)", "2 (AES)"]);
  });

  it("sends the card's profile and key set, and refreshes the list", async () => {
    const calls = serve();
    renderWithClient(<Cards />);
    const dialog = await openEdit();

    type(dialog, /^Description/, "Spare");
    type(dialog, /^MSISDN/, "");
    choose(dialog, /^Key Set/, "1 (3DES)");
    fireEvent.click(within(dialog).getByRole("button", { name: "Update" }));

    await waitFor(() => expect(calls).toHaveLength(1));
    expect(calls[0]).toEqual({
      method: "PUT",
      path: cardPath,
      body: {
        imsi: IMSI,
        msisdn: "",
        description: "Spare",
        tar: card.tar,
        kvn: 1,
      },
    });
    await waitFor(() =>
      expect(rowCells(card.iccid).slice(1, 5)).toEqual([
        "—",
        "Spare",
        "b00001",
        "1 (3DES, counter 3) · in use2 (AES, CC 4, counter 0)",
      ]),
    );
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(screen.getByText("Card updated successfully.")).toBeInTheDocument();
  });

  it("stays disabled for an invalid field", async () => {
    serve();
    renderWithClient(<Cards />);
    const dialog = await openEdit();
    const submit = within(dialog).getByRole("button", { name: "Update" });

    type(dialog, /^TAR/, "b0001");
    expect(submit).toBeDisabled();
    type(dialog, /^TAR/, "b00020");
    expect(submit).toBeEnabled();
  });

  it.each([
    [400, "invalid IMSI"],
    [409, "IMSI 001010000000001 belongs to another card"],
  ])("keeps the dialog open on a %i", async (status, error) => {
    serve({ [cardPath]: fail(status, error) });
    renderWithClient(<Cards />);
    const dialog = await openEdit();

    fireEvent.click(within(dialog).getByRole("button", { name: "Update" }));

    expect(
      await within(dialog).findByText(`Could not update the card: ${error}`),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("dialog", { name: "Edit Card" }),
    ).toBeInTheDocument();
  });
});

describe("Delete", () => {
  it("sends nothing when cancelled", async () => {
    const calls = serve();
    renderWithClient(<Cards />);
    await grid();

    fireEvent.click(
      screen.getByRole("button", { name: `Delete ${card.iccid}` }),
    );
    const dialog = screen.getByRole("dialog", { name: "Delete Card" });
    expect(dialog).toHaveTextContent(`Delete ${card.iccid}?`);
    fireEvent.click(within(dialog).getByRole("button", { name: "Cancel" }));

    await waitFor(() =>
      expect(screen.queryByRole("dialog")).not.toBeInTheDocument(),
    );
    expect(calls).toEqual([]);
    expect(rowCells(card.iccid)[0]).toBe(card.iccid);
  });

  it("deletes the card, which leaves the list", async () => {
    const calls = serve();
    renderWithClient(<Cards />);
    await grid();

    fireEvent.click(
      screen.getByRole("button", { name: `Delete ${card.iccid}` }),
    );
    fireEvent.click(
      within(screen.getByRole("dialog", { name: "Delete Card" })).getByRole(
        "button",
        { name: "Delete" },
      ),
    );

    await waitFor(() =>
      expect(calls).toEqual([{ method: "DELETE", path: cardPath }]),
    );
    expect(await screen.findByText("No cards.")).toBeInTheDocument();
    expect(screen.queryByText(card.iccid)).not.toBeInTheDocument();
    expect(screen.getByText("Card deleted successfully.")).toBeInTheDocument();
  });

  it("says why a deletion failed", async () => {
    serve({ [cardPath]: fail(500, "database is locked") });
    renderWithClient(<Cards />);
    await grid();

    fireEvent.click(
      screen.getByRole("button", { name: `Delete ${card.iccid}` }),
    );
    fireEvent.click(
      within(screen.getByRole("dialog", { name: "Delete Card" })).getByRole(
        "button",
        { name: "Delete" },
      ),
    );

    expect(
      await screen.findByText("Failed to delete card: database is locked"),
    ).toBeInTheDocument();
    expect(rowCells(card.iccid)[0]).toBe(card.iccid);
  });
});

describe("Import", () => {
  it("imports a key file as text", async () => {
    const calls = serve();
    renderWithClient(<Cards />);
    const dialog = await openImport();
    const submit = within(dialog).getByRole("button", { name: "Import" });

    expect(submit).toBeDisabled();
    chooseFile(dialog, "ICCID,IMSI,KIC1\n", null);
    expect(within(dialog).getByText("cards.csv")).toBeInTheDocument();
    // A key file does not tell its algorithm.
    expect(submit).toBeDisabled();
    choose(dialog, /^Algorithm/, "3DES");
    expect(
      within(dialog).queryByRole("combobox", { name: /^CC Length/ }),
    ).not.toBeInTheDocument();
    fireEvent.click(submit);

    expect(
      await screen.findByText("Imported 3 cards: 2 created, 1 updated"),
    ).toBeInTheDocument();
    expect(calls).toEqual([
      {
        method: "POST",
        path: "/api/v1/cards/import?algorithm=3des",
        body: "ICCID,IMSI,KIC1\n",
      },
    ]);
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  });

  it.each([
    ["8", "?algorithm=aes&cc_length=8"],
    ["4", "?algorithm=aes&cc_length=4"],
  ])("imports AES key sets with checksums of %s bytes", async (cc, query) => {
    const calls = serve();
    renderWithClient(<Cards />);
    const dialog = await openImport();

    chooseFile(dialog, "ICCID,IMSI,KIC1\n", "AES");
    if (cc !== "8") choose(dialog, /^CC Length/, cc);
    fireEvent.click(within(dialog).getByRole("button", { name: "Import" }));

    await screen.findByText("Imported 3 cards: 2 created, 1 updated");
    expect(calls.map((c) => c.path)).toEqual([`/api/v1/cards/import${query}`]);
  });

  it("says what it imports", async () => {
    serve();
    renderWithClient(<Cards />);
    const dialog = await openImport();

    expect(dialog).toHaveTextContent(
      "Imports ICCID, the KIC/KID key sets and IMSI, if present; ignores Ki, OPc, PINs, ADM keys and KIKs.",
    );
  });

  it("sends text/plain", async () => {
    serve();
    renderWithClient(<Cards />);
    const dialog = await openImport();

    chooseFile(dialog, "ICCID,IMSI\n");
    fireEvent.click(within(dialog).getByRole("button", { name: "Import" }));

    await screen.findByText("Imported 3 cards: 2 created, 1 updated");
    const [, init] = vi
      .mocked(fetch)
      .mock.calls.find(([url]) => String(url).includes("/import?"))!;
    expect(init?.headers).toEqual({ "Content-Type": "text/plain" });
  });

  it("says a single card", async () => {
    serve({
      "/api/v1/cards/import": () =>
        json(200, { result: { created: 0, updated: 1 } }),
    });
    renderWithClient(<Cards />);
    const dialog = await openImport();

    chooseFile(dialog, "ICCID,IMSI\n");
    fireEvent.click(within(dialog).getByRole("button", { name: "Import" }));

    expect(
      await screen.findByText("Imported 1 card: 0 created, 1 updated"),
    ).toBeInTheDocument();
  });

  it("refuses an empty file", async () => {
    const calls = serve();
    renderWithClient(<Cards />);
    const dialog = await openImport();

    chooseFile(dialog, "");
    fireEvent.click(within(dialog).getByRole("button", { name: "Import" }));

    expect(
      await within(dialog).findByText(
        "Could not import the cards: cards.csv is empty.",
      ),
    ).toBeInTheDocument();
    expect(calls).toEqual([]);
  });

  it.each([
    [400, "line 2: ICCID 123 is not 18 to 20 digits"],
    [409, "IMSI 001010000000001 belongs to another card"],
  ])("keeps the dialog open on a %i", async (status, error) => {
    serve({ "/api/v1/cards/import": fail(status, error) });
    renderWithClient(<Cards />);
    const dialog = await openImport();

    chooseFile(dialog, "ICCID,IMSI\n123,001010000000001\n");
    fireEvent.click(within(dialog).getByRole("button", { name: "Import" }));

    expect(
      await within(dialog).findByText(`Could not import the cards: ${error}`),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("dialog", { name: "Import Cards" }),
    ).toBeInTheDocument();
  });
});

describe("New job", () => {
  it("opens the job dialog with the card chosen", async () => {
    serve();
    renderWithClient(<Cards />);
    await grid();

    fireEvent.click(
      screen.getByRole("button", { name: `New job for ${card.iccid}` }),
    );

    const dialog = screen.getByRole("dialog", { name: "New Job" });
    const chip = within(dialog).getByText(card.iccid).closest(".MuiChip-root");
    expect(chip).not.toBeNull();
    expect(
      within(dialog).getByRole("button", { name: "Create" }),
    ).toBeEnabled();
  });

  it("is disabled until the SMSC is set", async () => {
    serve({
      "/api/v1/smsc": () =>
        json(200, {
          result: {
            host: "",
            originator: "",
            validity_seconds: 86400,
            password_set: false,
          },
        }),
    });
    renderWithClient(<Cards />);
    await grid();

    const button = screen.getByRole("button", {
      name: `New job for ${card.iccid}`,
    });
    await waitFor(() => expect(button).toBeDisabled());
    fireEvent.mouseOver(button.parentElement!);
    expect(await screen.findByRole("tooltip")).toHaveTextContent(
      "Set the SMSC first",
    );
  });

  it("is disabled for a card without an MSISDN", async () => {
    serve({}, [{ ...card, msisdn: "" }]);
    renderWithClient(<Cards />);
    await grid();

    expect(
      screen.getByRole("button", { name: `New job for ${card.iccid}` }),
    ).toBeDisabled();
  });
});
