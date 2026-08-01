package udpst

import (
	"encoding/hex"
	"testing"
)

func TestDeriveSessionKeys(t *testing.T) {
	// Reference vector produced with the same KDF OB-UDPST calls:
	//   openssl kdf -keylen 64 -kdfopt mac:HMAC -kdfopt digest:SHA256 \
	//     -kdfopt key:testkey123 -kdfopt salt:UDPSTP \
	//     -kdfopt info:1712345678 KBKDF
	want, err := hex.DecodeString(
		"419bcd26c920c07768bfead96cf2acda1302c304b9b4e61d961204d88a3371eb" +
			"6b14945df66f9ab6d650622813" + "42d538aa98557a27bf631832a1b681dd9531e7")
	if err != nil {
		t.Fatal(err)
	}
	k := deriveSessionKeys("testkey123", 1712345678)
	got := append(append([]byte{}, k.client[:]...), k.server[:]...)
	if hex.EncodeToString(got) != hex.EncodeToString(want) {
		t.Errorf("derived keys\n got %x\nwant %x", got, want)
	}
}

func TestDigestSignVerify(t *testing.T) {
	key := []byte("secret")
	pdu := (&SetupPDU{
		ProtocolVer: ProtocolVer,
		MCCount:     1,
		CmdRequest:  cmdSetupRequest,
		Auth:        authTrailer{AuthMode: authModeControl, AuthUnixTime: 1712345678},
	}).marshal()

	signPDU(key, pdu, setupAuthDigestOff)
	parsed, err := parseSetup(pdu)
	if err != nil {
		t.Fatal(err)
	}
	if !verifyDigest(key, pdu, setupAuthDigestOff, parsed.Auth.AuthDigest) {
		t.Error("valid digest did not verify")
	}
	pdu[10] ^= 0xFF // corrupt a signed byte
	if verifyDigest(key, pdu, setupAuthDigestOff, parsed.Auth.AuthDigest) {
		t.Error("corrupted PDU verified")
	}
}
