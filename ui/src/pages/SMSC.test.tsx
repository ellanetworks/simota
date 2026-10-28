import { afterEach, describe, expect, it, vi } from "vitest";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import SMSC from "@/pages/SMSC";
import type { SMSC as SMSCSettings } from "@/queries/smsc";
import { json, renderWithClient, stubApi } from "@/test/render";
import { formatDateTime } from "@/utils/dates";

const PASSWORD = "s3cret!";
const SINCE = "2026-10-28T12:00:00Z";

const unset: SMSCSettings = {
  host: "",
  port: 2775,
  tls: false,
  system_id: "",
  originator: "",
  validity_seconds: 86400,
  password_set: false,
  bind: { status: "unset", since: SINCE },
};

const set: SMSCSettings = {
  host: "127.0.0.1",
  port: 2775,
  tls: false,
  system_id: "simota",
  originator: "+15550000000",
  validity_seconds: 86400,
  password_set: true,
  bind: {
    status: "bound",
    since: SINCE,
    smsc_system_id: "EllaSMSC",
  },
};

// serve is the SMSC API: a PUT answers what it set, without its password, as the server does.
const serve = (
  smsc: SMSCSettings,
  put: (body: Record<string, unknown>) => Response = (body) =>
    json(200, {
      result: {
        host: body.host,
        port: body.port,
        tls: body.tls,
        system_id: body.system_id,
        originator: body.originator,
        validity_seconds: body.validity_seconds,
        password_set: true,
        bind: { status: "connecting", since: SINCE },
      },
    }),
) => {
  const puts: unknown[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(
      stubApi({
        "/api/v1/smsc": (_url, init) => {
          if (init?.method === "PUT") {
            const body = JSON.parse(String(init.body));
            puts.push(body);
            return put(body);
          }
          return json(200, { result: smsc });
        },
      }),
    ),
  );
  return puts;
};

afterEach(() => {
  vi.unstubAllGlobals();
});

const settings = () =>
  Object.fromEntries(
    within(screen.getByRole("table", { name: "SMSC settings" }))
      .getAllByRole("row")
      .map((r) => {
        const [label, value] = within(r).getAllByRole("cell");
        return [label.textContent, value.textContent];
      }),
  );

const openEdit = async () => {
  // The Edit button waits for the settings.
  const button = await screen.findByRole("button", { name: "Edit" });
  await waitFor(() => expect(button).toBeEnabled());
  fireEvent.click(button);
  return screen.getByRole("dialog", { name: "Edit SMSC" });
};

const field = (dialog: HTMLElement, name: RegExp) =>
  within(dialog).getByLabelText(name) as HTMLInputElement;

const type = (dialog: HTMLElement, name: RegExp, value: string) =>
  fireEvent.change(field(dialog, name), { target: { value } });

const update = (dialog: HTMLElement) =>
  within(dialog).getByRole("button", { name: "Update" });

