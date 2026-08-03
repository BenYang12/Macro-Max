"""Whole-pack MILP with category, variety, and concentration constraints.

Integer pack variables determine cost while continuous consumed grams determine
nutrition, preserving the difference between groceries bought and food eaten.
"""

import time

from ortools.linear_solver import pywraplp

# Shared validation, calorie ceiling, and option defaults.
from lp import (
    FALLBACK_MAX_GRAMS_WEEK,
    _calorie_ceiling,
    _error_response,
    _resolve_options,
    _validate,
)
from solver.v1 import solver_pb2

# The concentration threshold, in grams per week. Past this much of a single
# food, the objective starts paying a per-gram tax. 1400g is 200g/day — enough
# that a genuine staple (rice, oats) never trips it, low enough that "3kg of
# lentils" does.
CONCENTRATION_THRESHOLD_G = 1400.0

# Ceiling on packs of any one product, so every integer variable has a finite
# domain. Unbounded integer variables make branch-and-bound explore forever.
MAX_PACKS_PER_PRODUCT = 30


def solve(request):
    """Solve the whole-pack MILP."""
    started = time.monotonic()

    problem = _validate(request)
    if problem:
        return _error_response(problem)

    opts = _resolve_options(request.options)
    foods = list(request.foods)

    solver = pywraplp.Solver.CreateSolver("SCIP")
    if solver is None:
        return _error_response("could not create SCIP solver (is ortools installed correctly?)")
    solver.SetTimeLimit(int(opts["time_limit_seconds"] * 1000))

    model = _build_model(solver, request, foods, opts)
    status = solver.Solve()
    elapsed = time.monotonic() - started

    if status in (pywraplp.Solver.OPTIMAL, pywraplp.Solver.FEASIBLE):
        return _build_response(request, foods, model, status, elapsed)

    # Everything else is some flavor of "no answer", which is where the staged
    # diagnosis begins. That's the most user-valuable code in this file.
    return _diagnose(request, foods, opts, elapsed)


def _group_by_food(foods):
    """Map food_id -> list of indices into `foods`.

    Variety is counted per FOOD, not per product: a 2 lb bag and a 10 lb bag of
    the same rice are one food. Without this grouping the model would happily
    "achieve variety" by buying the same thing in two sizes, which is exactly
    the gaming these constraints exist to prevent.
    """
    groups = {}
    for i, f in enumerate(foods):
        groups.setdefault(f.food_id, []).append(i)
    return groups


