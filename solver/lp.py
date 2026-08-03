"""Continuous least-cost diet model used as the MILP baseline."""

import time

from ortools.linear_solver import pywraplp

from solver.v1 import solver_pb2

# Solver-side defaults for proto fields left at zero.
DEFAULT_MIN_PORTION_GRAMS = 200.0
DEFAULT_DIVERSITY_LAMBDA = 0.05
DEFAULT_MAX_KCAL_SHARE = 0.30
DEFAULT_TIME_LIMIT_SECONDS = 10.0

# Derive a calorie ceiling from Atwater factors when none is supplied.
ATWATER_KCAL_PER_G = {"protein": 4.0, "carbs": 4.0, "fat": 9.0}
DERIVED_CALORIE_HEADROOM = 1.1

# Finite fallback bound for otherwise uncapped food variables.
FALLBACK_MAX_GRAMS_WEEK = 20_000.0


def _resolve_options(opts):
    """Fill solver-side defaults for options left at zero."""
    return {
        "integer_packs": opts.integer_packs,
        "min_portion_grams": opts.min_portion_grams or DEFAULT_MIN_PORTION_GRAMS,
        "diversity_lambda": opts.diversity_lambda or DEFAULT_DIVERSITY_LAMBDA,
        "max_kcal_share": opts.max_kcal_share_per_food or DEFAULT_MAX_KCAL_SHARE,
        "time_limit_seconds": opts.time_limit_seconds or DEFAULT_TIME_LIMIT_SECONDS,
        "min_protein_sources": opts.min_protein_sources,
        "min_vegetables": opts.min_vegetables,
        "min_fruits": opts.min_fruits,
    }


def _calorie_ceiling(targets):
    """Return the kcal upper bound, deriving one if the caller didn't set it."""
    if targets.calories_max > 0:
        return targets.calories_max

    implied = (
        ATWATER_KCAL_PER_G["protein"] * targets.protein_g
        + ATWATER_KCAL_PER_G["carbs"] * targets.carbs_g
        + ATWATER_KCAL_PER_G["fat"] * targets.fat_g
    )
    return implied * DERIVED_CALORIE_HEADROOM


def _validate(request):
    """Reject requests that can't produce a meaningful answer.

    I return a string (the error message) or None. Doing this BEFORE building
    the model matters: OR-Tools' own errors for a malformed model are cryptic,
    and "no foods provided" is far more useful than whatever GLOP says when
    handed an empty variable set.
    """
    if not request.foods:
        return "no foods provided: nothing to choose from"
    if request.budget_cents <= 0:
        return f"budget_cents must be positive, got {request.budget_cents}"

    t = request.targets
    if t.protein_g < 0 or t.carbs_g < 0 or t.fat_g < 0:
        return "macro targets must not be negative"
    if t.protein_g == 0 and t.carbs_g == 0 and t.fat_g == 0:
        return "all macro targets are zero: the empty basket is trivially optimal"

    for f in request.foods:
        if f.pack_grams <= 0:
            return f"product {f.product_id} has non-positive pack_grams {f.pack_grams}"
        if f.pack_price_cents <= 0:
            return f"product {f.product_id} has non-positive pack_price_cents {f.pack_price_cents}"
    return None


def _error_response(message):
    return solver_pb2.SolveResponse(
        status=solver_pb2.SOLVE_STATUS_ERROR,
        message=message,
    )


