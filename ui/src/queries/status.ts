import { apiFetch } from "@/queries/utils";

export interface Status {
  version: string;
}

export const getStatus = (): Promise<Status> =>
  apiFetch<Status>("/api/v1/status");
