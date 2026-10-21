package mesh

import (
	"errors"
	"testing"
	"time"
)

// What this node publishes is counted by mesh and kind, with its size and
// its busiest second: the numbers the delivery team asked for to size RLN's
// rate limits (2026-10-21).
func TestPublishesAreCountedForRLN(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	recordSend("zz-office", SendAnnounce, 600, nil, now)
	recordSend("zz-office", SendAnnounce, 600, nil, now)
	recordSend("zz-office", SendAnnounce, 600, nil, now.Add(time.Second))
	recordSend("zz-office", SendServices, 3000, nil, now)
	recordSend("zz-office", SendAnnounce, 600, errors.New("down"), now)

	counts, peak := SendStats(now.Add(2 * time.Second))
	var ann, svc *SendCount
	for i := range counts {
		switch {
		case counts[i].Mesh == "zz-office" && counts[i].Kind == SendAnnounce:
			ann = &counts[i]
		case counts[i].Mesh == "zz-office" && counts[i].Kind == SendServices:
			svc = &counts[i]
		}
	}
	if ann == nil || ann.Messages != 3 || ann.Bytes != 1800 || ann.Failures != 1 {
		t.Fatalf("announces: %+v", ann)
	}
	// 600 B: not in the 256 or 512 buckets, in every one from 1024 up.
	if ann.Buckets[0] != 0 || ann.Buckets[1] != 0 || ann.Buckets[2] != 3 || ann.Buckets[len(ann.Buckets)-1] != 3 {
		t.Errorf("announce buckets: %v", ann.Buckets)
	}
	if svc == nil || svc.Messages != 1 || svc.Buckets[3] != 0 || svc.Buckets[4] != 1 {
		t.Errorf("services: %+v", svc)
	}
	// Three in the first second (two announces and the services), one in the
	// next; a failed publish is not a sent message.
	if peak < 3 {
		t.Errorf("peak per second = %d, want at least 3", peak)
	}
	// Ten minutes on, that second has aged out.
	if _, later := SendStats(now.Add(peakWindow + 2*time.Second)); later != 0 {
		t.Errorf("peak still %d past the window", later)
	}
}
