package store

// In databases, a pool (or connection pool) is a stored cache of ready-to-use connections to the database. Instead of opening and closing a new connection for every single user request, my application borrows an existing connection, uses it, and returns it to the pool when finished.
// My server handles requests concurrently (stdlib spawns a goroutine per request).
// A pool holds several connections, lends one per query, and takes it back after
// pgx pool also handles reconnecting when the database restarts.

// Package store is the data layer: everything that touches Postgres lives here!
// Handlers never import pgx directly -> they go through this package, so SQL stays in one place!
import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Store struct wraps the pgx connection pool. Later steps hang query methods off
// this struct (ListFoods, GetProduct, ...), so handlers receive a *Store and
// never see connection details.

// Why a struct instead of a package-level global pool?
// Explicit dependencies: anything that needs the DB must be GIVEN a *Store, which makes the dependency visible in signatures and swappable in tests.
type Store struct {
	Pool *pgxpool.Pool
}

// NewPool connects to Postgres and verifies the connection with a ping
// DSN -> Data Source Name, a data structure used to connect an application to a database.
func NewPool(ctx context.Context, dsn string) (*Store, error) {
	// pgxpool.New parses the DSN and prepares the pool, but does NOT actually connect yet.
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("creating connection pool: %w", err)
	}

	// Because connections are lazy, a bad DSN or unreachable database would otherwise go unnoticed until the First Query -> runtime error
	// Ping forces one real connection now, so a broken database fails at startup with a clear error
	// Remember, I want my applications to fail fast!
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second) //WithTimeout derives a child context: cancelled when the parent is, OR after 5 seconds, whichever comes first.
	defer cancel()                                             //always release the timer, even on the success path

	if err := pool.Ping(pingCtx); err != nil {
		pool.Close() // don't leak the half-made pool alongside the error
		return nil, fmt.Errorf("pinging database: %w", err)
	}

	return &Store{Pool: pool}, nil //&Store{} creates the struct and returns a pointer to it (*Store)
}

// Close returns all connections to the OS. Call on shutdown (main defers it).
func (s *Store) Close() {
	s.Pool.Close()
}
