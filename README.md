# simota

A minimal SIM OTA platform. It keeps SIM cards and their SCP80 key sets, and runs Remote File Management jobs on the USIM's EF.OPLMNwAcT as secured packets (ETSI TS 102 225, 3GPP TS 31.115) sent through any SMSC, such as Ella SMSC, as an SMPP 3.4 client, decoding the cards' Proof of Receipt.

## Run

```shell
(cd ui && npm ci && npm run build)
go run ./cmd/simota -config simota.yaml
```

simota builds against the SMPP 3.4 library of Ella SMSC, `github.com/ellanetworks/smsc/smpp`, from a sibling checkout of `ellanetworks/smsc`: `go.mod` replaces it with `../smsc/smpp`, so `go install` and CI jobs that check out simota alone cannot build it yet. Once the library is merged into `ellanetworks/smsc`, the `replace` will be replaced by a pseudo-version pin of that commit.

The UI and the API (`/api/v1`, described at `/api/v1/openapi.yaml`) listen on `127.0.0.1:5020`. They have no authentication yet, so keep them on the loopback interface.

## Set up

1. **SMSC.** On the SMSC page, or `PUT /api/v1/smsc`: the host and port (2775 by default) of the SMSC's SMPP listener, TLS or not, the `system_id` and password of an ESME account, the originating address, and the validity of OTA messages, at most 99 days. simota keeps one transceiver session bound to it, binds again whenever it is lost, and shows the bind state on the same page.

   Each command goes as a `submit_sm` of standard fields only, asking for the SME Manual/User Acknowledgement and for a final delivery receipt (`registered_delivery` 0x09); an SMSC that does not support the acknowledgement ignores it. The SMSC pushes the card's answer and the receipt back as `deliver_sm`:

   - the **card's answer** is recognised by its content: user data, in `short_message` or `message_payload`, that is a Response Packet behind the header `02 71 00`, with or without UDHI (TS 31.115 clause 4.1), from the card's MSISDN, as a normal message or an SME Manual/User Acknowledgement. It is matched to its job by TAR and counter, which the PoR copies from the command (TS 102 225 clause 5.2); `receipted_message_id` is not needed. An unsecured error PoR has a zero counter, so it is taken only when one job of that MSISDN and TAR is in flight. A PoR that matches no job's last command is acknowledged and ignored.
   - the **delivery receipt** is matched by `receipted_message_id`, or by the `id:` of its text without it, and closes the job without a PoR; a PoR that comes later, before or after the receipt, or after the job timed out, still gives the job its outcome.
   - A job that gets neither within its validity plus 6 hours, counted only while bound, is closed without a PoR. A `deliver_sm` whose outcome cannot be stored is answered `ESME_RX_T_APPN`, so that the SMSC sends it again.
   - After `ESME_RTHROTTLED` or `ESME_RMSGQFUL`, simota backs off from 10 seconds to 5 minutes; a command the SMSC fails to take 10 times fails. A command whose submission cannot be recorded is sent again with a higher counter, which is harmless to the card: commands are idempotent, and the first one's PoR, of the spent counter, is ignored.
2. **Cards.** Import the vendor's key file with its key set algorithm (`POST /api/v1/cards/import?algorithm=3des`), then set each card's MSISDN and TAR: the RFM application that reaches ADF.USIM, `b00011` on a sysmoISIM-SJA5.
3. **Jobs.** Read the roaming list, or set or clear one of its entries.

## Security

Commands are ciphered with a cryptographic checksum and a counter that must be higher than the card's; PoRs are ciphered with a checksum, in the SMS-DELIVER-REPORT. Each key set's counter is kept in the database.
