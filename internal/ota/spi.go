package ota

// ETSI TS 102 225 clause 5.1.1
// ETSI TS 102 225 clause 5.1.2
// TS 31.115 clause 4.2
const SPI uint16 = 0x1619

// ETSI TS 102 225 clause 5.1.1
const (
	spiIntegrity    = 0x0300
	spiCC           = 0x0200
	spiCiphering    = 0x0400
	spiPoRIntegrity = 0x000c
	spiPoRCC        = 0x0008
	spiPoRCiphering = 0x0010
)
