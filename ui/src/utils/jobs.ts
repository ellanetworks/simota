import type {
  AccessTechnology,
  Job,
  JobOperation,
  JobStatus,
} from "@/queries/jobs";

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

export const accessLabel = (
  access: AccessTechnology[] = [],
  raw?: string,
): string => {
  const labels = access.map((a) => accessLabels[a]).join(", ") || "none";
  return raw ? `${labels} (${raw})` : labels;
};

export const plmnLabel = (mcc?: string, mnc?: string): string =>
  mcc ? `${mcc}/${mnc ?? ""}` : "—";

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

// ETSI TS 102 225 clause 5.2
// TS 31.115 clause 7
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

export const porLabel = (status?: string): string =>
  status ? porCode(status)[1] : "—";

export const porDetail = (status?: string): string => {
  if (!status) return "—";
  const [code, label] = porCode(status);
  return code && !label.endsWith(code) ? `${label} (${code})` : label;
};

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

export const inProgress = (job: Job): boolean =>
  job.status === "queued" || job.status === "sent";
