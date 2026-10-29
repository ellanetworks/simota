# simota

A minimal SIM OTA platform. It keeps SIM cards and their SCP80 key sets, and runs Remote File Management jobs on the USIM's EF.OPLMNwAcT and EF.IMSI as secured packets (ETSI TS 102 225, 3GPP TS 31.115) sent through any SMSC, such as Ella SMSC, as an SMPP 3.4 client, decoding the cards' Proof of Receipt.

## Run

```shell
(cd ui && npm ci && npm run build)
go run ./cmd/simota -config simota.yaml
```

simota uses the SMPP 3.4 library of Ella SMSC, `github.com/ellanetworks/smsc/smpp`, pinned in `go.mod` to a commit of `ellanetworks/smsc`.

The UI and the API (`/api/v1`, described at `/api/v1/openapi.yaml`) listen on `127.0.0.1:5020`. They have no authentication yet, so keep them on the loopback interface.

## Set up

1. **SMSC.** On the SMSC page, or `PUT /api/v1/smsc`: the host and port (2775 by default) of the SMSC's SMPP listener, TLS or not, the `system_id` and password of an ESME account, the originating address, and the validity of OTA messages, at most 99 days. simota keeps one transceiver session bound to it, binds again whenever it is lost, and shows the bind state on the same page.

   Each command goes as a `submit_sm` of standard fields only, asking for the SME Manual/User Acknowledgement and for a final delivery receipt (`registered_delivery` 0x09); an SMSC that does not support the acknowledgement ignores it. The SMSC pushes the card's answer and the receipt back as `deliver_sm`:

   - the **card's answer** is recognised by its content: user data, in `short_message` or `message_payload`, that is a Response Packet behind the header `02 71 00`, with or without UDHI (TS 31.115 clause 4.1), from the card's MSISDN, as a normal message or an SME Manual/User Acknowledgement. It is matched to its job by TAR and counter, which the PoR copies from the command (TS 102 225 clause 5.2); `receipted_message_id` is not needed. An unsecured error PoR has a zero counter, so it is taken only when one job of that MSISDN and TAR is in flight. A PoR that matches no job's last command is acknowledged and ignored.
   - the **delivery receipt** is matched by `receipted_message_id`, or by the `id:` of its text without it, and closes the job without a PoR. A PoR that comes later, after the receipt or after the job timed out, still gives the job its outcome when it matches the job's last command: a set, a clear or an IMSI write goes from `unconfirmed` to `succeeded` or `failed`, and the PoR of a read's last command finishes or fails it. A read closed without the PoR to an earlier step stays `failed`; run it again.
   - A job that gets neither within the validity plus 6 hours of being sent, counted only while bound, is closed without a PoR. A `deliver_sm` whose outcome cannot be stored is answered `ESME_RX_T_APPN`, so that the SMSC sends it again.
   - After `ESME_RTHROTTLED` or `ESME_RMSGQFUL`, simota backs off from 10 seconds to 5 minutes; a command the SMSC fails to take 10 times fails. The attempts are counted on the job, so a restart does not reset them. A command whose submission cannot be recorded is sent again with a higher counter, which is harmless to the card: commands are idempotent, and the first one's PoR, of the spent counter, is ignored.
2. **Cards.** Import the vendor's key file with its key set algorithm (`POST /api/v1/cards/import?algorithm=3des`), then set each card's MSISDN and TAR: the RFM application that reaches ADF.USIM, `b00011` on a sysmoISIM-SJA5. OTA reaches a card by its MSISDN. Its IMSI on record is optional, but an IMSI write needs it.
3. **Jobs.** Read the roaming list, set or clear one of its entries, or write the card's IMSI.

## IMSI write

An IMSI write replaces the IMSI in the card's EF.IMSI (3GPP TS 31.102 clause 4.2.2) in one secured packet: it writes all 9 octets of the file, coded as in TS 24.008 clause 10.5.1.4, and reads them back. It succeeds only when the card reads back exactly what was written.

A wrong IMSI cuts the card off the network, and with it off OTA. So:

- A write takes one card, and the IMSI it replaces (`previous_imsi`), which must be the card's IMSI on record. This catches a write to the wrong card, and one based on an outdated view of it. A card without an IMSI on record needs it set first, by editing the card.
- Provision the new IMSI in the core first, with the card's keys and MSISDN, so that the card can attach and SMS can reach it. The new IMSI's MNC must be as long as the old one's: the card's EF.AD gives the MNC length (TS 31.102 clause 4.2.18), and simota does not change it.

The card's IMSI on record becomes the new IMSI only when the write succeeds, including when a late PoR confirms it. A failed write leaves the IMSI on record unchanged, and so does an `unconfirmed` one, since the card may still hold the old IMSI. The job keeps both IMSIs. A retry names the same `previous_imsi` and is harmless if the first write did take effect.

**The new IMSI takes effect when the device restarts.** simota sends no REFRESH, so the phone keeps using the old IMSI until its next USIM initialisation. simota also does not reset the card's location files (EF.LOCI, EF.PSLOCI, EF.EPSLOCI, EF.5GS3GPPLOCI), which TS 31.102 annex A advises updating when EF.IMSI changes. After the restart, a phone may therefore present the old registration's temporary identity (GUTI, 5G-GUTI), and a core that still holds that registration takes it for the old subscriber.

## Security

Every command has the SPI `16 19`: ciphered, with a cryptographic checksum and a counter that must be higher than the card's; it asks for a PoR always, ciphered with a checksum, in the SMS-DELIVER-REPORT. Each key set's counter is kept in the database.
