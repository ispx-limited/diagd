// Package tr143 implements the network server side of Broadband Forum TR-143
// throughput and echo diagnostics: the HTTP download/upload endpoints and the
// UDP Echo Plus responder that CPE-initiated tests run against.
//
// TR-143 measures on the CPE; the server's job is to answer correctly and
// never be the bottleneck. All test payloads are generated, nothing is stored.
package tr143

import (
	"crypto/rand"
	"net/netip"
)

// payloadBlock is the shared source block for generated downloads.
// Incompressible content keeps compressing middleboxes from inflating
// measured throughput.
var payloadBlock = func() []byte {
	b := make([]byte, 1<<20)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return b
}()

// AllowFunc reports whether a peer address may use a diagnostic endpoint.
// A nil AllowFunc permits all peers.
type AllowFunc func(netip.Addr) bool
