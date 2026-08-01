// Package main is the entry point. Its job is unchanged — read config,
// construct dependencies, start the server — plus one new responsibility:
// shutting down GRACEFULLY when the OS asks (Ctrl-C locally, SIGTERM from
// any deploy platform): stop accepting new requests, finish in-flight ones,
// close the DB pool, then exit.
package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/BenYang12/Macro-Max/internal/config"
	"github.com/BenYang12/Macro-Max/internal/kroger"
	"github.com/BenYang12/Macro-Max/internal/server"
	"github.com/BenYang12/Macro-Max/internal/solver"
	"github.com/BenYang12/Macro-Max/internal/store"
)

func main() {
	cfg, err := config.LoadFromEnv()
	if err != nil {
		log.Fatal(err)
	}

	// The ROOT context for the whole process. signal.NotifyContext returns a
	// context that is CANCELLED when the OS delivers SIGINT (Ctrl-C) or
	// SIGTERM (how deploy platforms say "please stop"). Everything long-lived
	// derives from this ctx, so one signal cancels the whole tree of work.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Connect to Postgres and fail fast if it's unreachable (NewPool pings).
	st, err := store.NewPool(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatal(err)
	}
	// defer runs on ANY exit from main below this line — the pool always
	// gets closed. (Note: log.Fatal above skips defers, which is fine there:
	// nothing is open yet.)
	defer st.Close()
	log.Println("connected to database")

	// Dial the solver. grpc.NewClient does NOT connect — it sets up a managed
	// channel that connects lazily and reconnects on its own. So an error here
	// means the ADDRESS is malformed, not that the solver is down.
	//
	// I deliberately do not treat this as fatal. Without Postgres nothing
	// works, so that one is log.Fatal; without the solver only /v1/solve is
	// affected, so I log a warning and carry on serving everything else.
	sv, err := solver.New(cfg.SolverAddr)
	if err != nil {
		log.Printf("warning: solver client unavailable (%v); /v1/solve will not be registered", err)
		sv = nil
	} else {
		defer sv.Close()
		log.Printf("solver client configured for %s", cfg.SolverAddr)
	}

	// Redis, for the solve cache. Like the solver, this is NOT fatal: a cache
	// that can take down the API is worse than no cache at all. If Redis is
	// unreachable the handler simply computes every solve.
	cache, err := solver.NewCache(cfg.RedisURL)
	if err != nil {
		log.Printf("warning: solve cache unavailable (%v); every solve will be computed", err)
		cache = nil
	} else {
		defer cache.Close()
		pingCtx, cancelPing := context.WithTimeout(ctx, 2*time.Second)
		if err := cache.Ping(pingCtx); err != nil {
			log.Printf("warning: redis ping failed (%v); solves will not be cached", err)
		} else {
			log.Printf("solve cache connected to %s", cfg.RedisURL)
		}
		cancelPing()
	}

	// The Kroger client, for the store-picker endpoint. Only built when
	// credentials exist — without them /v1/stores is simply absent, which is a
	// clearer signal than a route that 500s on every call.
	var kr *kroger.Client
	if cfg.KrogerClientID != "" && cfg.KrogerClientSecret != "" {
		kr = kroger.New(cfg.KrogerClientID, cfg.KrogerClientSecret, nil)
		log.Println("kroger client configured; /v1/stores enabled")
	} else {
		log.Println("KROGER_CLIENT_ID/SECRET not set; /v1/stores will not be registered")
	}

	srv := server.New(cfg.Addr(), st, sv, cache, kr)

	// ListenAndServe BLOCKS until the server stops, but main must also watch
	// ctx for the shutdown signal — two things to wait on, so the server
	// gets its own goroutine ("go" starts one), and reports back through a
	// channel (a typed pipe between goroutines; buffer of 1 so the send
	// never blocks even if nobody is listening anymore).
	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.ListenAndServe()
	}()
	log.Printf("starting server on %s", cfg.Addr())

	// select waits on MULTIPLE channels and takes whichever is ready first:
	// either the server died on its own (port in use), or the OS asked us
	// to stop. ctx.Done() is a channel that closes on cancellation — this is
	// how contexts and select compose.
	select {
	case err := <-errCh:
		log.Fatal(err)
	case <-ctx.Done():
		log.Println("shutdown signal received")
	}

	// Graceful drain: Shutdown stops accepting new connections and waits for
	// in-flight requests to finish — but only up to this deadline, so a
	// stuck client can't hold the process hostage forever.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("graceful shutdown failed: %v", err)
	}

	log.Println("server stopped cleanly")
}
