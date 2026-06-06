package agentping

import (
	"crypto/rand"
	"encoding/hex"
	"regexp"
	"sync"
	"time"
)

var (
	apiKeyRegionRE = regexp.MustCompile(`^apk_([a-z]{2})_[0-9a-f]{32}$`)
	idPatternRE    = regexp.MustCompile(`^[a-z]+_[a-z]{2}_[0-9a-f]{32}$`)

	idMu    sync.Mutex
	lastMs  int64
	lastSeq uint16
)

// uuid7Hex returns a UUIDv7 as 32 lowercase hex characters with the dashes
// stripped. The layout is 48 bits of millisecond timestamp, 4 version bits, 12
// bits of monotonic counter, 2 variant bits, then 62 bits of random data.
func uuid7Hex() string {
	idMu.Lock()
	ms := time.Now().UnixMilli()
	if ms <= lastMs {
		ms = lastMs
		lastSeq++
	} else {
		lastMs = ms
		lastSeq = 0
	}
	seq := lastSeq & 0x0FFF
	idMu.Unlock()

	var b [16]byte
	b[0] = byte(ms >> 40)
	b[1] = byte(ms >> 32)
	b[2] = byte(ms >> 24)
	b[3] = byte(ms >> 16)
	b[4] = byte(ms >> 8)
	b[5] = byte(ms)
	b[6] = 0x70 | byte((seq>>8)&0x0F)
	b[7] = byte(seq)

	if _, err := rand.Read(b[8:]); err != nil {
		// crypto/rand should not fail; fall back to a time-derived tail to
		// keep the SDK contract of never crashing user code.
		nanos := uint64(time.Now().UnixNano())
		for i := 8; i < 16; i++ {
			b[i] = byte(nanos >> ((i - 8) * 8))
		}
	}
	b[8] = 0x80 | (b[8] & 0x3F)

	return hex.EncodeToString(b[:])
}

// extractRegion parses the two-letter region from an apk_<region>_<32 hex>
// key. Returns "eu" if the key is empty or malformed; validation happens at
// the API boundary.
func extractRegion(apiKey string) string {
	if apiKey == "" {
		return "eu"
	}
	m := apiKeyRegionRE.FindStringSubmatch(apiKey)
	if len(m) != 2 {
		return "eu"
	}
	return m[1]
}

// newID builds a prefixed identifier of the form <prefix>_<region>_<32 hex>.
func newID(prefix, region string) string {
	return prefix + "_" + region + "_" + uuid7Hex()
}

// isValidID reports whether the value matches the global ID pattern.
func isValidID(value string) bool {
	return idPatternRE.MatchString(value)
}
