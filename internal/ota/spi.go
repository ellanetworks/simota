// Package ota codes the secured packets of SIM OTA over SMS point to point: the Command Packet that the Sending Entity
// sends in an SMS-DELIVER, and the Response Packet (PoR) that the card answers with (ETSI TS 102 225, 3GPP TS 31.115).
package ota

// SPI is the Security Parameter Indicator of every Command Packet that simota sends (TS 102 225 clause 5.1.1, TS
// 31.115 clause 4.2):
//
//   - first octet '16': a cryptographic checksum (b2-b1 = 10), ciphering (b3 = 1), and a counter that must be higher
//     than the card's (b5-b4 = 10), which protects against replay and which AES requires (TS 102 225 clause 5.1.2);
//   - second octet '19': a PoR always (b2-b1 = 01), with a cryptographic checksum (b4-b3 = 10), ciphered (b5 = 1), in
//     the SMS-DELIVER-REPORT (b6 = 0).
const SPI uint16 = 0x1619

// The bits of an SPI that the coding of the packets depends on (TS 102 225 clause 5.1.1).
const (
	spiIntegrity    = 0x0300
	spiCC           = 0x0200
	spiCiphering    = 0x0400
	spiPoRIntegrity = 0x000c
	spiPoRCC        = 0x0008
	spiPoRCiphering = 0x0010
)
