// SMPP 3.4 section 7.1.1.2
export const MAX_VALIDITY_SECONDS = 8553600;

export const DEFAULT_PORT = 2775;

// SMPP 3.4 section 4.1.1 table 4-1
export const MAX_SYSTEM_ID = 15;
export const MAX_PASSWORD = 8;

const hostnameRE =
  /^[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?(\.[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?)*$/;
const ipv4RE =
  /^(25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)(\.(25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)){3}$/;

export const isHost = (value: string): boolean => {
  if (ipv4RE.test(value)) return true;
  if (value.includes(":")) {
    if (value.includes("%") || value.includes("[")) return false;
    try {
      return new URL(`http://[${value}]/`).hostname !== "";
    } catch {
      return false;
    }
  }
  if (/^[\d.]+$/.test(value)) return false;
  return value.length <= 253 && hostnameRE.test(value);
};

export const portOf = (value: string): number | undefined => {
  if (!/^\d{1,5}$/.test(value.trim())) return undefined;
  const port = Number(value);
  return port >= 1 && port <= 65535 ? port : undefined;
};

const isPrintable = (value: string): boolean => /^[\x20-\x7e]*$/.test(value);

export const isSystemID = (value: string): boolean =>
  value.length >= 1 && value.length <= MAX_SYSTEM_ID && isPrintable(value);

export const isPassword = (value: string): boolean =>
  value.length >= 1 && value.length <= MAX_PASSWORD && isPrintable(value);

export const validitySeconds = (hours: string): number | undefined => {
  if (!/^\d+(\.\d+)?$/.test(hours.trim())) return undefined;
  const seconds = Math.round(Number(hours) * 3600);
  return seconds >= 1 && seconds <= MAX_VALIDITY_SECONDS ? seconds : undefined;
};

export const validityHours = (seconds: number): string =>
  String(Number((seconds / 3600).toFixed(6)));
