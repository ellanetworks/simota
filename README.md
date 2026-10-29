# Ella SIM OTA (beta)

<p align="center">
  <img src="docs/images/logo.svg" alt="Ella SIM OTA" width="120"/>
</p>

**Ella SIM OTA** updates the SIM cards of a 4G or 5G network over the air. It sends secured commands to the cards through any SMSC over SMPP, and reports what each card answered.

## Key Features

- **Roaming list over the air**: read the USIM's operator-controlled PLMN list (EF.OPLMNwAcT), and set or clear its entries.
- **IMSI write**: replace a card's IMSI over the air, with a check of the card's current IMSI and a read-back.
- **Any SMSC**: connects to the SMSC as an SMPP 3.4 client, over TLS or not, such as [Ella SMSC](https://github.com/ellanetworks/smsc).
- **Secured commands**: SCP80 (ETSI TS 102 225) with 3DES or AES key sets imported from the card vendor's key file.
- **Proof of Receipt**: every job shows the card's answer, its status word and what it read back.
- **Easy to operate**: a single binary with an embedded database, a web UI and an HTTP API. Install with Docker or Kubernetes.

## Quick Links

- [Website](https://ellanetworks.com/)
- [Ella SMSC](https://github.com/ellanetworks/smsc)
- [Ella Core](https://github.com/ellanetworks/core)
- [Contributing](CONTRIBUTING.md)

## Tutorial: run a first job

1. Build and run Ella SIM OTA, as in [Contributing](CONTRIBUTING.md), and open `http://127.0.0.1:5020`.
2. On the **SMSC** page, set the SMSC: the host and port of its SMPP listener, TLS or not, the `system_id` and password of an ESME account, the originating address, and the validity of OTA messages. The page shows the bind state.
3. On the **Cards** page, import the vendor's key file with its key set algorithm. Then set each card's MSISDN and TAR: the RFM application that reaches ADF.USIM, `b00011` on a sysmoISIM-SJA5.
4. On the **Jobs** page, create a job: read the roaming list, set or clear one of its entries, or write the card's IMSI.

## How-to Guides

### Deploy on Kubernetes

```shell
kubectl create namespace ella
kubectl apply -k k8s
```

The UI and the API are on port `5020` of the `simota` Service.

### Write a card's IMSI

1. Provision the new IMSI in the core first, with the card's keys and MSISDN, so that the card can attach and SMS can reach it. Keep the new IMSI's MNC as long as the old one's.
2. If the card has no IMSI on record, set it first, by editing the card.
3. Create an IMSI write for that one card, giving the IMSI it replaces: the card's IMSI on record.
4. Once the job succeeds, restart the device.

If the job ends **Unconfirmed**, retry it with the same current IMSI.

## Reference

### Configuration

```yaml
logging:
  level: info
db:
  path: simota.db
api:
  address: 127.0.0.1
  port: 5020
```

The UI and the API (`/api/v1`, described at `/api/v1/openapi.yaml`) have no authentication yet: restrict who can reach them.

### Jobs

| Status | Meaning |
|---|---|
| Queued | Waiting to be sent to the SMSC. |
| Sent | With the SMSC, waiting for the card's answer. |
| Succeeded | The card did it, and its answer confirms it. |
| Unconfirmed | A write the card may or may not have done: no answer came back. |
| Failed | Not done. The job tells why. |

## Explanation

### The IMSI write's safeguards

A wrong IMSI cuts the card off the network, and with it off OTA. Requiring the card's current IMSI catches a write to the wrong card, and one based on an outdated view of it. The card's IMSI on record changes only once the write is confirmed.

### When the new IMSI takes effect

The phone keeps using the old IMSI until it restarts. After the restart, a phone may still present its old registration, and a core that still holds it takes the phone for the old subscriber.

## License

Ella SIM OTA is licensed under Business Source (BUSL-1.1).

You need a [Production License](https://ellanetworks.com/pricing) to run Ella SIM OTA in production. For more information, view our [License FAQ](https://ellanetworks.com/license-faq).
