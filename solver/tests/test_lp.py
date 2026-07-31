"""Tests for the pure LP solver.

Every test here builds a SolveRequest by hand and calls solve() directly. No
gRPC server, no network, no database — the same reason I made my Go
normalization functions pure. Optimization bugs are subtle and quiet (a wrong
answer looks exactly like a right one), so I want to be able to write many
small, fast, precisely-controlled cases.

A note on how I'm asserting: I mostly do NOT assert exact gram amounts. The
optimum is a real number that depends on floating-point simplex pivots, and
pinning it to 12 decimal places makes a brittle test that fails when OR-Tools
updates. Instead I assert the PROPERTIES that must hold for any correct answer:
constraints satisfied, cost within budget, cheaper option preferred over dearer.
"""

import pytest

import lp
from solver.v1 import solver_pb2


def food(
    product_id,
    *,
    food_id=None,
    category="protein",
    protein=0.0,
    carbs=0.0,
    fat=0.0,
    kcal=None,
    pack_grams=1000.0,
    price_cents=1000,
    max_grams=0.0,
    name=None,
):
    """Build a Food message with per-GRAM macros.

    A helper because a raw Food() literal is ten keyword arguments of noise, and
    unreadable tests are tests I won't maintain. kcal defaults to the Atwater
    value implied by the macros, so I only specify it when I'm deliberately
    testing an energy-dense food.
    """
    if kcal is None:
        kcal = 4 * protein + 4 * carbs + 9 * fat
    return solver_pb2.Food(
        product_id=product_id,
        food_id=food_id if food_id is not None else product_id,
        category=category,
        protein_per_g=protein,
        carbs_per_g=carbs,
        fat_per_g=fat,
        kcal_per_g=kcal,
        pack_grams=pack_grams,
        pack_price_cents=price_cents,
        max_grams_week=max_grams,
        food_name=name or f"food{product_id}",
        product_name=name or f"product{product_id}",
    )


def request(foods, *, protein=0.0, carbs=0.0, fat=0.0, calories_max=0.0, budget=100_000):
    return solver_pb2.SolveRequest(
        targets=solver_pb2.MacroTargets(
            protein_g=protein, carbs_g=carbs, fat_g=fat, calories_max=calories_max
        ),
        budget_cents=budget,
        foods=foods,
        options=solver_pb2.SolveOptions(),
    )


def achieved_macros(resp):
    return resp.achieved.protein_g, resp.achieved.carbs_g, resp.achieved.fat_g


# ---------------------------------------------------------------- basic sanity


def test_single_food_meets_protein_target():
    """The simplest possible solve: one food, one target."""
    # 0.25 g protein per gram = 25g protein per 100g, roughly chicken breast.
    resp = lp.solve(request([food(1, protein=0.25)], protein=1000))

    assert resp.status == solver_pb2.SOLVE_STATUS_OPTIMAL
    assert len(resp.items) == 1
    # 1000g protein / 0.25 per g = 4000g of food. I assert the CONSTRAINT holds
    # rather than the exact gram count, but here the answer is forced so I can
    # check it loosely.
    assert resp.items[0].grams == pytest.approx(4000, rel=0.01)
    assert resp.achieved.protein_g >= 1000 - 1e-6


def test_chooses_the_cheaper_of_two_equivalent_foods():
    """Given identical nutrition at different prices, cost must decide.

    If this failed, the objective isn't wired up — the single most important
    thing to verify about an optimizer.
    """
    cheap = food(1, protein=0.25, price_cents=500)
    dear = food(2, protein=0.25, price_cents=5000)

    resp = lp.solve(request([cheap, dear], protein=1000))

    assert resp.status == solver_pb2.SOLVE_STATUS_OPTIMAL
    chosen = {it.product_id for it in resp.items}
    assert chosen == {1}, "the solver bought the expensive food"


def test_all_macro_constraints_are_satisfied_together():
    """Three targets at once, satisfied by three complementary foods."""
    foods = [
        food(1, category="protein", protein=0.25, price_cents=800),
        food(2, category="carb", carbs=0.80, price_cents=250),
        food(3, category="fat", fat=1.00, price_cents=500),
    ]
    resp = lp.solve(request(foods, protein=1000, carbs=1500, fat=400))

    assert resp.status == solver_pb2.SOLVE_STATUS_OPTIMAL
    p, c, f = achieved_macros(resp)
    # Lower bounds: every target must be MET OR EXCEEDED. The tiny tolerance
    # absorbs simplex floating-point slop.
    assert p >= 1000 - 1e-6
    assert c >= 1500 - 1e-6
    assert f >= 400 - 1e-6


