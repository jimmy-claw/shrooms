package mesh

import (
	"sort"
	"sync"
	"time"

	"github.com/vpavlin/shrooms/internal/topic"
)

// What this node publishes on the rendezvous plane, by mesh and kind: how
// many messages, how many payload bytes, how big each one is, and the most it
// sent in any one second lately. For the delivery team, who set RLN's
// per-publisher rate limits and asked what an application like this one
// actually sends (2026-10-21).
//
// Counted here, at our own sends, rather than taken from the delivery node:
// that node counts everything it relays, and on a Core node most of that is
// other applications' traffic. Payload bytes are what we hand to the library;
// the wire adds the envelope.

// Kinds of message this node publishes.
const (
	SendAnnounce   = "announce"
	SendRekey      = "rekey"
	SendAdvice     = "relay_advice"
	SendServices   = "services"
	SendRevocation = "revocation"
	SendGrant      = "grant"
	SendInvite     = "invite_answer"
)

// SendSizeBuckets are the histogram's upper bounds, in payload bytes. The
// announce is padded to a fixed size (512 B before sealing), and the
// library's own limit is 150 KiB.
var SendSizeBuckets = []float64{256, 512, 1024, 2048, 4096, 8192, 16384, 65536, 153600}

// peakWindow is how far back the busiest second is looked for.
const peakWindow = 10 * time.Minute

// SendCount is one mesh and kind's tally.
type SendCount struct {
	Mesh, Kind string
	Messages   uint64
	Bytes      uint64
	Failures   uint64
	Buckets    []uint64 // cumulative counts per SendSizeBuckets
}

type sendKey struct{ mesh, kind string }

var sends = struct {
	sync.Mutex
	by     map[sendKey]*SendCount
	second map[int64]uint64 // unix second -> messages sent in it
}{by: map[sendKey]*SendCount{}, second: map[int64]uint64{}}

// recordSend counts one message handed to the delivery node.
func recordSend(mesh, kind string, size int, err error, now time.Time) {
	sends.Lock()
	defer sends.Unlock()
	k := sendKey{mesh, kind}
	c := sends.by[k]
	if c == nil {
		c = &SendCount{Mesh: mesh, Kind: kind, Buckets: make([]uint64, len(SendSizeBuckets))}
		sends.by[k] = c
	}
	if err != nil {
		c.Failures++
		return
	}
	c.Messages++
	c.Bytes += uint64(size)
	for i, b := range SendSizeBuckets {
		if float64(size) <= b {
			c.Buckets[i]++
		}
	}
	s := now.Unix()
	sends.second[s]++
	for t := range sends.second {
		if t < s-int64(peakWindow/time.Second) {
			delete(sends.second, t)
		}
	}
}

// SendStats is every mesh and kind's tally, in a stable order, and the most
// messages this node published in any one second over the last peakWindow.
func SendStats(now time.Time) ([]SendCount, uint64) {
	sends.Lock()
	defer sends.Unlock()
	out := make([]SendCount, 0, len(sends.by))
	for _, c := range sends.by {
		cp := *c
		cp.Buckets = append([]uint64(nil), c.Buckets...)
		out = append(out, cp)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Mesh != out[j].Mesh {
			return out[i].Mesh < out[j].Mesh
		}
		return out[i].Kind < out[j].Kind
	})
	var peak uint64
	from := now.Unix() - int64(peakWindow/time.Second)
	for t, n := range sends.second {
		if t >= from && n > peak {
			peak = n
		}
	}
	return out, peak
}

// send publishes on this mesh's current topic and counts it.
func (m *Mesh) send(kind string, payload []byte, now time.Time) (string, error) {
	id, err := m.node.Send(topic.Current(m.nk, now), payload, true)
	recordSend(m.cfg.MeshLabel, kind, len(payload), err, now)
	return id, err
}
