import { describe, expect, it } from "vitest";
import { job } from "@/test/fixtures";
import {
  ACCESS_TECHNOLOGIES,
  ALL_ACCESS_TECHNOLOGIES,
  accessLabel,
  accessLabels,
  isMCC,
  isMNC,
  isSlot,
  jobLabel,
  MORE_ACCESS_TECHNOLOGIES,
  plmnLabel,
  porDetail,
  porLabel,
} from "@/utils/jobs";

describe("jobLabel", () => {
  it.each([
    [job(), "Read roaming list"],
    [
      job({
        operation: "oplmn_set",
        slot: 2,
        mcc: "999",
        mnc: "99",
        access: ["eutran_wb_s1", "eutran_nb_s1", "ngran"],
        access_raw: "4c00",
      }),
      "Set slot 2 to 999/99 (E-UTRAN WB-S1, E-UTRAN NB-S1, NG-RAN)",
    ],
    [job({ operation: "oplmn_clear", slot: 2 }), "Clear slot 2"],
    [
      job({
        operation: "imsi_write",
        imsi: "001010000000002",
        previous_imsi: "001010000000001",
      }),
      "Write IMSI 001010000000002",
    ],
  ])("labels %j as %s", (j, label) => {
    expect(jobLabel(j)).toBe(label);
  });
});

describe("accessLabel", () => {
  it("labels every access technology", () => {
    expect(ALL_ACCESS_TECHNOLOGIES.map((a) => accessLabels[a])).toEqual([
      "UTRAN",
      "E-UTRAN WB-S1",
      "E-UTRAN NB-S1",
      "NG-RAN",
      "Satellite NG-RAN",
      "Satellite E-UTRAN WB-S1",
      "Satellite E-UTRAN NB-S1",
      "GSM",
      "EC-GSM-IoT",
      "GSM COMPACT",
      "cdma2000 HRPD",
      "cdma2000 1xRTT",
    ]);
  });

  it("splits them into the common and the others", () => {
    expect(
      [...ACCESS_TECHNOLOGIES, ...MORE_ACCESS_TECHNOLOGIES].sort(),
    ).toEqual([...ALL_ACCESS_TECHNOLOGIES].sort());
  });

  it("adds the identifier in hex", () => {
    expect(accessLabel(["eutran_wb_s1", "eutran_nb_s1", "ngran"], "4800")).toBe(
      "E-UTRAN WB-S1, E-UTRAN NB-S1, NG-RAN (4800)",
    );
    expect(accessLabel(["gsm"])).toBe("GSM");
    expect(accessLabel([], "0000")).toBe("none (0000)");
  });
});

describe("plmnLabel", () => {
  it("shows an unused entry as a dash", () => {
    expect(plmnLabel("001", "01")).toBe("001/01");
    expect(plmnLabel(undefined, undefined)).toBe("—");
  });
});

describe("porLabel", () => {
  it.each([
    ["por_ok", "OK", "OK (00)"],
    ["counter_low", "Counter too low", "Counter too low (02)"],
    [
      "insufficient_security_level",
      "Security too low",
      "Security too low (0A)",
    ],
    ["proprietary_c1", "Proprietary C1", "Proprietary C1"],
    ["reserved_0d", "Reserved 0D", "Reserved 0D"],
    [undefined, "—", "—"],
  ])("labels %s as %s", (status, label, detail) => {
    expect(porLabel(status)).toBe(label);
    expect(porDetail(status)).toBe(detail);
  });
});

describe("isSlot", () => {
  it.each(["1", "2", "51"])("accepts %s", (v) => {
    expect(isSlot(v)).toBe(true);
  });

  it.each(["", "0", "52", "-1", "1.5", "a"])("rejects %j", (v) => {
    expect(isSlot(v)).toBe(false);
  });
});

describe("isMCC", () => {
  it.each(["001", "999"])("accepts %s", (v) => {
    expect(isMCC(v)).toBe(true);
  });

  it.each(["", "01", "0011", "00a"])("rejects %j", (v) => {
    expect(isMCC(v)).toBe(false);
  });
});

describe("isMNC", () => {
  it.each(["01", "001"])("accepts %s", (v) => {
    expect(isMNC(v)).toBe(true);
  });

  it.each(["", "1", "0001", "0a"])("rejects %j", (v) => {
    expect(isMNC(v)).toBe(false);
  });
});