def test_total_cost_never_exceeds_budget():
    """The budget is a hard constraint, not a preference."""
    foods = [food(1, protein=0.25, price_cents=800), food(2, carbs=0.80, price_cents=250)]
    resp = lp.solve(request(foods, protein=1000, carbs=1500, budget=20_000))

    assert resp.status == solver_pb2.SOLVE_STATUS_OPTIMAL
    assert resp.total_cost_cents <= 20_000


# ------------------------------------------------------------------ ceilings


def test_calorie_ceiling_is_respected():
    """An explicit calorie cap must bind.

    I give the solver a very cheap, very calorie-dense food and a tight ceiling.
    Without the kcal constraint it would load up on the cheap one.
    """
    dense = food(1, protein=0.10, fat=0.50, kcal=5.0, price_cents=100)
    lean = food(2, protein=0.25, fat=0.01, kcal=1.2, price_cents=900)

    resp = lp.solve(request([dense, lean], protein=1000, calories_max=6000))

    assert resp.status == solver_pb2.SOLVE_STATUS_OPTIMAL
    assert resp.achieved.calories <= 6000 + 1e-6


def test_calorie_ceiling_is_derived_when_unset():
    """calories_max = 0 means 'derive one', not 'unlimited calories'.

    This is the sentinel-vs-value distinction from my proto comments, and
    getting it backwards would silently remove the only downward pressure on
    quantity in the whole model.
    """
    resp = lp.solve(request([food(1, protein=0.25)], protein=1000))

    # Derived ceiling = 1.1 * (4*1000) = 4400 kcal.
    assert resp.status == solver_pb2.SOLVE_STATUS_OPTIMAL
    assert resp.achieved.calories <= 4400 + 1e-6


def test_max_grams_per_week_caps_a_single_food():
    """The palatability cap must limit how much of one food gets bought.

    This is what stops "just eat 4kg of whey". The cheap food is capped, so the
    solver is forced to buy the expensive one too.
    """
    cheap_capped = food(1, protein=0.80, price_cents=100, max_grams=500)
    dear_uncapped = food(2, protein=0.25, price_cents=2000)

    resp = lp.solve(request([cheap_capped, dear_uncapped], protein=1000))

    assert resp.status == solver_pb2.SOLVE_STATUS_OPTIMAL
    by_id = {it.product_id: it for it in resp.items}
    assert by_id[1].grams <= 500 + 1e-6, "the weekly cap was ignored"
    assert 2 in by_id, "the solver should have been forced onto the second food"


# -------------------------------------------------------------- infeasibility


def test_impossible_budget_returns_infeasible_with_a_real_number():
    """The most valuable answer this service gives.

    A 1-cent budget can't buy anything, so the solve fails. What matters is that
    it comes back with min_feasible_budget_cents — the actionable number — not
    just the word "infeasible".
    """
    resp = lp.solve(request([food(1, protein=0.25, price_cents=800)], protein=1000, budget=1))

    assert resp.status == solver_pb2.SOLVE_STATUS_INFEASIBLE
    assert resp.min_feasible_budget_cents > 0
    # 4000g needed at 800 cents/1000g = 3200 cents.
    assert resp.min_feasible_budget_cents == pytest.approx(3200, rel=0.02)
    assert "at least" in resp.message


def test_unreachable_macros_are_infeasible_at_any_budget():
    """When caps make the target impossible, money isn't the problem.

    Here the only protein food is capped at 100g/week, so 1000g of protein
    cannot be reached no matter how much I'm willing to spend. The response must
    say that rather than quoting an impossible price.
    """
    resp = lp.solve(
        request([food(1, protein=0.25, price_cents=100, max_grams=100)], protein=1000)
    )

    assert resp.status == solver_pb2.SOLVE_STATUS_INFEASIBLE
    assert resp.min_feasible_budget_cents == 0
    assert "any budget" in resp.message


# ------------------------------------------------------------------ validation


@pytest.mark.parametrize(
    "req,expected",
    [
        (request([], protein=100), "no foods provided"),
        (request([food(1, protein=0.25)], protein=100, budget=0), "budget_cents must be positive"),
        (request([food(1, protein=0.25)]), "all macro targets are zero"),
        (
            request([food(1, protein=0.25, pack_grams=0)], protein=100),
            "non-positive pack_grams",
        ),
        (
            request([food(1, protein=0.25, price_cents=0)], protein=100),
            "non-positive pack_price_cents",
        ),
    ],
)
def test_malformed_requests_are_rejected_with_a_clear_message(req, expected):
    """Validation runs BEFORE the model is built.

    OR-Tools' errors for a malformed model are cryptic; mine name the actual
    problem. parametrize is pytest's table-driven testing, the same pattern as
    my Go test tables.
    """
    resp = lp.solve(req)
    assert resp.status == solver_pb2.SOLVE_STATUS_ERROR
    assert expected in resp.message


