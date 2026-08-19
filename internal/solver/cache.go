package solver

// cache.go — the Redis solve cache.
//
// A MILP solve takes ~100-250ms against my seeded catalog and will take longer
// as the model grows. That's fine once, and wasteful when a user tweaks their
// budget and re-solves the identical problem. So I cache answers.
//
// THE HARD PART OF CACHING IS INVALIDATION, and this design mostly sidesteps it.
// Instead of storing a key and later trying to remember when it went stale, I
// derive the key from EVERYTHING that could change the answer. If any input
// differs, the key differs, and I get a miss automatically. A stale entry is
// unreachable rather than wrong — it just ages out on its TTL.
//
// That's a "content-addressed" cache, and it's the same idea as a git commit
// hash or a Docker layer digest.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
	"google.golang.org/protobuf/proto"

	solverv1 "github.com/BenYang12/Macro-Max/internal/gen/solver/v1"
)

// 24 hours. Long enough that repeated tweaking is fast; short enough that a
// price I somehow failed to capture in the key can't haunt me for a week. The TTL is
// my backstop, not my primary invalidation.
const cacheTTL = 24 * time.Hour

// Cache wraps Redis. A nil *Cache is usable and simply never hits — so a
// missing Redis degrades my API to "slower", not "broken". Caches should never
// be load-bearing.
type Cache struct {
	rdb *redis.Client
}

func NewCache(redisURL string) (*Cache, error) {
	opt, err := redis.ParseURL(redisURL)
	if err != nil {
		return nil, fmt.Errorf("parsing redis url: %w", err)
	}
	return &Cache{rdb: redis.NewClient(opt)}, nil
}

func (c *Cache) Close() error {
	if c == nil || c.rdb == nil {
		return nil
	}
	return c.rdb.Close()
}

// SolveKey derives the cache key from the fully-built request.
//
// ONE INGREDIENT: the MARSHALED SolveRequest. I hash the serialized protobuf
// rather than hand-picking fields, because hand-picking is exactly how cache
// bugs get written: I add a solver option in three months, forget to add it to
// the key, and silently serve an answer computed under the old option. If it
// can change the answer it's in the request, and if it's in the request it's in
// the hash. The compiler can't enforce that, but this design makes forgetting
// impossible rather than merely unlikely.
//
// There is no separate prices fingerprint. One was kept here on the theory
// that it would become necessary once ingestion could change prices between
// two otherwise-identical requests. That day arrived and it still wasn't:
// ListSolveCandidates filters to available, priced, non-zero-weight rows in
// SQL, and BuildRequest copies each survivor's id and effective price into the
// request — so a price move changes the request, and therefore the blob, and
// therefore the key. The fingerprint hashed the same numbers a second time.
//
// One caveat I should be honest about: proto serialization is not guaranteed
// deterministic across library versions (map ordering, unknown fields). For a
// cache that's acceptable — the worst case is a spurious miss, which costs
// 200ms. I would NOT rely on this for anything where a false miss mattered.
func SolveKey(req *solverv1.SolveRequest) (string, error) {
	blob, err := proto.Marshal(req)
	if err != nil {
		return "", fmt.Errorf("marshaling request for cache key: %w", err)
	}

	sum := sha256.Sum256(blob)
	return "solve:" + hex.EncodeToString(sum[:]), nil
}

// Get returns a cached response, or nil for a miss.
//
// Every failure path here returns "miss" rather than an error. A cache that can
// break the request it's supposed to accelerate is worse than no cache: if
// Redis is down, or the stored bytes are corrupt, the correct behavior is to
// quietly recompute.
func (c *Cache) Get(ctx context.Context, key string) *solverv1.SolveResponse {
	if c == nil || c.rdb == nil {
		return nil
	}

	blob, err := c.rdb.Get(ctx, key).Bytes()
	if err != nil {
		// redis.Nil is an ordinary miss; anything else is Redis being unwell.
		// Both mean the same thing to me: compute it.
		return nil
	}

	var resp solverv1.SolveResponse
	if err := proto.Unmarshal(blob, &resp); err != nil {
		return nil
	}
	return &resp
}

// Set stores a response. Errors are swallowed for the same reason as above —
// failing to cache is not a reason to fail a request that already succeeded.
func (c *Cache) Set(ctx context.Context, key string, resp *solverv1.SolveResponse) {
	if c == nil || c.rdb == nil {
		return
	}

	blob, err := proto.Marshal(resp)
	if err != nil {
		return
	}
	c.rdb.Set(ctx, key, blob, cacheTTL)
}

// Ping verifies Redis is actually reachable, for startup logging. Unlike the
// database I do NOT fail startup on this — see the comment on Cache.
func (c *Cache) Ping(ctx context.Context) error {
	if c == nil || c.rdb == nil {
		return fmt.Errorf("no redis client")
	}
	return c.rdb.Ping(ctx).Err()
}
