import type {
  AccessTechnology,
  Job,
  JobOperation,
  JobStatus,
} from "@/queries/jobs";

// ACCESS_TECHNOLOGIES are those a phone of a private network uses; MORE_ACCESS_TECHNOLOGIES the others.
export const ACCESS_TECHNOLOGIES: AccessTechnology[] = [
  "utran",
  "eutran_wb_s1",
  "eutran_nb_s1",
  "ngran",
  "gsm",
];

export const MORE_ACCESS_TECHNOLOGIES: AccessTechnology[] = [
  "satellite_ngran",
  "satellite_eutran_wb_s1",
  "satellite_eutran_nb_s1",
  "ec_gsm_iot",
  "gsm_compact",
  "cdma2000_hrpd",
  "cdma2000_1xrtt",
];

// ALL_ACCESS_TECHNOLOGIES are in the order of the API.
export const ALL_ACCESS_TECHNOLOGIES: AccessTechnology[] = [
  "utran",
  "eutran_wb_s1",
  "eutran_nb_s1",
  "ngran",
  "satellite_ngran",
  "satellite_eutran_wb_s1",
  "satellite_eutran_nb_s1",
  "gsm",
  "ec_gsm_iot",
  "gsm_compact",
  "cdma2000_hrpd",
  "cdma2000_1xrtt",
];

export const DEFAULT_ACCESS: AccessTechnology[] = [
  "eutran_wb_s1",
  "eutran_nb_s1",
  "ngran",
];

export const accessLabels: Record<AccessTechnology, string> = {
  utran: "UTRAN",
  eutran_wb_s1: "E-UTRAN WB-S1",
  eutran_nb_s1: "E-UTRAN NB-S1",
  ngran: "NG-RAN",
  satellite_ngran: "Satellite NG-RAN",
  satellite_eutran_wb_s1: "Satellite E-UTRAN WB-S1",
  satellite_eutran_nb_s1: "Satellite E-UTRAN NB-S1",
  gsm: "GSM",
  ec_gsm_iot: "EC-GSM-IoT",
  gsm_compact: "GSM COMPACT",
  cdma2000_hrpd: "cdma2000 HRPD",
  cdma2000_1xrtt: "cdma2000 1xRTT",
};

export const operationLabels: Record<JobOperation, string> = {
  oplmn_read: "Read roaming list",
  oplmn_set: "Set roaming list entry",
  oplmn_clear: "Clear roaming list entry",
};

// accessLabel is a list of access technologies, with their identifier in hex when it is known, such as
// "E-UTRAN WB-S1, NG-RAN (4800)".
export const accessLabel = (
  access: AccessTechnology[] = [],
  raw?: string,
): string => {
  const labels = access.map((a) => accessLabels[a]).join(", ") || "none";
  return raw ? `${labels} (${raw})` : labels;
};

// plmnLabel is a PLMN as MCC/MNC, such as 999/99.
export const plmnLabel = (mcc?: string, mnc?: string): string =>
  mcc ? `${mcc}/${mnc ?? ""}` : "—";

// jobLabel is what a job does, such as "Set slot 2 to 999/99 (E-UTRAN, NG-RAN)".
export const jobLabel = (job: Job): string => {
  switch (job.operation) {
    case "oplmn_set":
      return `Set slot ${job.slot} to ${plmnLabel(job.mcc, job.mnc)} (${accessLabel(job.access)})`;
    case "oplmn_clear":
      return `Clear slot ${job.slot}`;
    default:
      return operationLabels.oplmn_read;
  }
};

// MAX_SLOT is the last entry a job may write, as the server allows.
export const MAX_SLOT = 51;

export const isSlot = (value: string): boolean =>
  /^\d+$/.test(value) && Number(value) >= 1 && Number(value) <= MAX_SLOT;

export const isMCC = (value: string): boolean => /^\d{3}$/.test(value);

export const isMNC = (value: string): boolean => /^\d{2,3}$/.test(value);

export const statusHelp: Record<JobStatus, string> = {
  queued: "Queued: waiting to send its next command to the SMSC.",
  sent: "Sent: the SMSC is delivering its command to the card.",
  succeeded:
    "Succeeded: written on the card; the phone uses it after its next USIM initialisation or REFRESH.",
  unconfirmed:
    "Unconfirmed: the write may have taken effect, without a PoR to confirm it.",
  failed: "Failed: the card did not do it.",
};

// inProgress tells whether a job still waits for the card.
export const inProgress = (job: Job): boolean =>
  job.status === "queued" || job.status === "sent";