def solve(request):
    """Solve the Phase 3 LP. Returns a SolveResponse.

    Phase 4 will branch here on options.integer_packs and hand off to milp.py.
    For now everything continuous.
    """
    started = time.monotonic()

    problem = _validate(request)
    if problem:
        return _error_response(problem)

    opts = _resolve_options(request.options)
    foods = list(request.foods)

    # ---- The solver object -------------------------------------------------
    # GLOP is Google's own simplex implementation for pure LINEAR programs. It
    # only handles continuous variables, which is exactly Phase 3's model — and
    # it's why Phase 4 has to switch engines to SCIP rather than just adding
    # constraints. Asking GLOP for an integer variable silently gets me a
    # continuous one, which would be a very quiet, very wrong bug.
    solver = pywraplp.Solver.CreateSolver("GLOP")
    if solver is None:
        return _error_response("could not create GLOP solver (is ortools installed correctly?)")

    solver.SetTimeLimit(int(opts["time_limit_seconds"] * 1000))  # milliseconds

    # ---- Decision variables ------------------------------------------------
    # One variable per product: how many GRAMS of it the basket contains.
    #
    # Grams rather than packs is the deliberate Phase 3 simplification. It lets
    # every constraint stay linear and lets GLOP solve instantly, at the cost of
    # producing answers like "buy 2.4 bags of rice" that nobody can act on.
    # Phase 4 fixes exactly this by introducing integer pack variables.
    grams = []
    for f in foods:
        cap = f.max_grams_week if f.max_grams_week > 0 else FALLBACK_MAX_GRAMS_WEEK
        # NumVar(lower_bound, upper_bound, name). The lower bound of 0 encodes
        # "you cannot buy negative food", which sounds obvious but is a real
        # constraint I have to state — without it the solver would happily
        # "sell" food to fund the rest of the basket.
        grams.append(solver.NumVar(0.0, cap, f"g_{f.product_id}"))

    # ---- Cost, expressed once and reused -----------------------------------
    # Cost per gram = pack price / pack grams. I compute this in Python rather
    # than asking my Go code to send it, because the solver is the only place
    # that needs it and deriving it here keeps the contract smaller.
    #
    # This is where money temporarily becomes a float, which violates my
    # project's integer-cents law. That's acceptable ONLY because the LP needs
    # real arithmetic to optimize — and I defend against it by recomputing every
    # returned cost from integer prices at the end, never trusting the solver's
    # float objective.
    cost_per_gram = [f.pack_price_cents / f.pack_grams for f in foods]

    total_cost = solver.Sum(cost_per_gram[i] * grams[i] for i in range(len(foods)))

    # ---- Constraints -------------------------------------------------------
    # Each of these is a linear inequality. Reading them as sentences:

    # 1. "The basket must supply at least my weekly protein target." Same for
    #    carbs and fat. These are the constraints that make the answer useful.
    targets = request.targets
    if targets.protein_g > 0:
        solver.Add(
            solver.Sum(foods[i].protein_per_g * grams[i] for i in range(len(foods)))
            >= targets.protein_g
        )
    if targets.carbs_g > 0:
        solver.Add(
            solver.Sum(foods[i].carbs_per_g * grams[i] for i in range(len(foods)))
            >= targets.carbs_g
        )
    if targets.fat_g > 0:
        solver.Add(
            solver.Sum(foods[i].fat_per_g * grams[i] for i in range(len(foods)))
            >= targets.fat_g
        )

    # 2. "The basket must not exceed my calorie ceiling." This is the only
    #    constraint pushing DOWNWARD on quantity. Without it the LP is
    #    effectively unbounded in the tasty direction: more food always
    #    satisfies macro floors better, and only cost objects.
    kcal_ceiling = _calorie_ceiling(targets)
    total_kcal = solver.Sum(foods[i].kcal_per_g * grams[i] for i in range(len(foods)))
    solver.Add(total_kcal <= kcal_ceiling)

    # 3. "The basket must cost no more than my budget." Note this appears BOTH
    #    as a constraint and as the objective. That isn't redundant: the
    #    objective says "prefer cheaper", the constraint says "over budget is
    #    not an answer at all". Without the constraint, the solver would return
    #    the cheapest basket even if it were still unaffordable.
    solver.Add(total_cost <= request.budget_cents)

    # Per-food max_grams_week is already encoded as each variable's upper bound
    # above, which is cheaper for the solver than a separate constraint row.

    # ---- Objective ---------------------------------------------------------
    solver.Minimize(total_cost)

    # ---- Solve -------------------------------------------------------------
    status = solver.Solve()
    elapsed = time.monotonic() - started

    if status == pywraplp.Solver.INFEASIBLE:
        # Infeasible is not a failure — it's information. The most useful thing
        # I can do is answer the obvious follow-up question: "then how much
        # WOULD it cost?" I re-solve without the budget constraint to find out.
        return _infeasible_response(request, opts, elapsed)

    if status not in (pywraplp.Solver.OPTIMAL, pywraplp.Solver.FEASIBLE):
        return solver_pb2.SolveResponse(
            status=solver_pb2.SOLVE_STATUS_ERROR,
            message=f"solver returned unexpected status {status}",
            solve_seconds=elapsed,
        )

    return _build_response(request, foods, grams, status, elapsed)