# --------------------------------------------------- money and reporting rules


def test_costs_are_integer_cents_recomputed_from_prices():
    """Money must survive the round trip through floating-point optimization.

    The solver works in floats internally. My rule is that every cent I REPORT
    is recomputed from integer pack prices, never read off the float objective.
    Here the line total must equal the sum of the item costs exactly.
    """
    foods = [
        food(1, protein=0.25, price_cents=837),
        food(2, carbs=0.80, price_cents=249),
    ]
    resp = lp.solve(request(foods, protein=1000, carbs=1500))

    assert resp.status == solver_pb2.SOLVE_STATUS_OPTIMAL
    assert resp.total_cost_cents == sum(it.cost_cents for it in resp.items)
    assert isinstance(resp.total_cost_cents, int)


def test_numerically_zero_lines_are_dropped():
    """Simplex leaves unused variables at ~1e-13, not exactly 0.

    A basket listing '0.0000000000001g of tilapia' is noise, so anything under
    half a gram gets filtered out of the response.
    """
    foods = [
        food(1, protein=0.25, price_cents=100),
        food(2, protein=0.25, price_cents=9999),  # never worth buying
    ]
    resp = lp.solve(request(foods, protein=1000))

    for it in resp.items:
        assert it.grams >= 0.5


def test_items_are_sorted_most_expensive_first():
    foods = [
        food(1, category="carb", carbs=0.80, price_cents=200),
        food(2, category="protein", protein=0.25, price_cents=900),
    ]
    resp = lp.solve(request(foods, protein=1000, carbs=1500))

    costs = [it.cost_cents for it in resp.items]
    assert costs == sorted(costs, reverse=True)


# ------------------------------------------------------ the Stigler demo case


def test_stigler_basket_is_degenerate_and_that_is_the_point():
    """Phase 3's headline result: mathematically optimal, humanly inedible.

    I hand the solver a realistic catalog including cheap whey, cheap oil, and
    cheap rice — the three foods I seeded specifically to make this happen — and
    it ignores everything else. This test EXISTS to document that the naive LP
    produces a basket nobody would eat, which is the entire justification for
    the Phase 4 variety constraints.

    When Phase 4 lands, the equivalent test asserts the opposite, and the diff
    between them is the demo.
    """
    catalog = [
        # The degenerate trio: unbeatable cost per macro.
        food(1, food_id=1, category="protein", protein=0.80, carbs=0.08, fat=0.03,
             pack_grams=2268, price_cents=4499, name="whey"),
        food(2, food_id=2, category="fat", fat=1.00,
             pack_grams=1305, price_cents=499, name="canola oil"),
        food(3, food_id=3, category="carb", carbs=0.80, protein=0.07,
             pack_grams=4536, price_cents=899, name="white rice"),
        # Real foods that a human would actually want, all more expensive per
        # unit of macro.
        food(4, food_id=4, category="protein", protein=0.225, fat=0.026,
             pack_grams=1361, price_cents=1047, name="chicken breast"),
        food(5, food_id=5, category="vegetable", protein=0.028, carbs=0.066,
             pack_grams=340, price_cents=249, name="broccoli"),
        food(6, food_id=6, category="fruit", carbs=0.228, protein=0.011,
             pack_grams=1361, price_cents=177, name="bananas"),
        food(7, food_id=7, category="dairy", protein=0.102, carbs=0.036, fat=0.004,
             pack_grams=907, price_cents=449, name="greek yogurt"),
    ]

    # 180g protein / 200g carbs / 60g fat daily, x7 for the week.
    resp = lp.solve(
        request(catalog, protein=1260, carbs=1400, fat=420, budget=10_000)
    )

    assert resp.status == solver_pb2.SOLVE_STATUS_OPTIMAL

    names = {it.food_name for it in resp.items}

    # THE POINT: a tiny handful of foods, drawn from the cheap trio.
    assert len(resp.items) <= 4, f"expected a degenerate basket, got {names}"
    assert names <= {"whey", "canola oil", "white rice"}, (
        f"expected only the cheap trio, got {names}"
    )

    # And no vegetables or fruit at all — the specific failure Phase 4 fixes.
    assert "broccoli" not in names
    assert "bananas" not in names
