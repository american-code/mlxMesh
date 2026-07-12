package coordinator

import (
	"fmt"
	"hash/fnv"
	"sort"
)

// hashRingReplicas is how many virtual points each real member gets on the
// ring — spreads a member's share of the keyspace across many positions so
// one member joining/leaving the candidate set only remaps roughly the
// fraction of keyspace it owned, not the whole ring.
//
// 32 was the original value but proved too low for SMALL fleets specifically:
// with only 2-3 candidates, 32 replicas/member still leaves real per-instantiation
// skew (simulation: a random 2-member ring is >70/30-skewed roughly 20% of the
// time, occasionally much worse), which showed up as flakiness in
// TestIntegrationPrefixAffinityKeepsRepeatedPromptsOnTheSameNode — a handful of
// distinct prompts could all land on one node purely from bad luck in the ring's
// random point placement, not a routing bug. 512 was chosen empirically
// (simulation: 0/5000 trials still fully skewed toward one member of a 2-member
// ring after 200 distinct probes, vs 2/5000 at 256) — diminishing returns past
// this. Ring construction cost scales linearly with members*replicas and is
// still microseconds even at fleet sizes in the hundreds, so there's no real
// cost to raising this — it only reduces variance.
const hashRingReplicas = 512

// hashRing is a minimal consistent-hash ring: the same key always maps to
// the same member as long as that member is present in the ring, and
// removing/adding a member only remaps the keys it owned — never the whole
// keyspace, unlike a plain `hash(key) % len(members)` scheme. Built fresh
// per call from the current eligible candidate set (cheap at realistic
// fleet sizes: sorting a few thousand points is microseconds), so there is
// no persisted ring state to keep in sync with node churn — membership IS
// whatever candidate list the caller passes in.
type hashRing struct {
	points []ringPoint
}

type ringPoint struct {
	hash   uint32
	member string
}

func newHashRing(members []string) *hashRing {
	points := make([]ringPoint, 0, len(members)*hashRingReplicas)
	for _, m := range members {
		for i := 0; i < hashRingReplicas; i++ {
			points = append(points, ringPoint{hash: hashKey(fmt.Sprintf("%s#%d", m, i)), member: m})
		}
	}
	sort.Slice(points, func(i, j int) bool { return points[i].hash < points[j].hash })
	return &hashRing{points: points}
}

// get returns the member owning key: the first ring point clockwise (>=)
// from key's own hash, wrapping around to the first point if key's hash
// falls past the last one. false if the ring is empty.
func (r *hashRing) get(key string) (string, bool) {
	if len(r.points) == 0 {
		return "", false
	}
	h := hashKey(key)
	i := sort.Search(len(r.points), func(i int) bool { return r.points[i].hash >= h })
	if i == len(r.points) {
		i = 0
	}
	return r.points[i].member, true
}

// hashKey is FNV-1a — not cryptographic, doesn't need to be: this only
// decides routing affinity, nothing security-sensitive keys on it.
func hashKey(s string) uint32 {
	h := fnv.New32a()
	_, _ = h.Write([]byte(s))
	return h.Sum32()
}
