// MAX_VALIDITY_SECONDS is 63 weeks, the longest validity period of an SMS-SUBMIT (3GPP TS 23.040 clause 9.2.3.12.1).
export const MAX_VALIDITY_SECONDS = 38102400;

// DEFAULT_PORT is the port IANA registers for SMPP.
export const DEFAULT_PORT = 2775;

// MAX_SYSTEM_ID and MAX_PASSWORD are the longest system_id and password of an SMPP bind, without their NULL
// terminator (SMPP 3.4 section 4.1.1, table 4-1).
export const MAX_SYSTEM_ID = 15;
export const MAX_PASSWORD = 8;

const hostnameRE =
  /^[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?(\.[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?)*$/;
const ipv4RE =
  /^(25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)(\.(25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)){3}$/;

// isHost is a host name or an IP address, without a scheme or a port, as the API takes it.
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
  // A dotted quad that is not an IPv4 address is not a host name either.
  if (/^[\d.]+$/.test(value)) return false;
  return value.length <= 253 && hostnameRE.test(value);
};

// portOf is a port from 1 to 65535, or undefined.
export const portOf = (value: string): number | undefined => {
  if (!/^\d{1,5}$/.test(value.trim())) return undefined;
  const port = Number(value);
  return port >= 1 && port <= 65535 ? port : undefined;
};

// isPrintable is printable ASCII, as an SMPP bind carries its system_id and password.
const isPrintable = (value: string): boolean => /^[\x20-\x7e]*$/.test(value);

export const isSystemID = (value: string): boolean =>
  value.length >= 1 && value.length <= MAX_SYSTEM_ID && isPrintable(value);

export const isPassword = (value: string): boolean =>
  value.length >= 1 && value.length <= MAX_PASSWORD && isPrintable(value);

// validitySeconds are the seconds of a validity in hours, if it is one the SMSC takes: from 1 second to 63 weeks.
export const validitySeconds = (hours: string): number | undefined => {
  if (!/^\d+(\.\d+)?$/.test(hours.trim())) return undefined;
  const seconds = Math.round(Number(hours) * 3600);
  return seconds >= 1 && seconds <= MAX_VALIDITY_SECONDS ? seconds : undefined;
};

// validityHours is a validity in hours, as the edit dialog shows it.
export const validityHours = (seconds: number): string =>
  String(Number((seconds / 3600).toFixed(6)));
