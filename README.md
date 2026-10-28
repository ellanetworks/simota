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

Defaults: key set 1, triple DES, TAR `B00010` (SIM RFM), ciphering and cryptographic checksum without counter, ciphered PoR with a checksum in the SMS-DELIVER-REPORT.