def _build_response(request, foods, grams, status, elapsed):
    """Turn solved variable values back into a protobuf answer."""
    items = []
    total_cents = 0
    achieved = {"protein": 0.0, "carbs": 0.0, "fat": 0.0, "kcal": 0.0}

    for i, f in enumerate(foods):
        g = grams[i].solution_value()

        # Drop numerically-zero lines. Simplex routinely leaves variables at
        # 1e-13 instead of exactly 0, and a basket listing "0.0000000000001g of
        # tilapia" is noise. 0.5g is comfortably below any real portion.
        if g < 0.5:
            continue

        packs = g / f.pack_grams

        # MONEY BACK TO INTEGERS. I deliberately do NOT read the solver's float
        # objective. I recompute each line from the integer pack price and round
        # once, so my totals are exact cents and my project's money law survives
        # its trip through floating-point optimization.
        cost_cents = round(packs * f.pack_price_cents)
        total_cents += cost_cents

        achieved["protein"] += f.protein_per_g * g
        achieved["carbs"] += f.carbs_per_g * g
        achieved["fat"] += f.fat_per_g * g
        achieved["kcal"] += f.kcal_per_g * g

        items.append(
            solver_pb2.BasketItem(
                product_id=f.product_id,
                packs=packs,
                grams=g,
                cost_cents=cost_cents,
                food_name=f.food_name,
                product_name=f.product_name,
            )
        )

    # Sort most expensive first: when I read a basket, the big-ticket lines are
    # what I care about.
    items.sort(key=lambda it: it.cost_cents, reverse=True)

    proto_status = (
        solver_pb2.SOLVE_STATUS_OPTIMAL
        if status == pywraplp.Solver.OPTIMAL
        else solver_pb2.SOLVE_STATUS_FEASIBLE
    )

    return solver_pb2.SolveResponse(
        status=proto_status,
        items=items,
        total_cost_cents=total_cents,
        achieved=solver_pb2.MacroTotals(
            protein_g=achieved["protein"],
            carbs_g=achieved["carbs"],
            fat_g=achieved["fat"],
            calories=achieved["kcal"],
        ),
        solve_seconds=elapsed,
        message=f"{len(items)} products",
    )


def _infeasible_response(request, opts, elapsed):
    """Re-solve without the budget to answer 'what WOULD this cost?'.

    This is the highest-value thing the whole service does. "Infeasible" alone
    is a dead end for a user. "Your macros need at least $47/week at this store"
    is actionable — they can raise the budget, lower a target, or pick a
    different store, and they know which.

    If it's STILL infeasible with an unlimited budget, then money was never the
    problem: the available foods genuinely cannot hit those macros (e.g. a
    vegan filter leaving only two protein sources, both capped).
    """
    solver = pywraplp.Solver.CreateSolver("GLOP")
    if solver is None:
        return _error_response("could not create GLOP solver for the infeasibility probe")
    solver.SetTimeLimit(int(opts["time_limit_seconds"] * 1000))

    foods = list(request.foods)
    grams = []
    for f in foods:
        cap = f.max_grams_week if f.max_grams_week > 0 else FALLBACK_MAX_GRAMS_WEEK
        grams.append(solver.NumVar(0.0, cap, f"g_{f.product_id}"))

    cost_per_gram = [f.pack_price_cents / f.pack_grams for f in foods]
    total_cost = solver.Sum(cost_per_gram[i] * grams[i] for i in range(len(foods)))

    t = request.targets
    if t.protein_g > 0:
        solver.Add(solver.Sum(foods[i].protein_per_g * grams[i] for i in range(len(foods))) >= t.protein_g)
    if t.carbs_g > 0:
        solver.Add(solver.Sum(foods[i].carbs_per_g * grams[i] for i in range(len(foods))) >= t.carbs_g)
    if t.fat_g > 0:
        solver.Add(solver.Sum(foods[i].fat_per_g * grams[i] for i in range(len(foods))) >= t.fat_g)

    # Every constraint EXCEPT the budget. The calorie ceiling stays, because
    # relaxing it too would answer a different question than the one asked.
    solver.Add(
        solver.Sum(foods[i].kcal_per_g * grams[i] for i in range(len(foods)))
        <= _calorie_ceiling(t)
    )

    solver.Minimize(total_cost)
    status = solver.Solve()

    if status in (pywraplp.Solver.OPTIMAL, pywraplp.Solver.FEASIBLE):
        needed = round(solver.Objective().Value())
        return solver_pb2.SolveResponse(
            status=solver_pb2.SOLVE_STATUS_INFEASIBLE,
            min_feasible_budget_cents=needed,
            message=(
                f"no basket fits a budget of {request.budget_cents} cents; "
                f"these macros need at least {needed} cents at this store"
            ),
            solve_seconds=elapsed,
        )

    return solver_pb2.SolveResponse(
        status=solver_pb2.SOLVE_STATUS_INFEASIBLE,
        message=(
            "these macro targets cannot be met from the available foods at any "
            "budget: the per-food weekly caps or the calorie ceiling are binding"
        ),
        solve_seconds=elapsed,
    )
