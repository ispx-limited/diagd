package udpst

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/binary"
	"strconv"
)

// authTimeWindow is the allowed clock skew for authenticated setup requests,
// in seconds (AUTH_TIME_WINDOW).
const authTimeWindow = 5

// Authentication modes.
const (
	authModeNone    = 0
	authModeControl = 1 // HMAC on control PDUs only
)

// sessionKeys holds the per-session directional keys derived for protocol
// version 20. Client-to-server PDUs are signed with the client key,
// server-to-client PDUs with the server key.
type sessionKeys struct {
	client [32]byte
	server [32]byte
}

// deriveSessionKeys derives the directional session keys from the shared
// secret and the Setup Request's authUnixTime, matching OB-UDPST's use of
// the OpenSSL KBKDF: NIST SP 800-108 counter-mode KDF with HMAC-SHA-256,
// label "UDPSTP", context the decimal string of authUnixTime, a separator
// byte, and the output length field included (64 bytes total output).
func deriveSessionKeys(sharedKey string, authUnixTime uint32) sessionKeys {
	label := []byte("UDPSTP")
	context := []byte(strconv.FormatUint(uint64(authUnixTime), 10))

	const outBytes = 64
	var out []byte
	var counter uint32 = 1
	for len(out) < outBytes {
		mac := hmac.New(sha256.New, []byte(sharedKey))
		var buf [4]byte
		binary.BigEndian.PutUint32(buf[:], counter)
		mac.Write(buf[:])
		mac.Write(label)
		mac.Write([]byte{0}) // separator
		mac.Write(context)
		binary.BigEndian.PutUint32(buf[:], outBytes*8)
		mac.Write(buf[:])
		out = mac.Sum(out)
		counter++
	}

	var k sessionKeys
	copy(k.client[:], out[:32])
	copy(k.server[:], out[32:64])
	return k
}

// computeDigest returns the HMAC-SHA-256 digest of a control PDU with its
// digest field zeroed. The checksum bytes are always zero on our PDUs, so no
// separate masking is needed for them.
func computeDigest(key []byte, pdu []byte, digestOff int) [32]byte {
	masked := make([]byte, len(pdu))
	copy(masked, pdu)
	for i := digestOff; i < digestOff+32; i++ {
		masked[i] = 0
	}
	mac := hmac.New(sha256.New, key)
	mac.Write(masked)
	var d [32]byte
	mac.Sum(d[:0])
	return d
}

// verifyDigest checks a received control PDU's digest in constant time.
func verifyDigest(key []byte, pdu []byte, digestOff int, received [32]byte) bool {
	want := computeDigest(key, pdu, digestOff)
	return subtle.ConstantTimeCompare(want[:], received[:]) == 1
}

// signPDU writes the digest into the PDU in place.
func signPDU(key []byte, pdu []byte, digestOff int) {
	d := computeDigest(key, pdu, digestOff)
	copy(pdu[digestOff:digestOff+32], d[:])
}
