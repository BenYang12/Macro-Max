// Package solver is my Go side of the gRPC boundary: it turns database rows
// into a SolveRequest, calls the Python service, and hands back the answer.
//
// This package owns the UNIT CONVERSIONS, and that's its most important job.
// My database stores nutrition per-100g and targets per-day; the solver
// contract speaks per-gram and per-week. Those conversions happen here, in one
// file, exactly once. If I let them leak into handlers or into the solver, I'd
// have two places that both "know" about the factor of 100 or the factor of 7,
// and the day they disagree I'd get a basket that's off by 7x with no error
// anywhere.
package solver

import (
	"context"
	"fmt"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	solverv1 "github.com/BenYang12/Macro-Max/internal/gen/solver/v1"
	"github.com/BenYang12/Macro-Max/internal/store"
)

// Conversion constants, named so the arithmetic below reads as English rather
// than as magic numbers.
const (
	// My foods table stores macros per 100g because that's how USDA publishes.
	// The solver contract is per-gram. This is the only place that divides.
	gramsPerNutritionUnit = 100.0

	// Users think in daily targets; groceries are bought weekly; the solver
	// works in weekly totals. This is the only place that multiplies.
	daysPerWeek = 7
)

// Timeouts. I set the LP one short because GLOP solves this model in
// milliseconds — if it hasn't answered in 10 seconds something is badly wrong
// and I'd rather fail fast. Phase 4's MILP gets a longer one.
const (
	lpTimeout   = 10 * time.Second
	milpTimeout = 30 * time.Second
)

// Client wraps the generated gRPC stub.
//
// The generated SolverServiceClient is an interface, which is convenient: my
// handler tests can inject a fake without any network at all, exactly like the
// FoodStore interface pattern I've used since Phase 1.
type Client struct {
	rpc  solverv1.SolverServiceClient
	conn *grpc.ClientConn
}

// New dials the solver.
//
// A crucial thing I had to learn about grpc.NewClient: it does NOT connect.
// It creates a managed channel that connects lazily on the first RPC and
// reconnects automatically after failures. So this returning without error does
// not mean the solver is up — it means the address parsed. That's actually the
// behavior I want for a service dependency: my API should start even if the
// solver is briefly down, and recover on its own when it returns, rather than
// refusing to boot.
//
// (This is the opposite of my Postgres pool, where I deliberately Ping() at
// startup to fail fast. The difference is that without a database NOTHING in
// my API works, whereas without the solver only /v1/solve is affected.)
func New(addr string) (*Client, error) {
	conn, err := grpc.NewClient(
		addr,
		// No TLS: this is an internal call, on a private network in every
		// environment. See the matching note in server.py.
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		return nil, fmt.Errorf("creating solver client for %q: %w", addr, err)
	}

	return &Client{
		rpc:  solverv1.NewSolverServiceClient(conn),
		conn: conn,
	}, nil
}

// Close releases the connection. main defers it, same as the database pool.
func (c *Client) Close() error {
	if c.conn == nil {
		return nil
	}
	return c.conn.Close()
}

// SolveInput is what a caller gives me: a target, and the candidate products.
// I keep this as my OWN type rather than making handlers build a SolveRequest
// directly, so the proto stays an implementation detail of this package.
type SolveInput struct {
	Target   store.UserTarget
	Products []store.Product
	Foods    map[int64]store.Food // keyed by food id, for nutrition and category

	// IntegerPacks switches on the Phase 4 MILP. Phase 3 leaves it false.
	IntegerPacks bool
}

// Solve converts, calls, and returns the raw response.
func (c *Client) Solve(ctx context.Context, in SolveInput) (*solverv1.SolveResponse, error) {
	req, err := BuildRequest(in)
	if err != nil {
		return nil, err
	}

	// A DEADLINE on the RPC. Without one, a wedged solver would hold my HTTP
	// handler open until the client gave up — and my server's WriteTimeout
	// would fire, returning nothing useful.
	timeout := lpTimeout
	if in.IntegerPacks {
		timeout = milpTimeout
	}
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	resp, err := c.rpc.Solve(callCtx, req)
	if err != nil {
		return nil, fmt.Errorf("solver rpc: %w", err)
	}
	return resp, nil
}

// BuildRequest is the conversion layer, exported separately from Solve so I can
// unit-test the arithmetic without a server. This is where every unit rule in
// the project gets enforced, so it's the function most worth testing.
func BuildRequest(in SolveInput) (*solverv1.SolveRequest, error) {
	if len(in.Products) == 0 {
		return nil, fmt.Errorf("no products available for store %q", in.Target.StoreID)
	}

	foods := make([]*solverv1.Food, 0, len(in.Products))

	for _, p := range in.Products {
		f, ok := in.Foods[p.FoodID]
		if !ok {
			// A product whose food I didn't load is a bug in the caller's
			// query, not a recoverable condition. Failing loudly beats silently
			// solving over a smaller catalog than intended.
			return nil, fmt.Errorf("product %d references food %d, which was not loaded", p.ID, p.FoodID)
		}

		// Effective price: the promo price when there is one. My SQL already
		// computes this via COALESCE, so I just trust the column — the single
		// place that decides what a thing costs.
		price := p.EffectivePriceCents
		if price <= 0 {
			// Skip rather than fail: one mispriced product shouldn't kill an
			// otherwise fine solve. The solver validates too, but filtering
			// here keeps its input clean.
			continue
		}
		if p.NetWeightG <= 0 || !p.Available {
			continue
		}

		// A nil pointer means "no cap"; the contract encodes that as 0.
		var maxGrams float64
		if f.MaxGramsPerWeek != nil {
			maxGrams = *f.MaxGramsPerWeek
		}

		foods = append(foods, &solverv1.Food{
			ProductId: p.ID,
			FoodId:    p.FoodID,
			Category:  f.Category,

			// THE PER-100G -> PER-GRAM CONVERSION. Exactly once, right here.
			ProteinPerG: f.ProteinGPer100g / gramsPerNutritionUnit,
			CarbsPerG:   f.CarbsGPer100g / gramsPerNutritionUnit,
			FatPerG:     f.FatGPer100g / gramsPerNutritionUnit,
			KcalPerG:    f.KcalPer100g / gramsPerNutritionUnit,

			PackGrams:      p.NetWeightG,
			PackPriceCents: price,
			MaxGramsWeek:   maxGrams,

			FoodName:    f.Name,
			ProductName: p.Name,
		})
	}

	if len(foods) == 0 {
		return nil, fmt.Errorf("no usable products for store %q after filtering", in.Target.StoreID)
	}

	t := in.Target

	// THE DAILY -> WEEKLY CONVERSION. Also exactly once, also right here.
	targets := &solverv1.MacroTargets{
		ProteinG: float64(t.ProteinGDaily * daysPerWeek),
		CarbsG:   float64(t.CarbsGDaily * daysPerWeek),
		FatG:     float64(t.FatGDaily * daysPerWeek),
	}
	if t.CaloriesMaxDaily != nil {
		targets.CaloriesMax = float64(*t.CaloriesMaxDaily * daysPerWeek)
	}
	// A nil CaloriesMaxDaily leaves CaloriesMax at 0, which the contract
	// defines as "derive one" — not "unlimited". That sentinel is documented
	// in the proto and honored in lp.py.

	return &solverv1.SolveRequest{
		Targets:     targets,
		BudgetCents: int64(t.BudgetCentsWeekly),
		Foods:       foods,
		Options: &solverv1.SolveOptions{
			IntegerPacks: in.IntegerPacks,
		},
	}, nil
}