def _build_model(solver, request, foods, opts):
    """Declare every variable and constraint. Returns the handles I need later."""
    groups = _group_by_food(foods)
    kcal_ceiling = _calorie_ceiling(request.targets)

    # ---- Variables ---------------------------------------------------------
    packs, grams = [], []
    for f in foods:
        cap_g = f.max_grams_week if f.max_grams_week > 0 else FALLBACK_MAX_GRAMS_WEEK

        # How many packs I could conceivably need: enough to cover the gram cap,
        # and never more than the hard ceiling. A tight bound here is not
        # cosmetic — it directly shrinks the search tree.
        max_packs = min(MAX_PACKS_PER_PRODUCT, int(cap_g / f.pack_grams) + 1)

        packs.append(solver.IntVar(0, max_packs, f"n_{f.product_id}"))
        grams.append(solver.NumVar(0.0, cap_g, f"g_{f.product_id}"))

    used, over = {}, {}
    for food_id in groups:
        # BoolVar is shorthand for IntVar(0, 1). This is the variable that makes
        # the whole model non-convex, and it's what buys me the ability to say
        # "at least three DIFFERENT protein sources" — a statement no purely
        # continuous model can express.
        used[food_id] = solver.BoolVar(f"y_{food_id}")
        over[food_id] = solver.NumVar(0.0, solver.infinity(), f"o_{food_id}")

    # ---- Constraint 1: buy what you eat ------------------------------------
    # g_i <= n_i * pack_grams_i
    #
    # I can't eat more of a product than I bought. The SLACK in this inequality
    # is leftovers: buy a 4536g bag, eat 3000g, 1536g sits in the cupboard. That
    # gap is why the answer is honest — Phase 3 pretended food came in
    # arbitrarily divisible amounts.
    for i, f in enumerate(foods):
        solver.Add(grams[i] <= packs[i] * f.pack_grams)

    # ---- Constraint 2: budget, on what I BUY -------------------------------
    # The cost is now driven by integer packs, not by grams eaten. This is the
    # single most important change from Phase 3: I pay for the whole bag.
    total_cost = solver.Sum(packs[i] * foods[i].pack_price_cents for i in range(len(foods)))
    solver.Add(total_cost <= request.budget_cents)

    # ---- Macro targets and the calorie ceiling (unchanged from the LP) ------
    t = request.targets
    if t.protein_g > 0:
        solver.Add(solver.Sum(foods[i].protein_per_g * grams[i] for i in range(len(foods))) >= t.protein_g)
    if t.carbs_g > 0:
        solver.Add(solver.Sum(foods[i].carbs_per_g * grams[i] for i in range(len(foods))) >= t.carbs_g)
    if t.fat_g > 0:
        solver.Add(solver.Sum(foods[i].fat_per_g * grams[i] for i in range(len(foods))) >= t.fat_g)

    solver.Add(
        solver.Sum(foods[i].kcal_per_g * grams[i] for i in range(len(foods))) <= kcal_ceiling
    )

    # ---- Constraint 3: honest distinctness ---------------------------------
    # G_f = total grams of food f across all its products.
    #
    #   G_f <= max_grams_f * y_f    (y_f = 0 forces G_f = 0)
    #   G_f >= min_portion * y_f    (y_f = 1 forces a real portion)
    #
    # Read together, these say: a food is either ABSENT, or present in an amount
    # a person would actually eat. There is no middle. That's what kills the
    # gaming where the solver satisfies "3 protein sources" by buying one gram
    # each of two extra proteins.
    #
    # This is the classic BIG-M / indicator pattern: linking a binary to a
    # continuous quantity by multiplying the binary by an upper bound. The
    # bound has to be valid (never cutting off a real solution) but as TIGHT as
    # possible, because a loose big-M gives the solver weak relaxations and
    # makes branch-and-bound crawl. Using each food's own max_grams rather than
    # a global constant is that tightening.
    min_portion = opts["min_portion_grams"]
    food_grams = {}
    for food_id, idxs in groups.items():
        G_f = solver.Sum(grams[i] for i in idxs)
        food_grams[food_id] = G_f

        cap = max(
            (foods[i].max_grams_week if foods[i].max_grams_week > 0 else FALLBACK_MAX_GRAMS_WEEK)
            for i in idxs
        )
        solver.Add(G_f <= cap * used[food_id])
        solver.Add(G_f >= min_portion * used[food_id])

    # ---- Constraints 4 & 5: variety floors ---------------------------------
    # Now that y_f is honest, counting is trivial: sum the binaries in a
    # category. This is the payoff of all the machinery above.
    def category_of(food_id):
        return foods[groups[food_id][0]].category

    def require_at_least(category, n):
        if n <= 0:
            return
        members = [used[fid] for fid in groups if category_of(fid) == category]
        if not members:
            # Asking for 2 vegetables when the catalog has none is infeasible by
            # construction, and adding the constraint anyway would produce a
            # baffling "no solution" rather than the diagnosis in _diagnose.
            return
        solver.Add(solver.Sum(members) >= n)

    require_at_least("protein", opts["min_protein_sources"])
    require_at_least("vegetable", opts["min_vegetables"])
    require_at_least("fruit", opts["min_fruits"])

    # ---- Constraint 6: no single food dominates the calories ---------------
    # sum(kcal_i * g_i for i in food f) <= share * KcalMax
    #
    # The linearity here is deliberate and worth spelling out: KcalMax is the
    # CONSTANT ceiling computed above, NOT the variable total calories of the
    # basket. Writing it against the variable total would multiply two
    # variables together, which is not linear and which SCIP cannot accept.
    # Using the constant is slightly stricter than "30% of what you actually
    # eat", and that's a fine trade for staying in the linear world.
    share_cap = opts["max_kcal_share"] * kcal_ceiling
    for food_id, idxs in groups.items():
        solver.Add(solver.Sum(foods[i].kcal_per_g * grams[i] for i in idxs) <= share_cap)

    # ---- Constraint 7: the soft monotony tax -------------------------------
    # o_f >= G_f - threshold_f,  with o_f >= 0.
    #
    # Because o_f appears in the objective with a positive coefficient, the
    # solver will always push it down to exactly max(0, G_f - threshold). That's
    # a standard trick for expressing max(0, x) in a linear model: state it as
    # an inequality and let the objective do the tightening.
    #
    # SOFT, not hard: eating 3kg of rice stays legal, it just costs the
    # objective something. A hard cap would make perfectly reasonable
    # bulk-staple baskets infeasible.
    for food_id in groups:
        threshold = min(CONCENTRATION_THRESHOLD_G, _food_cap(foods, groups[food_id]))
        solver.Add(over[food_id] >= food_grams[food_id] - threshold)

    # ---- Objective ---------------------------------------------------------
    # Minimize actual spend, plus lambda cents per gram of over-concentration.
    # lambda ~ 0.05 c/g means 1000g past the threshold costs the objective 50
    # cents — enough to break a tie in favor of variety, not enough to override
    # a genuinely large price difference. This number is empirical and I expect
    # to tune it against real baskets.
    diversity_penalty = solver.Sum(over[fid] for fid in groups) * opts["diversity_lambda"]
    solver.Minimize(total_cost + diversity_penalty)

    return {"packs": packs, "grams": grams, "used": used, "over": over, "groups": groups}


