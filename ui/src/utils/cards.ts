import type { KeyAlgorithm } from "@/queries/cards";
import { isE164 } from "@/utils/e164";

export const isICCID = (value: string): boolean =>
  /^\d{18,20}[Ff]?$/.test(value);

export const isIMSI = (value: string): boolean =>
  value === "" || /^\d{6,15}$/.test(value);

export const isMSISDN = (value: string): boolean =>
  value === "" || isE164(value);

// ETSI TS 101 220 annex D table D.1
export const isTAR = (value: string): boolean =>
  /^[bB]0(00(01|[1-9a-fA-F][0-9a-fA-F])|01[01][0-9a-fA-F])$/.test(value);

export const KEY_BYTES: Record<KeyAlgorithm, number[]> = {
  "3des": [16],
  aes: [16, 24, 32],
};

export const isKey = (value: string, algorithm: KeyAlgorithm): boolean =>
  /^[0-9A-Fa-f]*$/.test(value) &&
  value.length % 2 === 0 &&
  KEY_BYTES[algorithm].includes(value.length / 2);

export const keyHelp = (algorithm: KeyAlgorithm): string =>
  algorithm === "3des"
    ? "32 hex characters (16 bytes)."
    : "32, 48 or 64 hex characters (16, 24 or 32 bytes).";
