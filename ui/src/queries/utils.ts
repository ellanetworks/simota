export type ApiErrorKind = "network" | "http";

export class ApiError extends Error {
  readonly kind: ApiErrorKind;
  readonly status?: number;
  // detail is the technical line under the message, such as "HTTP 500 Internal Server Error".
  readonly detail?: string;

  constructor(
    kind: ApiErrorKind,
    message: string,
    status?: number,
    detail?: string,
  ) {
    super(message);
    this.name = "ApiError";
    this.kind = kind;
    this.status = status;
    this.detail = detail;
  }
}

export interface Page<T> {
  items: T[];
  page: number;
  per_page: number;
  total_count: number;
}

export interface PageParams {
  page?: number;
  perPage?: number;
}

interface ApiFetchOptions {
  method?: string;
  // body is sent as JSON.
  body?: unknown;
  // text is sent as is, as text/plain, such as an uploaded file.
  text?: string;
}

export async function apiFetch<T>(
  url: string,
  options: ApiFetchOptions = {},
): Promise<T> {
  const { method = "GET", body, text } = options;

  const init: RequestInit = { method };
  if (text !== undefined) {
    init.headers = { "Content-Type": "text/plain" };
    init.body = text;
  } else if (body !== undefined) {
    init.headers = { "Content-Type": "application/json" };
    init.body = JSON.stringify(body);
  }

  let response: Response;
  try {
    response = await fetch(url, init);
  } catch {
    throw new ApiError("network", "Cannot reach the server.");
  }

  if (response.status === 204) return undefined as T;

  let data: { result?: T; error?: string } | undefined;
  try {
    data = await response.json();
  } catch {
    data = undefined;
  }

  if (!response.ok) {
    const message =
      data?.error || response.statusText || `HTTP ${response.status}`;
    const detail =
      `HTTP ${response.status} ${response.statusText}`.trimEnd() +
      ` · ${method} ${url}`;
    throw new ApiError("http", message, response.status, detail);
  }

  if (data === undefined) {
    throw new ApiError(
      "http",
      "The server returned an invalid response.",
      response.status,
    );
  }

  return data.result as T;
}

export function withQuery(
  path: string,
  params: Record<string, string | number | undefined>,
): string {
  const query = new URLSearchParams();
  for (const [key, value] of Object.entries(params)) {
    if (value !== undefined && value !== "") {
      query.set(key, String(value));
    }
  }

  const qs = query.toString();
  return qs ? `${path}?${qs}` : path;
}
