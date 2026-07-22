package store

// My design here is a three-layer stack
// Each layer knows only about the one below it

// HTTP request (outside world) -> handler (parse/format, closes to user, knows about HTTP) -> store(SQL) -> Postgres -> Bottom
// dependencies point downward, store has no idea HTTP exists, while the store knows Postgres exists and queries it.