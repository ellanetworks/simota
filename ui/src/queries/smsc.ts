import { apiFetch } from "@/queries/utils";

// BindStatus is where simota's SMPP session to the SMSC is.
export type BindStatus = "unset" | "connecting" | "bound" | "disconnected";

// BindState is simota's transceiver session to the SMSC, bound again whenever it is lost.
export interface BindState {
  bound: boolean;
  status: BindStatus;
  // since is when the session entered its status.
  since: string;
  // smsc_system_id is the SMSC's system_id, while bound.
  smsc_system_id?: string;
  // last_error is the last failure to connect, to bind, or of the session.
  last_error?: string;
  last_error_at?: string;
}

// SMSC is the Ella SMSC that OTA messages go through over SMPP 3.4. Its password is write-only.
export interface SMSC {
  // host is empty while the SMSC is unset.
  host: string;
  port: number;
  tls: boolean;
  system_id: string;
  originator: string;
  validity_seconds: number;
  password_set: boolean;
  bind: BindState;
}

// SMSCParams set the SMSC. An absent password keeps the one set.
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

// BIND_REFRESH_MS is how often the SMSC page reads the SMSC again, for its bind state.
export const BIND_REFRESH_MS = 5000;

export const smscQuery = { queryKey: ["smsc"], queryFn: getSMSC };
