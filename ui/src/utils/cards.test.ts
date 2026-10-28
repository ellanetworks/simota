import { describe, expect, it } from "vitest";
import { isICCID, isIMSI, isKey, isMSISDN, isTAR } from "@/utils/cards";

const hex = (bytes: number) => "0123456789abcdef".repeat(8).slice(0, bytes * 2);

describe("isICCID", () => {
  it.each([
    "894944000000165920",
    "8949440000001659208",
    "89494400000016592081",
    "8949440000001659208F",
    "8949440000001659208f",
  ])("accepts %s", (v) => {
    expect(isICCID(v)).toBe(true);
  });

  it.each([
    "",
    "89494400000016592",
    "894944000000165920812",
    "89494400000016592F",
    "8949440000001659208FF",
    "F8949440000001659208",
  ])("rejects %j", (v) => {
    expect(isICCID(v)).toBe(false);
  });
});

describe("isIMSI", () => {
  it.each(["001010", "001010000000001"])("accepts %s", (v) => {
    expect(isIMSI(v)).toBe(true);
  });

  it.each(["", "00101", "0010100000000012", "00101000000000a"])(
    "rejects %j",
    (v) => {
      expect(isIMSI(v)).toBe(false);
    },
  );
});

describe("isMSISDN", () => {
  it.each(["", "+15550000001", "+1", "+123456789012345"])("accepts %j", (v) => {
    expect(isMSISDN(v)).toBe(true);
  });

  it.each([
    "+",
    "15550000001",
    "+05550000001",
    "+1234567890123456",
    "+1555 000",
  ])("rejects %j", (v) => {
    expect(isMSISDN(v)).toBe(false);
  });
});

describe("isTAR", () => {
  it.each([
    "b00001",
    "B00001",
    "b00010",
    "b00011",
    "b0001f",
    "b00020",
    "b000ff",
    "b000FF",
    "b00100",
    "b0011f",
  ])("accepts %s", (v) => {
    expect(isTAR(v)).toBe(true);
  });

  it.each([
    "",
    "b00000",
    "b00002",
    "b0000f",
    "b00120",
    "b00200",
    "b10001",
    "c00001",
    "b0001",
    "b000010",
  ])("rejects %j", (v) => {
    expect(isTAR(v)).toBe(false);
  });
});

describe("isKey", () => {
  it("takes a 16-byte 3DES key", () => {
    expect(isKey(hex(16), "3des")).toBe(true);
    expect(isKey(hex(16).toUpperCase(), "3des")).toBe(true);
    expect(isKey(hex(8), "3des")).toBe(false);
    expect(isKey(hex(24), "3des")).toBe(false);
  });

  it.each([16, 24, 32])("takes a %i-byte AES key", (bytes) => {
    expect(isKey(hex(bytes), "aes")).toBe(true);
  });

  it.each([0, 8, 20, 33])("rejects a %i-byte AES key", (bytes) => {
    expect(isKey(hex(bytes), "aes")).toBe(false);
  });

  it("rejects an odd length and characters that are not hex", () => {
    expect(isKey(hex(16).slice(1), "aes")).toBe(false);
    expect(isKey(`${hex(15)}zz`, "3des")).toBe(false);
  });
});
