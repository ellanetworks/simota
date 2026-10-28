import {
  apiFetch,
  type Page,
  type PageParams,
  withQuery,
} from "@/queries/utils";

export type KeyAlgorithm = "3des" | "aes";

// CCLength is the length of a key set's cryptographic checksums, in bytes. 4 is for AES only.
export type CCLength = 4 | 8;

// KeySet is a card's OTA key set as the server returns it: its keys are write-only.
export interface KeySet {
  version: number;
  algorithm: KeyAlgorithm;
  cc_length: CCLength;
  // counter is the last counter sent with the key set.
  counter: number;
}

// Card is a SIM card that the platform can reach over the air, by SMS to its MSISDN.
export interface Card {
  iccid: string;
  imsi: string;
  // msisdn and description may be empty.
  msisdn: string;
  description: string;
  // tar is the Toolkit Application Reference that commands go to, in hex.
  tar: string;
  // kvn is the version of the key set that secures commands.
  kvn: number;
  key_sets: KeySet[];
  created_at: string;
  updated_at: string;
}

// KeySetParams is a key set with its keys, in hex. cc_length defaults to 8.
export interface KeySetParams {
  version: number;
  algorithm: KeyAlgorithm;
  cc_length?: CCLength;
  kic: string;
  kid: string;
}

export type CardUpdate = Pick<
  Card,
  "imsi" | "msisdn" | "description" | "tar" | "kvn"
>;

export interface CardParams extends CardUpdate {
  iccid: string;
  key_sets: KeySetParams[];
}

export interface ImportResult {
  created: number;
  updated: number;
}

const cardsPath = "/api/v1/cards";

export const listCards = (params: PageParams = {}): Promise<Page<Card>> =>
  apiFetch<Page<Card>>(
    withQuery(cardsPath, { page: params.page, per_page: params.perPage }),
  );

export const createCard = (card: CardParams): Promise<Card> =>
  apiFetch<Card>(cardsPath, { method: "POST", body: card });

export const updateCard = (iccid: string, card: CardUpdate): Promise<Card> =>
  apiFetch<Card>(`${cardsPath}/${encodeURIComponent(iccid)}`, {
    method: "PUT",
    body: card,
  });

export const deleteCard = (iccid: string): Promise<{ message: string }> =>
  apiFetch<{ message: string }>(`${cardsPath}/${encodeURIComponent(iccid)}`, {
    method: "DELETE",
  });

// ImportParams are what a key file does not tell: the algorithm of its key sets, and for AES their checksums' length.
export interface ImportParams {
  algorithm: KeyAlgorithm;
  ccLength?: CCLength;
}

// importCards sends a card vendor's key file, as is.
export const importCards = (
  file: string,
  params: ImportParams,
): Promise<ImportResult> =>
  apiFetch<ImportResult>(
    withQuery(`${cardsPath}/import`, {
      algorithm: params.algorithm,
      cc_length: params.ccLength,
    }),
    { method: "POST", text: file },
  );
