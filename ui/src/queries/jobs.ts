import {
  apiFetch,
  type Page,
  type PageParams,
  withQuery,
} from "@/queries/utils";

// JobOperation is what a job does to a card's EF.OPLMNwAcT, its operator-controlled roaming list.
export type JobOperation = "oplmn_read" | "oplmn_set" | "oplmn_clear";

export type JobStatus =
  "queued" | "sent" | "succeeded" | "unconfirmed" | "failed";

// AccessTechnology is an access technology of a PLMN selector entry (3GPP TS 31.102 clause 4.2.5).
export type AccessTechnology =
  | "utran"
  | "eutran_wb_s1"
  | "eutran_nb_s1"
  | "ngran"
  | "satellite_ngran"
  | "satellite_eutran_wb_s1"
  | "satellite_eutran_nb_s1"
  | "gsm"
  | "ec_gsm_iot"
  | "gsm_compact"
  | "cdma2000_hrpd"
  | "cdma2000_1xrtt";

// Entry is an entry of the roaming list. An unused one has no PLMN.
export interface Entry {
  slot: number;
  mcc?: string;
  mnc?: string;
  access?: AccessTechnology[];
  // access_raw is the Access Technology Identifier, in hex.
  access_raw?: string;
}

export interface Job {
  id: number;
  iccid: string;
  operation: JobOperation;
  // slot, mcc, mnc and access are those of a set; a clear has a slot only.
  slot?: number;
  mcc?: string;
  mnc?: string;
  access?: AccessTechnology[];
  access_raw?: string;
  // tar and kvn are the TAR and key set version the job's commands use.
  tar?: string;
  kvn?: number;
  status: JobStatus;
  // step is the index of the job's current command; a job runs as one or more.
  step?: number;
  // file_size is the size of EF.OPLMNwAcT, once a read has it.
  file_size?: number;
  // counter is the counter of the current command.
  counter?: number;
  // warning tells that the last status word completed with a warning.
  warning?: boolean;
  message_id?: string;
  por_status?: string;
  status_word?: string;
  // entries are what the job read: the roaming list for a read, the slot read back for a set or a clear.
  entries?: Entry[];
  // command and response are the TP-UDH and TP-UD of the Command and Response Packets, in hex.
  command?: string;
  response?: string;
  error?: string;
  created_at: string;
  updated_at: string;
}

// JobParams queue an operation on each card. The server rejects the fields an operation does not take.
export type JobParams = { iccids: string[] } & (
  | { operation: "oplmn_read" }
  | { operation: "oplmn_clear"; slot: number }
  | {
      operation: "oplmn_set";
      slot: number;
      mcc: string;
      mnc: string;
      access: AccessTechnology[];
    }
);

export interface ListJobsParams extends PageParams {
  iccid?: string;
}

const jobsPath = "/api/v1/jobs";

export const listJobs = (params: ListJobsParams = {}): Promise<Page<Job>> =>
  apiFetch<Page<Job>>(
    withQuery(jobsPath, {
      iccid: params.iccid,
      page: params.page,
      per_page: params.perPage,
    }),
  );

export const getJob = (id: number): Promise<Job> =>
  apiFetch<Job>(`${jobsPath}/${id}`);

export const createJobs = (params: JobParams): Promise<Job[]> =>
  apiFetch<{ items: Job[] }>(jobsPath, { method: "POST", body: params }).then(
    (r) => r.items,
  );
