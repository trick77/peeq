package llm

import (
	"crypto/rand"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// chatUserAgent is the User-Agent sent to the chat endpoint. Go's default
// "Go-http-client/1.1" identifies the caller as a bot; the upstream is happier
// with the client string an ordinary OpenAI-compatible SDK sends. Ported from
// ../loom's llm client along with the session headers below.
//
// This and the session headers are MiMo-era and no longer load-bearing: Z.ai
// neither requires them nor issues session ids of this shape. They are kept
// because they are inert and cost nothing, not because anything depends on
// them — a future cleanup can drop the lot without a replacement.
const chatUserAgent = "opencode/1.18.11 ai-sdk/openai-compatible/3.0.20 ai-sdk/provider-utils/5.0.18 runtime/bun/1.3.14"

// Session ids mirror the shape the upstream issues, e.g.
//
//	ses_ 0367809bfffe ejtHKm95o6rU4mQ
//	│    └─12 hex────┘ └─14 base62───┘
//	│    timestamp+counter   random
//	prefix
//
// The 12 hex digits are the bitwise inversion of (millis << 12 | counter),
// truncated to 48 bits and written big-endian — a 12-bit per-process counter
// keeps ids minted in the same millisecond distinct, and the inversion is what
// gives upstream ids their characteristic trailing f's.
const (
	sessionIDPrefix   = "ses_"
	sessionIDRandomLn = 14
	sessionIDAlphabet = "0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"
)

// sessionCacheLimit bounds the video→session map. Ids only matter while a video
// is being processed, so the oldest entries are dropped once the cache is full
// rather than letting a long-running process accumulate one entry per video it
// has ever summarized.
const sessionCacheLimit = 4096

var sessionCounter atomic.Uint64

// processSessionID is the fallback affinity id for calls whose CallInfo carries
// no video — ad-hoc calls and tests. It is minted once per process, so those
// calls still pin to a single upstream node for the process lifetime.
var processSessionID = newSessionID()

var sessionCache = struct {
	sync.Mutex
	byVideo map[string]string
	order   []string
}{byVideo: map[string]string{}}

// chatSessionID returns the session/affinity id to send for a call. It is keyed
// on the video the call belongs to (CallInfo.VideoID), so the many calls one
// video costs — a summary call per transcript chunk, then the reduce — all pin
// to the same upstream node. Calls without a video fall back to the per-process
// id.
func chatSessionID(videoID string) string {
	if videoID == "" {
		return processSessionID
	}
	sessionCache.Lock()
	defer sessionCache.Unlock()
	if id, ok := sessionCache.byVideo[videoID]; ok {
		return id
	}
	id := newSessionID()
	if len(sessionCache.order) >= sessionCacheLimit {
		delete(sessionCache.byVideo, sessionCache.order[0])
		sessionCache.order = sessionCache.order[1:]
	}
	sessionCache.byVideo[videoID] = id
	sessionCache.order = append(sessionCache.order, videoID)
	return id
}

// newSessionID mints "ses_" + 12 hex (timestamp+counter) + 14 base62 random.
func newSessionID() string {
	millis := uint64(time.Now().UnixMilli())
	counter := sessionCounter.Add(1) & 0xFFF // 12 bits
	stamp := ^(millis<<12 | counter) & 0xFFFFFFFFFFFF
	return fmt.Sprintf("%s%012x%s", sessionIDPrefix, stamp, randomBase62(sessionIDRandomLn))
}

// randomBase62 draws n characters from the base62 alphabet. Rejection sampling
// keeps the draw unbiased; if the system entropy source fails the id degrades to
// the alphabet's first character rather than failing a model call, since this is
// an opaque routing token and not a secret.
func randomBase62(n int) string {
	const limit = 256 - (256 % len(sessionIDAlphabet)) // largest unbiased byte range
	out := make([]byte, 0, n)
	buf := make([]byte, n)
	for len(out) < n {
		if _, err := rand.Read(buf); err != nil {
			for len(out) < n {
				out = append(out, sessionIDAlphabet[0])
			}
			break
		}
		for _, b := range buf {
			if int(b) >= limit {
				continue
			}
			out = append(out, sessionIDAlphabet[int(b)%len(sessionIDAlphabet)])
			if len(out) == n {
				break
			}
		}
	}
	return string(out)
}
