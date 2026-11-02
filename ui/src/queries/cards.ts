import {
  apiFetch,
  type Page,
  type PageParams,
  withQuery,
} from "@/queries/utils";

export type KeyAlgorithm = "3des" | "aes";

export type CCLength = 4 | 8;

export interface KeySet {
  version: number;
  algorithm: KeyAlgorithm;
  cc_length: CCLength;
  counter: number;
}

export interface Card {
  iccid: string;
  imsi?: string;
  msisdn: string;
  description: string;
  tar: string;
  kvn: number;
  key_sets: KeySet[];
  created_at: string;
  updated_at: string;
}

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

export interface ImportParams {
  algorithm: KeyAlgorithm;
  ccLength?: CCLength;
}

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