def _food_cap(foods, idxs):
    return max(
        (foods[i].max_grams_week if foods[i].max_grams_week > 0 else FALLBACK_MAX_GRAMS_WEEK)
        for i in idxs
    )


def _build_response(request, foods, model, status, elapsed):
    """Turn solved variables into a basket."""
    packs, grams = model["packs"], model["grams"]

    items = []
    total_cents = 0
    achieved = {"protein": 0.0, "carbs": 0.0, "fat": 0.0, "kcal": 0.0}

    for i, f in enumerate(foods):
        # round() because an integer variable comes back as 2.9999999996.
        n = int(round(packs[i].solution_value()))
        if n == 0:
            continue

        g = grams[i].solution_value()

        # Cost is packs x price: both integers, so this is EXACT. No rounding,
        # no float objective, no violation of the money law. This is strictly
        # better than Phase 3, where I had to round a fractional pack count.
        cost = n * f.pack_price_cents
        total_cents += cost

        achieved["protein"] += f.protein_per_g * g
        achieved["carbs"] += f.carbs_per_g * g
        achieved["fat"] += f.fat_per_g * g
        achieved["kcal"] += f.kcal_per_g * g

        items.append(
            solver_pb2.BasketItem(
                product_id=f.product_id,
                packs=float(n),
                grams=g,
                cost_cents=cost,
                food_name=f.food_name,
                product_name=f.product_name,
            )
        )

    items.sort(key=lambda it: it.cost_cents, reverse=True)

    proto_status = (
        solver_pb2.SOLVE_STATUS_OPTIMAL
        if status == pywraplp.Solver.OPTIMAL
        else solver_pb2.SOLVE_STATUS_FEASIBLE
    )

    distinct_foods = len({f.food_id for f, it in zip(foods, range(len(foods)))
                          if packs[it].solution_value() >= 0.5})

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
        message=f"{len(items)} products across {distinct_foods} foods",
    )


# ---------------------------------------------------------------------------
# STAGED INFEASIBILITY DIAGNOSIS
#
# "Infeasible" on its own is the least useful thing software can say. The user
# cannot tell whether the problem is their budget, their macros, their diet
# filter, or a bug. So when the full model fails I re-solve progressively weaker
# versions of it and report which layer was actually responsible.
#
# Each stage answers a different question:
#   Stage 1  drop the budget          -> "how much WOULD this cost?"
#   Stage 2  drop variety too         -> "is variety what's impossible?"
#   Stage 3  nothing left to drop     -> "your macros are unreachable here"
# ---------------------------------------------------------------------------


