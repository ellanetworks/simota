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
  imsi_write: "Write IMSI",
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
    case "imsi_write":
      return `Write IMSI ${job.imsi}`;
    default:
      return operationLabels.oplmn_read;
  }
};

// porCodes are the Response Status Codes of a PoR, by their name in the API, with their label (TS 102 225 table 5,
// TS 31.115 clause 7). The server's job errors use the same words.
const porCodes: Record<string, [code: string, label: string]> = {
  por_ok: ["00", "OK"],
  rc_cc_ds_failed: ["01", "Checksum failed"],
  counter_low: ["02", "Counter too low"],
  counter_high: ["03", "Counter too high"],
  counter_blocked: ["04", "Counter blocked"],
  ciphering_error: ["05", "Ciphering error"],
  unidentified_security_error: ["06", "Security error"],
  insufficient_memory: ["07", "Out of memory"],
  more_time: ["08", "More time needed"],
  tar_unknown: ["09", "Unknown TAR"],
  insufficient_security_level: ["0A", "Security too low"],
  response_in_sms_submit: ["0B", "Answer by SMS"],
  response_in_ussd: ["0C", "Answer by USSD"],
};

// porCode is a PoR status as its code in hex and its label, such as ["02", "Counter too low"]; proprietary_c0 is
// ["C0", "Proprietary C0"].
const porCode = (status: string): [code: string, label: string] => {
  const known = porCodes[status];
  if (known) return known;
  const m = /^(proprietary|reserved)_([0-9a-f]{2})$/.exec(status);
  if (!m) return ["", status];
  const code = m[2].toUpperCase();
  return [
    code,
    `${m[1] === "proprietary" ? "Proprietary" : "Reserved"} ${code}`,
  ];
};

// porLabel is a PoR status in words, such as "Counter too low", or a dash.
export const porLabel = (status?: string): string =>
  status ? porCode(status)[1] : "—";

// porDetail is a PoR status with its code, such as "Counter too low (02)". A proprietary or reserved code is in its
// label already.
export const porDetail = (status?: string): string => {
  if (!status) return "—";
  const [code, label] = porCode(status);
  return code && !label.endsWith(code) ? `${label} (${code})` : label;
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
