import {
  apiFetch,
  type Page,
  type PageParams,
  withQuery,
} from "@/queries/utils";

export type JobOperation =
  "oplmn_read" | "oplmn_set" | "oplmn_clear" | "imsi_write";

export type JobStatus =
  "queued" | "sent" | "succeeded" | "unconfirmed" | "failed";

// TS 31.102 clause 4.2.5
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

export interface Entry {
  slot: number;
  mcc?: string;
  mnc?: string;
  access?: AccessTechnology[];
  access_raw?: string;
}

export interface Job {
  id: number;
  iccid: string;
  operation: JobOperation;
  slot?: number;
  mcc?: string;
  mnc?: string;
  access?: AccessTechnology[];
  access_raw?: string;
  imsi?: string;
  previous_imsi?: string;
  imsi_read_back?: string;
  tar?: string;
  kvn?: number;
  status: JobStatus;
  step?: number;
  file_size?: number;
  counter?: number;
  warning?: boolean;
  message_id?: string;
  por_status?: string;
  status_word?: string;
  entries?: Entry[];
  command?: string;
  response?: string;
  error?: string;
  created_at: string;
  updated_at: string;
}

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
  | {
      operation: "imsi_write";
      imsi: string;
      previous_imsi: string;
    }
);

export interface ListJobsParams extends PageParams {
  search?: string;
}

const jobsPath = "/api/v1/jobs";

export const listJobs = (params: ListJobsParams = {}): Promise<Page<Job>> =>
  apiFetch<Page<Job>>(
    withQuery(jobsPath, {
      search: params.search,
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