def _diagnose(request, foods, opts, elapsed):
    without_budget = _resolve_relaxed(request, foods, opts, drop_budget=True)
    if without_budget is not None:
        # The macros AND the variety rules are satisfiable — I just can't afford
        # them. This is the actionable answer, and the most common one.
        return solver_pb2.SolveResponse(
            status=solver_pb2.SOLVE_STATUS_INFEASIBLE,
            min_feasible_budget_cents=without_budget,
            message=(
                f"no basket fits a budget of {request.budget_cents} cents; "
                f"with these variety requirements the macros need at least "
                f"{without_budget} cents at this store"
            ),
            solve_seconds=elapsed,
        )

    without_variety = _resolve_relaxed(request, foods, opts, drop_budget=True, drop_variety=True)
    if without_variety is not None:
        # Dropping variety made it solvable, so variety is the binding
        # constraint. Naming the specific shortage is what turns this from a
        # complaint into a diagnosis.
        shortages = _variety_shortfalls(foods, opts)
        detail = f" ({shortages})" if shortages else ""
        return solver_pb2.SolveResponse(
            status=solver_pb2.SOLVE_STATUS_INFEASIBLE,
            min_feasible_budget_cents=without_variety,
            message=(
                "the variety requirements cannot be met from the available "
                f"foods at this store{detail}; without them the macros would "
                f"cost {without_variety} cents"
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


def _resolve_relaxed(request, foods, opts, *, drop_budget=False, drop_variety=False):
    """Re-solve with some constraints removed. Returns min cost in cents, or None.

    I deliberately rebuild the model from scratch rather than mutating the
    original. pywraplp has no clean way to remove a constraint, and a
    half-modified model is a much worse bug than a few extra milliseconds.
    """
    solver = pywraplp.Solver.CreateSolver("SCIP")
    if solver is None:
        return None
    solver.SetTimeLimit(int(opts["time_limit_seconds"] * 1000))

    groups = _group_by_food(foods)
    kcal_ceiling = _calorie_ceiling(request.targets)

    packs, grams = [], []
    for f in foods:
        cap_g = f.max_grams_week if f.max_grams_week > 0 else FALLBACK_MAX_GRAMS_WEEK
        max_packs = min(MAX_PACKS_PER_PRODUCT, int(cap_g / f.pack_grams) + 1)
        packs.append(solver.IntVar(0, max_packs, f"n_{f.product_id}"))
        grams.append(solver.NumVar(0.0, cap_g, f"g_{f.product_id}"))

    for i, f in enumerate(foods):
        solver.Add(grams[i] <= packs[i] * f.pack_grams)

    total_cost = solver.Sum(packs[i] * foods[i].pack_price_cents for i in range(len(foods)))
    if not drop_budget:
        solver.Add(total_cost <= request.budget_cents)

    t = request.targets
    if t.protein_g > 0:
        solver.Add(solver.Sum(foods[i].protein_per_g * grams[i] for i in range(len(foods))) >= t.protein_g)
    if t.carbs_g > 0:
        solver.Add(solver.Sum(foods[i].carbs_per_g * grams[i] for i in range(len(foods))) >= t.carbs_g)
    if t.fat_g > 0:
        solver.Add(solver.Sum(foods[i].fat_per_g * grams[i] for i in range(len(foods))) >= t.fat_g)

    solver.Add(solver.Sum(foods[i].kcal_per_g * grams[i] for i in range(len(foods))) <= kcal_ceiling)

    if not drop_variety:
        used = {fid: solver.BoolVar(f"y_{fid}") for fid in groups}
        min_portion = opts["min_portion_grams"]
        for fid, idxs in groups.items():
            G_f = solver.Sum(grams[i] for i in idxs)
            cap = _food_cap(foods, idxs)
            solver.Add(G_f <= cap * used[fid])
            solver.Add(G_f >= min_portion * used[fid])

        def require(cat, n):
            if n <= 0:
                return
            members = [used[fid] for fid in groups if foods[groups[fid][0]].category == cat]
            if members:
                solver.Add(solver.Sum(members) >= n)

        require("protein", opts["min_protein_sources"])
        require("vegetable", opts["min_vegetables"])
        require("fruit", opts["min_fruits"])

        share_cap = opts["max_kcal_share"] * kcal_ceiling
        for fid, idxs in groups.items():
            solver.Add(solver.Sum(foods[i].kcal_per_g * grams[i] for i in idxs) <= share_cap)

    solver.Minimize(total_cost)
    status = solver.Solve()

    if status in (pywraplp.Solver.OPTIMAL, pywraplp.Solver.FEASIBLE):
        return int(round(solver.Objective().Value()))
    return None


def _variety_shortfalls(foods, opts):
    """Name the specific category that can't be satisfied.

    "only 2 vegan protein sources at this store" is a sentence a user can act
    on; "infeasible" is not. This counts DISTINCT foods per category and reports
    any category where the catalog itself is too thin.
    """
    counts = {}
    for f in foods:
        counts.setdefault(f.category, set()).add(f.food_id)

    problems = []
    for category, required in (
        ("protein", opts["min_protein_sources"]),
        ("vegetable", opts["min_vegetables"]),
        ("fruit", opts["min_fruits"]),
    ):
        available = len(counts.get(category, ()))
        if required > 0 and available < required:
            problems.append(f"only {available} {category} food(s) available, {required} required")

    return "; ".join(problems)
