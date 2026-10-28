# simota

A minimal SIM OTA platform. It keeps SIM cards and their SCP80 key sets, and runs Remote File Management jobs on the USIM's EF.OPLMNwAcT as secured packets (ETSI TS 102 225, 3GPP TS 31.115) sent over Ella SMSC as an SMPP 3.4 client, decoding the cards' Proof of Receipt.

## Run

```shell
(cd ui && npm ci && npm run build)
go run ./cmd/simota -config simota.yaml
```

simota builds against Ella SMSC's SMPP 3.4 library, `github.com/ellanetworks/smsc/smpp`, from a sibling checkout of Ella SMSC: `go.mod` replaces it with `../smsc/smpp`.

The UI and the API (`/api/v1`, described at `/api/v1/openapi.yaml`) listen on `127.0.0.1:5020`. They have no authentication yet, so keep them on the loopback interface.

## Set up

1. **SMSC.** On the SMSC page, or `PUT /api/v1/smsc`: the host and port (2775 by default) of Ella SMSC's SMPP listener, TLS or not, the `system_id` and password of an ESME account, the originating address, and the validity of OTA messages. simota keeps one transceiver session bound to it, binds again whenever it is lost, and shows the bind state on the same page. Each command goes as a `submit_sm` asking for the SME Manual/User Acknowledgement, which carries the card's PoR, and for a delivery receipt; Ella SMSC pushes both back as `deliver_sm`.
2. **Cards.** Import the vendor's key file with its key set algorithm (`POST /api/v1/cards/import?algorithm=3des`), then set each card's MSISDN and TAR: the RFM application that reaches ADF.USIM, `b00011` on a sysmoISIM-SJA5.
3. **Jobs.** Read the roaming list, or set or clear one of its entries.

## Security

Commands are ciphered with a cryptographic checksum and a counter that must be higher than the card's; PoRs are ciphered with a checksum, in the SMS-DELIVER-REPORT. Each key set's counter is kept in the database.
