import { apiFetch } from "@/queries/utils";

export type BindStatus = "unset" | "connecting" | "bound" | "disconnected";

export interface BindState {
  status: BindStatus;
  since: string;
  smsc_system_id?: string;
  last_error?: string;
  last_error_at?: string;
}

export interface SMSC {
  host: string;
  port: number;
  tls: boolean;
  system_id: string;
  originator: string;
  validity_seconds: number;
  password_set: boolean;
  bind: BindState;
}

export interface SMSCParams {
  host: string;
  port: number;
  tls: boolean;
  system_id: string;
  password?: string;
  originator: string;
  validity_seconds: number;
}

const smscPath = "/api/v1/smsc";

export const getSMSC = (): Promise<SMSC> => apiFetch<SMSC>(smscPath);

export const updateSMSC = (smsc: SMSCParams): Promise<SMSC> =>
  apiFetch<SMSC>(smscPath, { method: "PUT", body: smsc });

export const BIND_REFRESH_MS = 5000;

export const smscQuery = { queryKey: ["smsc"], queryFn: getSMSC };
