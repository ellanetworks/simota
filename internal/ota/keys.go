package ota

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/des"
	"crypto/subtle"
	"errors"
	"fmt"
)

// Algorithm is the algorithm of a key set, coded in the low nibble of KIc and KID (TS 102 225 clauses 5.1.2, 5.1.3).
type Algorithm uint8

const (
	// TripleDES is triple DES in outer CBC mode with two keys: CBC for KIc, CBC-MAC for KID.
	TripleDES Algorithm = 0x5
	// AES is AES in CBC mode for KIc, and in CMAC mode for KID.
	AES Algorithm = 0x2
)

// ccLength is the length of a cryptographic checksum. Both algorithms give 8 octets in TS 102 225 clause 5.1.3.1.
const ccLength = 8

var ErrIntegrity = errors.New("cryptographic checksum mismatch")

// Keyset is a key set of a card's security domain for SCP80: its Key Version Number, its algorithm, and its ciphering
// (KIc) and integrity (KID) keys.
type Keyset struct {
	Version   uint8
	Algorithm Algorithm
	KIc       []byte
	KID       []byte
}

func (k Keyset) validate() error {
	if k.Version < 1 || k.Version > 15 {
		return fmt.Errorf("key version %d not in 1-15", k.Version)
	}

	switch k.Algorithm {
	case TripleDES:
		if len(k.KIc) != 16 || len(k.KID) != 16 {
			return errors.New("triple DES keys must be 16 octets")
		}
	case AES:
		if !validAESKey(k.KIc) || !validAESKey(k.KID) {
			return errors.New("AES keys must be 16, 24 or 32 octets")
		}
	default:
		return fmt.Errorf("unsupported algorithm %#x", byte(k.Algorithm))
	}

	return nil
}

func validAESKey(k []byte) bool {
	return len(k) == 16 || len(k) == 24 || len(k) == 32
}

// blockSize is the block size that ciphering pads to.
func (k Keyset) blockSize() int {
	if k.Algorithm == AES {
		return aes.BlockSize
	}

	return des.BlockSize
}

func (k Keyset) block(key []byte) (cipher.Block, error) {
	if k.Algorithm == AES {
		return aes.NewCipher(key)
	}

	// Two-key triple DES is K1, K2, K1.
	return des.NewTripleDESCipher(append(append([]byte{}, key...), key[:8]...))
}

// encrypt ciphers data, a multiple of the block size, in CBC mode with a zero IV.
func (k Keyset) encrypt(data []byte) ([]byte, error) {
	b, err := k.block(k.KIc)
	if err != nil {
		return nil, err
	}

	out := make([]byte, len(data))
	cipher.NewCBCEncrypter(b, make([]byte, b.BlockSize())).CryptBlocks(out, data)

	return out, nil
}

func (k Keyset) decrypt(data []byte) ([]byte, error) {
	b, err := k.block(k.KIc)
	if err != nil {
		return nil, err
	}

	if len(data)%b.BlockSize() != 0 {
		return nil, fmt.Errorf("ciphered data of %d octets is not a multiple of the block size", len(data))
	}

	out := make([]byte, len(data))
	cipher.NewCBCDecrypter(b, make([]byte, b.BlockSize())).CryptBlocks(out, data)

	return out, nil
}

// checksum is the cryptographic checksum of data: the last block of a CBC-MAC over data padded with zeros for triple
// DES, or AES-CMAC (RFC 4493) truncated to its first 8 octets.
func (k Keyset) checksum(data []byte) ([]byte, error) {
	b, err := k.block(k.KID)
	if err != nil {
		return nil, err
	}

	if k.Algorithm == AES {
		return cmac(b, data)[:ccLength], nil
	}

	padded := pad(data, b.BlockSize())
	out := make([]byte, len(padded))
	cipher.NewCBCEncrypter(b, make([]byte, b.BlockSize())).CryptBlocks(out, padded)

	return out[len(out)-ccLength:], nil
}

func (k Keyset) verify(data, cc []byte) error {
	want, err := k.checksum(data)
	if err != nil {
		return err
	}

	if subtle.ConstantTimeCompare(want, cc) != 1 {
		return ErrIntegrity
	}

	return nil
}

// pad appends zeros to data up to a multiple of size.
func pad(data []byte, size int) []byte {
	if r := len(data) % size; r != 0 {
		return append(append([]byte{}, data...), make([]byte, size-r)...)
	}

	return data
}

// cmac is the AES-CMAC of data (RFC 4493).
func cmac(b cipher.Block, data []byte) []byte {
	const rb = 0x87

	l := make([]byte, aes.BlockSize)
	b.Encrypt(l, l)

	k1 := shiftLeft(l)
	if l[0]&0x80 != 0 {
		k1[aes.BlockSize-1] ^= rb
	}

	k2 := shiftLeft(k1)
	if k1[0]&0x80 != 0 {
		k2[aes.BlockSize-1] ^= rb
	}

	n := (len(data) + aes.BlockSize - 1) / aes.BlockSize
	complete := n > 0 && len(data)%aes.BlockSize == 0

	if n == 0 {
		n = 1
	}

	last := make([]byte, aes.BlockSize)
	copy(last, data[(n-1)*aes.BlockSize:])

	if complete {
		subtle.XORBytes(last, last, k1)
	} else {
		last[len(data)-(n-1)*aes.BlockSize] = 0x80
		subtle.XORBytes(last, last, k2)
	}

	x := make([]byte, aes.BlockSize)
	for i := range n - 1 {
		subtle.XORBytes(x, x, data[i*aes.BlockSize:(i+1)*aes.BlockSize])
		b.Encrypt(x, x)
	}

	subtle.XORBytes(x, x, last)
	b.Encrypt(x, x)

	return x
}

func shiftLeft(in []byte) []byte {
	out := make([]byte, len(in))

	for i := range in {
		out[i] = in[i] << 1
		if i+1 < len(in) {
			out[i] |= in[i+1] >> 7
		}
	}

	return out
}
