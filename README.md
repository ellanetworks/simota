# simota

A minimal SIM OTA platform: it sends Remote File Management scripts to SIM cards as secured packets (ETSI TS 102 225, 3GPP TS 31.115) over Ella SMSC, and decodes the cards' Proof of Receipt.

## Usage

```shell
export SIMOTA_SMSC_URL=http://<smsc>:5010
export SIMOTA_SMSC_TOKEN=ellasmsc_...   # a Submitter token
go run ./cmd/simota -to +15551230001 -kic <KIC1> -kid <KID1> oplmn read
go run ./cmd/simota -to +15551230001 -kic <KIC1> -kid <KID1> oplmn set 2 999 99
go run ./cmd/simota -to +15551230001 -kic <KIC1> -kid <KID1> oplmn clear 2
```

With a card stored in the database, `-card <iccid>` replaces `-to`, `-kic`, `-kid`, `-kvn` and `-tar`:

```shell
go run ./cmd/simota -card 8949440000001659208 oplmn read
```

## Web UI and API

```shell
(cd ui && npm ci && npm run build)
go run ./cmd/simota serve -config simota.yaml
```

The UI and the API (`/api/v1`, described at `/api/v1/openapi.yaml`) listen on `127.0.0.1:5020`. They have no authentication yet, so keep them on the loopback interface.

## Defaults

Key set 1, triple DES, TAR `B00010` (SIM RFM), ciphering and cryptographic checksum without counter, ciphered PoR with a checksum in the SMS-DELIVER-REPORT.
