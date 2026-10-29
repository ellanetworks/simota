import { describe, expect, it } from "vitest";
import { formatDuration } from "@/utils/durations";

describe("formatDuration", () => {
  it("uses the largest whole unit", () => {
    expect(formatDuration(604800)).toBe("7d");
    expect(formatDuration(3600)).toBe("1h");
    expect(formatDuration(900)).toBe("15m");
    expect(formatDuration(90)).toBe("90s");
    expect(formatDuration(5400)).toBe("90m");
  });
});
