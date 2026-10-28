import type { Card } from "@/queries/cards";
import type { Job } from "@/queries/jobs";

export const card = (overrides: Partial<Card> = {}): Card => ({
  iccid: "8949440000001659208",
  imsi: "001010000000001",
  msisdn: "+15550000001",
  description: "Lab phone",
  tar: "b00001",
  kvn: 1,
  key_sets: [{ version: 1, algorithm: "3des", cc_length: 8, counter: 0 }],
  created_at: "2026-10-01T12:00:00.000Z",
  updated_at: "2026-10-02T08:30:00.123Z",
  ...overrides,
});

export const job = (overrides: Partial<Job> = {}): Job => ({
  id: 1,
  iccid: "8949440000001659208",
  operation: "oplmn_read",
  status: "queued",
  created_at: "2026-10-28T17:34:53.123Z",
  updated_at: "2026-10-28T17:34:54.456Z",
  ...overrides,
});

// page is the page of items that url asks for, as the server paginates them.
export const page = <T>(url: URL, items: T[]) => {
  const number = Number(url.searchParams.get("page") ?? 1);
  const perPage = Number(url.searchParams.get("per_page") ?? 25);
  return {
    items: items.slice((number - 1) * perPage, number * perPage),
    page: number,
    per_page: perPage,
    total_count: items.length,
  };
};
