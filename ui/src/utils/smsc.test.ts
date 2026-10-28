import { describe, expect, it } from "vitest";
import {
  isHost,
  isPassword,
  isSystemID,
  portOf,
  validityHours,
  validitySeconds,
} from "@/utils/smsc";

describe("isHost", () => {
  it.each([
    "127.0.0.1",
    "10.0.0.254",
    "smsc",
    "smsc-1.example.com",
    "2001:db8::1",
    "::1",
  ])("accepts %s", (v) => {
    expect(isHost(v)).toBe(true);
  });

  it.each([
    "",
    "http://127.0.0.1",
    "127.0.0.1:2775",
    "256.0.0.1",
    "1.2.3",
    "[2001:db8::1]",
    "fe80::1%eth0",
    "-smsc",
    "smsc..example",
    "smsc example",
  ])("rejects %j", (v) => {
    expect(isHost(v)).toBe(false);
  });
});

describe("portOf", () => {
  it.each([
    ["2775", 2775],
    ["1", 1],
    ["65535", 65535],
  ])("reads %s", (v, port) => {
    expect(portOf(v)).toBe(port);
  });

  it.each(["", "0", "65536", "-1", "27a", "1.5"])("rejects %j", (v) => {
    expect(portOf(v)).toBeUndefined();
  });
});

describe("isSystemID and isPassword", () => {
  it("take printable ASCII up to the SMPP lengths", () => {
    expect(isSystemID("simota")).toBe(true);
    expect(isSystemID("012345678901234")).toBe(true);
    expect(isSystemID("0123456789012345")).toBe(false);
    expect(isSystemID("")).toBe(false);
    expect(isPassword("s3cret!")).toBe(true);
    expect(isPassword("12345678")).toBe(true);
    expect(isPassword("123456789")).toBe(false);
    expect(isPassword("sécret")).toBe(false);
    expect(isPassword("")).toBe(false);
  });
});

describe("validitySeconds", () => {
  it.each([
    ["24", 86400],
    ["1", 3600],
    ["1.5", 5400],
    ["0.0003", 1],
    ["10584", 38102400],
  ])("reads %s hours as %i seconds", (hours, seconds) => {
    expect(validitySeconds(hours)).toBe(seconds);
  });

  it.each(["", "0", "0.0001", "10584.001", "-1", "1h", "abc", "1e3"])(
    "rejects %j",
    (v) => {
      expect(validitySeconds(v)).toBeUndefined();
    },
  );
});

describe("validityHours", () => {
  it("shows seconds as hours", () => {
    expect(validityHours(86400)).toBe("24");
    expect(validityHours(5400)).toBe("1.5");
    expect(validityHours(38102400)).toBe("10584");
    expect(validitySeconds(validityHours(1))).toBe(1);
  });
});
