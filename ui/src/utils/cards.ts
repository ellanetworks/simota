import type { KeyAlgorithm } from "@/queries/cards";
import { isE164 } from "@/utils/e164";

// isICCID accepts a trailing F, the padding of an odd number of digits, which the server drops.
export const isICCID = (value: string): boolean =>
  /^\d{18,20}[Ff]?$/.test(value);

export const isIMSI = (value: string): boolean => /^\d{6,15}$/.test(value);

// isMSISDN accepts an E.164 number, or none.
export const isMSISDN = (value: string): boolean =>
  value === "" || isE164(value);

// isTAR is the TAR of an RFM application that reaches ADF.USIM: b00001 or b00020 to b0011f, the ADF range of ETSI TS
// 101 220 table D.1, or b00010 to b0001f, where some cards put it, such as the sysmoISIM-SJA5 at b00011.
export const isTAR = (value: string): boolean =>
  /^[bB]0(00(01|[1-9a-fA-F][0-9a-fA-F])|01[01][0-9a-fA-F])$/.test(value);

// KEY_BYTES are the key lengths of each algorithm: 3DES takes a double-length key, AES any of its three.
export const KEY_BYTES: Record<KeyAlgorithm, number[]> = {
  "3des": [16],
  aes: [16, 24, 32],
};

// isKey is whether value is a key of the algorithm, in hex.
export const isKey = (value: string, algorithm: KeyAlgorithm): boolean =>
  /^[0-9A-Fa-f]*$/.test(value) &&
  value.length % 2 === 0 &&
  KEY_BYTES[algorithm].includes(value.length / 2);

// keyHelp is the length a key of the algorithm must have, for a form.
export const keyHelp = (algorithm: KeyAlgorithm): string =>
  algorithm === "3des"
    ? "32 hex characters (16 bytes)."
    : "32, 48 or 64 hex characters (16, 24 or 32 bytes).";
