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
	"github.com/BenYang12/Macro-Max/internal/server"
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

	_ = st // handlers start using the store in step 6; this quiets the compiler until then

	srv := server.New(cfg.Addr())

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
