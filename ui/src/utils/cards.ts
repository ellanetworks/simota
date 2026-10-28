import type { KeyAlgorithm } from "@/queries/cards";

export const isICCID = (value: string): boolean => /^\d{18,20}$/.test(value);

export const isIMSI = (value: string): boolean => /^\d{6,15}$/.test(value);

// isMSISDN accepts an E.164 number, or none.
export const isMSISDN = (value: string): boolean =>
  value === "" || /^\+[1-9]\d{0,14}$/.test(value);

// isTAR is a Toolkit Application Reference: 3 bytes, in hex.
export const isTAR = (value: string): boolean => /^[0-9A-Fa-f]{6}$/.test(value);

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