describe("SMSC", () => {
  it("shows an unset SMSC, with one Edit button", async () => {
    serve(unset);
    renderWithClient(<SMSC />);

    await screen.findAllByText("Not set");
    expect(screen.getAllByRole("button", { name: /^Edit/ })).toHaveLength(1);
    expect(settings()).toEqual({
      Host: "Not set",
      Port: "2775",
      TLS: "Off",
      "System ID": "Not set",
      Password: "Not set",
      Originator: "Not set",
      Validity: "1d",
      Bind: "unset",
      "Last error": "None",
    });
  });

  it("shows a set SMSC and its bind, without its password", async () => {
    serve(set);
    renderWithClient(<SMSC />);

    await screen.findByText("127.0.0.1");
    expect(settings()).toEqual({
      Host: "127.0.0.1",
      Port: "2775",
      TLS: "Off",
      "System ID": "simota",
      Password: "Set",
      Originator: "+15550000000",
      Validity: "1d",
      Bind: `boundsince ${formatDateTime(SINCE)}`,
      "Last error": "None",
    });
    // No banner: the bind state is in the table.
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();

    const dialog = await openEdit();
    expect(field(dialog, /^Host/).value).toBe(set.host);
    expect(field(dialog, /^Port/).value).toBe("2775");
    expect(field(dialog, /^TLS/).checked).toBe(false);
    expect(field(dialog, /^System ID/).value).toBe("simota");
    expect(field(dialog, /^Originator/).value).toBe(set.originator);
    expect(field(dialog, /^Validity/).value).toBe("24");
    expect(field(dialog, /^Password/).value).toBe("");
    expect(field(dialog, /^Password/)).toHaveAttribute("type", "password");
    expect(
      within(dialog).getByText("Leave empty to keep the current password"),
    ).toBeInTheDocument();
  });

  it("shows why the session is down", async () => {
    serve({
      ...set,
      bind: {
        status: "disconnected",
        since: SINCE,
        last_error:
          "smpp: bind refused: smpp: bind_transceiver_resp ESME_RBINDFAIL",
        last_error_at: SINCE,
      },
    });
    renderWithClient(<SMSC />);

    await screen.findByText("disconnected");
    expect(settings()["Last error"]).toBe(
      `smpp: bind refused: smpp: bind_transceiver_resp ESME_RBINDFAIL (${formatDateTime(SINCE)})`,
    );
  });

  it("needs a password the first time, and sends it", async () => {
    const puts = serve(unset);
    renderWithClient(<SMSC />);
    const dialog = await openEdit();

    type(dialog, /^Host/, "smsc.example.com");
    type(dialog, /^Port/, "3550");
    fireEvent.click(field(dialog, /^TLS/));
    type(dialog, /^System ID/, "simota");
    type(dialog, /^Originator/, "+15550000000");
    expect(field(dialog, /^Password/)).toBeRequired();
    expect(update(dialog)).toBeDisabled();
    type(dialog, /^Password/, PASSWORD);
    type(dialog, /^Validity/, "1.5");
    fireEvent.click(update(dialog));

    await waitFor(() =>
      expect(screen.queryByRole("dialog")).not.toBeInTheDocument(),
    );
    expect(puts).toEqual([
      {
        host: "smsc.example.com",
        port: 3550,
        tls: true,
        system_id: "simota",
        password: PASSWORD,
        originator: "+15550000000",
        validity_seconds: 5400,
      },
    ]);
    expect(screen.getByText("SMSC updated successfully.")).toBeInTheDocument();
    expect(settings()).toMatchObject({
      Host: "smsc.example.com",
      Port: "3550",
      TLS: "On",
      "System ID": "simota",
      Password: "Set",
      Validity: "90m",
    });
    expect(settings().Bind).toContain("connecting");
    expect(document.body).not.toHaveTextContent(PASSWORD);
  });

  it("keeps the password set when none is typed", async () => {
    const puts = serve(set);
    renderWithClient(<SMSC />);
    const dialog = await openEdit();

    expect(field(dialog, /^Password/)).not.toBeRequired();
    type(dialog, /^Host/, "2001:db8::1");
    fireEvent.click(update(dialog));

    await waitFor(() => expect(puts).toHaveLength(1));
    expect(puts[0]).toEqual({
      host: "2001:db8::1",
      port: 2775,
      tls: false,
      system_id: "simota",
      originator: "+15550000000",
      validity_seconds: 86400,
    });
  });

  it.each([
    ["a host with a scheme", /^Host/, "http://127.0.0.1"],
    ["a host with a port", /^Host/, "127.0.0.1:2775"],
    ["no host", /^Host/, ""],
    ["port 0", /^Port/, "0"],
    ["port 65536", /^Port/, "65536"],
    ["no port", /^Port/, ""],
    ["no system ID", /^System ID/, ""],
    ["a system ID of 16 characters", /^System ID/, "0123456789abcdef"],
    ["a password of 9 characters", /^Password/, "123456789"],
    ["a password that is not ASCII", /^Password/, "sécret"],
    ["an originator without +", /^Originator/, "15550000000"],
    ["no originator", /^Originator/, ""],
    ["a validity of 0", /^Validity/, "0"],
    ["a validity under a second", /^Validity/, "0.0001"],
    ["a validity over 99 days", /^Validity/, "2377"],
    ["a validity that is not a number", /^Validity/, "1d"],
  ])("disables Update for %s", async (_name, label, value) => {
    serve(set);
    renderWithClient(<SMSC />);
    const dialog = await openEdit();

    expect(update(dialog)).toBeEnabled();
    type(dialog, label, value);
    expect(update(dialog)).toBeDisabled();
  });

  it("keeps the dialog open on a 400", async () => {
    serve(set, () => json(400, { error: "Invalid system_id" }));
    renderWithClient(<SMSC />);
    const dialog = await openEdit();

    type(dialog, /^Password/, PASSWORD);
    fireEvent.click(update(dialog));

    expect(
      await within(dialog).findByText(
        "Could not update the SMSC: Invalid system_id",
      ),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("dialog", { name: "Edit SMSC" }),
    ).toBeInTheDocument();
  });
});
