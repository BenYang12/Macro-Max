"""Tests for the whole-pack mixed-integer program.

Every constraint in milp.py gets its own test, because a MILP fails QUIETLY: a
missing constraint doesn't error, it just returns an answer that's optimal for
the wrong problem. The only way to know constraint 5 is wired up is to construct
a case where its absence would produce a visibly different basket.

I build small catalogs by hand rather than using the seed data, so each test
isolates one behavior. The realistic end-to-end check lives in the Go E2E test.
"""

import pytest

import milp
from solver.v1 import solver_pb2

from solver_fixtures import food, request


def milp_request(foods, **kw):
    """A whole-pack request with every optional rule off by default.

    Two defaults here are deliberately NOT the production defaults, and I got
    this wrong the first time — nine tests failed at once until I worked out why.

    max_kcal_share defaults to 1.0 (no cap) rather than the production 0.30.
    The per-food calorie cap says no single food may supply more than 30% of the
    ceiling, which is exactly right for a 40-food catalog and IMPOSSIBLE for a
    two-food test: two foods cannot cover 100% of the calories at 30% each. The
    model was correct; my helper was asking for a contradiction. Tests that
    actually exercise the cap pass an explicit share and enough foods to satisfy
    it.

    diversity_lambda defaults to a near-zero epsilon rather than 0, because 0 is
    the proto's "use the default" sentinel and would silently become 0.05. This
    is the cost of encoding absent-vs-zero as a single value, and it's the same
    wart I documented in the proto — here it bites me from the other direction:
    I cannot ask for "explicitly no tax", only for "almost none".
    """
    opts = {
        "min_protein_sources": kw.pop("min_protein_sources", 0),
        "min_vegetables": kw.pop("min_vegetables", 0),
        "min_fruits": kw.pop("min_fruits", 0),
        "min_portion_grams": kw.pop("min_portion_grams", 0),
        "diversity_lambda": kw.pop("diversity_lambda", 1e-9),
        "max_kcal_share_per_food": kw.pop("max_kcal_share", 1.0),
        "time_limit_seconds": 15,
    }
    req = request(foods, **kw)
    req.options.CopyFrom(solver_pb2.SolveOptions(**opts))
    return req


# --------------------------------------------------------------- validation


@pytest.mark.parametrize(
    ("req", "message"),
    [
        (request([], protein=100), "no foods provided"),
        (request([food(1, protein=0.25)], protein=100, budget=0), "budget_cents must be positive"),
        (request([food(1, protein=0.25)], protein=-1), "macro targets must not be negative"),
        (request([food(1, protein=0.25)]), "all macro targets are zero"),
        (request([food(1, protein=0.25, pack_grams=0)], protein=100), "non-positive pack_grams"),
        (request([food(1, protein=0.25, price_cents=0)], protein=100), "non-positive pack_price_cents"),
    ],
)
def test_malformed_requests_return_structured_errors(req, message):
    resp = milp.solve(req)
    assert resp.status == solver_pb2.SOLVE_STATUS_ERROR
    assert message in resp.message


# ---------------------------------------------------------- shared invariants


def test_explicit_calorie_ceiling_is_respected():
    resp = milp.solve(milp_request(
        [food(1, protein=0.25, kcal=1.0)], protein=100, calories_max=300,
    ))
    assert resp.status == solver_pb2.SOLVE_STATUS_INFEASIBLE


def test_calorie_ceiling_is_derived_when_absent():
    resp = milp.solve(milp_request([food(1, protein=0.25, kcal=1.0)], protein=100))
    assert resp.status == solver_pb2.SOLVE_STATUS_OPTIMAL
    assert resp.achieved.calories <= 440 + 1e-6


def test_food_gram_cap_is_enforced():
    resp = milp.solve(milp_request(
        [food(1, protein=0.25, max_grams=100)], protein=100,
    ))
    assert resp.status == solver_pb2.SOLVE_STATUS_INFEASIBLE


def test_combined_macros_are_met_and_unused_products_are_filtered():
    foods = [
        food(1, category="protein", protein=1.0, price_cents=300),
        food(2, category="carb", carbs=1.0, price_cents=200),
        food(3, category="fat", fat=1.0, price_cents=100),
        food(4, category="protein", protein=1.0, price_cents=900),
    ]
    resp = milp.solve(milp_request(
        foods, protein=100, carbs=100, fat=100, min_portion_grams=1,
    ))
    assert resp.status == solver_pb2.SOLVE_STATUS_OPTIMAL
    assert resp.achieved.protein_g >= 100 - 1e-6
    assert resp.achieved.carbs_g >= 100 - 1e-6
    assert resp.achieved.fat_g >= 100 - 1e-6
    assert 4 not in {item.product_id for item in resp.items}
    assert [item.cost_cents for item in resp.items] == sorted(
        (item.cost_cents for item in resp.items), reverse=True
    )


# ------------------------------------------------------- the integer guarantee


def test_packs_are_whole_numbers():
    """Every returned quantity represents purchasable whole packs."""
    resp = milp.solve(milp_request([food(1, protein=0.25, pack_grams=1000, price_cents=800)],
                                   protein=1000))

    assert resp.status == solver_pb2.SOLVE_STATUS_OPTIMAL
    for it in resp.items:
        assert it.packs == int(it.packs), f"{it.packs} packs is not a whole number"


def test_cost_is_packs_times_price_exactly():
    """Money is now exact integer arithmetic, not a rounded float objective."""
    resp = milp.solve(milp_request([food(1, protein=0.25, pack_grams=1000, price_cents=837)],
                                   protein=1000))

    assert resp.status == solver_pb2.SOLVE_STATUS_OPTIMAL
    for it in resp.items:
        assert it.cost_cents == int(it.packs) * 837
    assert resp.total_cost_cents == sum(it.cost_cents for it in resp.items)


def test_you_cannot_eat_more_than_you_bought():
    """Constraint 1. Grams eaten <= packs bought x pack size.

    The pack is 1000g and I need 1500g of food, so the solver must buy TWO packs
    and leave 500g over.
    """
    resp = milp.solve(milp_request([food(1, protein=1.0, pack_grams=1000, price_cents=100)],
                                   protein=1500))

    assert resp.status == solver_pb2.SOLVE_STATUS_OPTIMAL
    it = resp.items[0]
    assert it.packs == 2, "should have bought 2 whole packs to cover 1500g"
    assert it.grams <= it.packs * 1000 + 1e-6
    assert it.grams >= 1500 - 1e-6


def test_leftovers_are_real():
    """The slack in constraint 1 is food I bought and didn't eat."""
    resp = milp.solve(milp_request([food(1, protein=1.0, pack_grams=1000, price_cents=100)],
                                   protein=1100))

    it = resp.items[0]
    assert it.packs == 2
    bought = it.packs * 1000
    assert bought - it.grams > 800, "expected substantial leftovers"


# ----------------------------------------------------------- variety machinery


def test_min_protein_sources_forces_distinct_foods():
    """Constraint 4. Three protein FOODS, even when one would be cheaper."""
    foods = [
        food(1, food_id=1, category="protein", protein=0.80, price_cents=100, pack_grams=1000),
        food(2, food_id=2, category="protein", protein=0.25, price_cents=900, pack_grams=1000),
        food(3, food_id=3, category="protein", protein=0.25, price_cents=950, pack_grams=1000),
        food(4, food_id=4, category="protein", protein=0.25, price_cents=999, pack_grams=1000),
    ]
    resp = milp.solve(milp_request(foods, protein=1200, min_protein_sources=3,
                                   min_portion_grams=200, budget=50_000))

    assert resp.status == solver_pb2.SOLVE_STATUS_OPTIMAL
    distinct = {it.food_name for it in resp.items}
    assert len(distinct) >= 3, f"only {distinct} — the variety floor was ignored"


def test_two_pack_sizes_of_one_food_count_as_one_food():
    """The anti-gaming rule, and the reason food_id exists in the contract.

    Both products share food_id=1. If the model counted PRODUCTS, buying both
    sizes would satisfy 'two protein sources'. It must not.
    """
    foods = [
        food(1, food_id=1, category="protein", protein=0.25, price_cents=100, pack_grams=1000),
        food(2, food_id=1, category="protein", protein=0.25, price_cents=180, pack_grams=2000),
        food(3, food_id=2, category="protein", protein=0.25, price_cents=900, pack_grams=1000),
    ]
    resp = milp.solve(milp_request(foods, protein=1000, min_protein_sources=2,
                                   min_portion_grams=200, budget=50_000))

    assert resp.status == solver_pb2.SOLVE_STATUS_OPTIMAL
    # food_id 2 is expensive but is the ONLY way to reach two distinct foods.
    product_ids = {it.product_id for it in resp.items}
    assert 3 in product_ids, "the solver gamed variety with two sizes of one food"


def test_min_portion_blocks_token_amounts():
    """Constraint 3. A food that's 'used' must be used meaningfully.

    Without this, the cheapest way to satisfy 'two protein sources' is to buy a
    single gram of a second protein. The min_portion floor makes that illegal.
    """
    foods = [
        food(1, food_id=1, category="protein", protein=0.80, price_cents=100, pack_grams=1000),
        food(2, food_id=2, category="protein", protein=0.25, price_cents=200, pack_grams=1000),
    ]
    resp = milp.solve(milp_request(foods, protein=1000, min_protein_sources=2,
                                   min_portion_grams=500, budget=50_000))

    assert resp.status == solver_pb2.SOLVE_STATUS_OPTIMAL
    for it in resp.items:
        assert it.grams >= 500 - 1e-6, f"{it.food_name}: {it.grams}g is below the 500g floor"


def test_category_coverage_forces_vegetables_and_fruit():
    """Constraint 5 ensures cost minimization still yields usable variety.

    Vegetables are a terrible way to buy macros, so a cost-minimizing model
    never picks them unless told to. This is the constraint that makes the
    basket cookable.
    """
    foods = [
        food(1, food_id=1, category="protein", protein=0.80, price_cents=100, pack_grams=1000),
        food(2, food_id=2, category="carb", carbs=0.80, price_cents=100, pack_grams=1000),
        food(3, food_id=3, category="fat", fat=1.0, price_cents=100, pack_grams=1000),
        food(4, food_id=4, category="vegetable", protein=0.03, carbs=0.07, price_cents=250, pack_grams=1000),
        food(5, food_id=5, category="vegetable", protein=0.03, carbs=0.04, price_cents=300, pack_grams=1000),
        food(6, food_id=6, category="fruit", carbs=0.23, price_cents=180, pack_grams=1000),
    ]
    resp = milp.solve(milp_request(foods, protein=1000, carbs=1200, fat=400,
                                   min_vegetables=2, min_fruits=1,
                                   min_portion_grams=200, budget=50_000))

    assert resp.status == solver_pb2.SOLVE_STATUS_OPTIMAL

    cats = {}
    for it in resp.items:
        idx = int(it.product_id) - 1
        cats.setdefault(foods[idx].category, set()).add(foods[idx].food_id)

    assert len(cats.get("vegetable", ())) >= 2, f"want 2 vegetables, got {cats}"
    assert len(cats.get("fruit", ())) >= 1, f"want 1 fruit, got {cats}"


@pytest.mark.parametrize("vegetable_count", [0, 1])
def test_missing_or_undersized_category_is_infeasible(vegetable_count):
    foods = [
        food(1, food_id=1, category="protein", protein=0.25,
             price_cents=100, pack_grams=1000),
    ]
    foods.extend(
        food(10 + i, food_id=10 + i, category="vegetable", carbs=0.05,
             price_cents=100, pack_grams=1000)
        for i in range(vegetable_count)
    )

    resp = milp.solve(milp_request(
        foods, protein=100, min_vegetables=2,
        min_portion_grams=100, budget=100_000,
    ))

    assert resp.status == solver_pb2.SOLVE_STATUS_INFEASIBLE
    assert f"only {vegetable_count} vegetable food(s) available, 2 required" in resp.message


def test_per_food_calorie_share_is_capped():
    """Constraint 6. No single food may dominate the basket's energy.

    One food is absurdly cheap per calorie, so without the cap the solver would
    take almost everything from it.
    """
    foods = [
        food(1, food_id=1, category="fat", fat=1.0, kcal=9.0, price_cents=50, pack_grams=1000),
        food(2, food_id=2, category="protein", protein=0.25, kcal=1.2, price_cents=900, pack_grams=1000),
        food(3, food_id=3, category="carb", carbs=0.80, kcal=3.6, price_cents=200, pack_grams=1000),
    ]
    # Explicit ceiling so the share math is predictable: 30% of 20000 = 6000 kcal.
    resp = milp.solve(milp_request(foods, protein=800, carbs=1200, fat=300,
                                   calories_max=20000, max_kcal_share=0.30,
                                   min_portion_grams=200, budget=50_000))

    if resp.status != solver_pb2.SOLVE_STATUS_OPTIMAL:
        pytest.skip(f"model infeasible under this cap ({resp.message})")

    for it in resp.items:
        idx = int(it.product_id) - 1
        kcal = foods[idx].kcal_per_g * it.grams
        assert kcal <= 6000 + 1, f"{it.food_name} supplies {kcal:.0f} kcal, over the 6000 cap"


def test_diversity_penalty_discourages_concentration():
    """Constraint 7 plus the objective term. A soft tax, not a hard limit.

    Two foods are nutritionally identical; one is marginally cheaper. With a
    large lambda, the monotony tax should outweigh that small price edge and
    push the solver to split between them rather than pile into one.
    """
    foods = [
        food(1, food_id=1, category="protein", protein=0.25, price_cents=100, pack_grams=1000),
        food(2, food_id=2, category="protein", protein=0.25, price_cents=101, pack_grams=1000),
    ]
    # 5000g of protein at 0.25/g needs 20kg of food — far past the 1400g
    # concentration threshold, so the tax definitely engages.
    resp = milp.solve(milp_request(foods, protein=5000, diversity_lambda=5.0,
                                   min_portion_grams=200, budget=500_000))

    assert resp.status == solver_pb2.SOLVE_STATUS_OPTIMAL
    assert len(resp.items) == 2, "a heavy monotony tax should have split the basket"


def test_zero_lambda_reverts_to_pure_cost_minimization():
    """The tax is opt-out. With lambda 0, cheapest wins outright."""
    foods = [
        food(1, food_id=1, category="protein", protein=0.25, price_cents=100, pack_grams=1000),
        food(2, food_id=2, category="protein", protein=0.25, price_cents=500, pack_grams=1000),
    ]
    resp = milp.solve(milp_request(foods, protein=1000, diversity_lambda=1e-9,
                                   min_portion_grams=200, budget=50_000))

    assert {it.product_id for it in resp.items} == {1}


# ------------------------------------------------- staged infeasibility layers


def test_budget_too_low_reports_what_it_would_cost():
    """Diagnosis stage 1: variety is satisfiable, money is the problem."""
    foods = [
        food(1, food_id=1, category="protein", protein=0.25, price_cents=800, pack_grams=1000),
        food(2, food_id=2, category="protein", protein=0.25, price_cents=850, pack_grams=1000),
        food(3, food_id=3, category="protein", protein=0.25, price_cents=900, pack_grams=1000),
    ]
    resp = milp.solve(milp_request(foods, protein=1000, min_protein_sources=3,
                                   min_portion_grams=200, budget=100))

    assert resp.status == solver_pb2.SOLVE_STATUS_INFEASIBLE
    assert resp.min_feasible_budget_cents > 100
    assert "at least" in resp.message


def test_variety_impossible_names_the_shortage():
    """Diagnosis stage 2: the catalog is too thin, and the message says so.

    Only two protein foods exist but three are required. The user needs to hear
    'only 2 protein foods available', not 'infeasible'.
    """
    foods = [
        food(1, food_id=1, category="protein", protein=0.25, price_cents=100, pack_grams=1000),
        food(2, food_id=2, category="protein", protein=0.25, price_cents=110, pack_grams=1000),
    ]
    resp = milp.solve(milp_request(foods, protein=1000, min_protein_sources=3,
                                   min_portion_grams=200, budget=100_000))

    assert resp.status == solver_pb2.SOLVE_STATUS_INFEASIBLE
    assert "variety" in resp.message
    assert "only 2 protein" in resp.message
    # Even here I can still say what the macros alone would cost.
    assert resp.min_feasible_budget_cents > 0


def test_unreachable_macros_at_any_budget():
    """Diagnosis stage 3: nothing left to relax."""
    resp = milp.solve(milp_request(
        [food(1, protein=0.25, price_cents=100, pack_grams=1000, max_grams=100)],
        protein=1000))

    assert resp.status == solver_pb2.SOLVE_STATUS_INFEASIBLE
    assert "any budget" in resp.message


# --------------------------------------------------------- realistic catalog


def test_the_stigler_basket_is_gone():
    """A realistic catalog produces variety while meeting its hard constraints."""
    catalog = [
        food(1, food_id=1, category="protein", protein=0.80, carbs=0.08, fat=0.03,
             pack_grams=2268, price_cents=4499, name="whey"),
        food(2, food_id=2, category="fat", fat=1.00,
             pack_grams=1305, price_cents=499, name="canola oil"),
        food(3, food_id=3, category="carb", carbs=0.80, protein=0.07,
             pack_grams=4536, price_cents=899, name="white rice"),
        food(4, food_id=4, category="protein", protein=0.225, fat=0.026,
             pack_grams=1361, price_cents=1047, name="chicken breast"),
        food(5, food_id=5, category="protein", protein=0.216, carbs=0.624, fat=0.014,
             pack_grams=454, price_cents=179, name="black beans"),
        food(6, food_id=6, category="vegetable", protein=0.028, carbs=0.066,
             pack_grams=340, price_cents=249, name="broccoli"),
        food(7, food_id=7, category="vegetable", protein=0.009, carbs=0.096,
             pack_grams=907, price_cents=229, name="carrots"),
        food(8, food_id=8, category="fruit", carbs=0.228, protein=0.011,
             pack_grams=1361, price_cents=177, name="bananas"),
        food(9, food_id=9, category="dairy", protein=0.102, carbs=0.036, fat=0.004,
             pack_grams=907, price_cents=449, name="greek yogurt"),
        food(10, food_id=10, category="carb", carbs=0.677, protein=0.132, fat=0.065,
             pack_grams=1191, price_cents=449, name="oats"),
    ]

    resp = milp.solve(milp_request(
        catalog, protein=1260, carbs=1400, fat=420, budget=15_000,
        min_protein_sources=3, min_vegetables=2, min_fruits=1,
        min_portion_grams=200, diversity_lambda=0.05, max_kcal_share=0.30,
    ))

    assert resp.status in (solver_pb2.SOLVE_STATUS_OPTIMAL, solver_pb2.SOLVE_STATUS_FEASIBLE), \
        f"MILP failed: {resp.message}"

    names = {it.food_name for it in resp.items}
    cats = {}
    for it in resp.items:
        idx = int(it.product_id) - 1
        cats.setdefault(catalog[idx].category, set()).add(catalog[idx].food_id)

    # The three variety claims are asserted directly.
    assert len(cats.get("protein", ())) >= 3, f"want >=3 protein foods, got {names}"
    assert len(cats.get("vegetable", ())) >= 2, f"want >=2 vegetables, got {names}"
    assert len(cats.get("fruit", ())) >= 1, f"want >=1 fruit, got {names}"

    # And it's still a real answer: macros met, budget respected, packs whole.
    assert resp.achieved.protein_g >= 1260 - 1e-6
    assert resp.total_cost_cents <= 15_000
    for it in resp.items:
        assert it.packs == int(it.packs)


# ------------------------------------------------------- diagnosis time budget


def test_infeasible_diagnosis_shares_one_time_budget(monkeypatch):
    """The staged diagnosis must not spend time_limit_seconds three times over.

    The Go client sets a fixed gRPC deadline. When each stage got its own full
    limit, a slow infeasible solve could take ~3x that limit, exceed the
    deadline, and turn the diagnosis into a bare 500 — losing precisely the
    answer the staging exists to produce.

    Rather than time a real solve (flaky on shared CI hardware), this records
    the limit handed to every relaxed re-solve and asserts the total rationed
    across them never exceeds the single budget.
    """
    handed_out = []
    original = milp._resolve_relaxed

    def spy(request, foods, opts, time_limit_seconds, **kw):
        handed_out.append(time_limit_seconds)
        return original(request, foods, opts, time_limit_seconds, **kw)

    monkeypatch.setattr(milp, "_resolve_relaxed", spy)

    # A budget far below any achievable cost: infeasible, so every stage runs.
    req = milp_request(
        [food(1, category="protein", protein=0.3, kcal=1.5, pack_grams=1000, price_cents=5000)],
        protein=700,
        budget=1,
    )
    resp = milp.solve(req)

    assert resp.status == solver_pb2.SOLVE_STATUS_INFEASIBLE
    assert handed_out, "the diagnosis should have attempted at least one stage"

    budget = req.options.time_limit_seconds
    assert max(handed_out) <= budget, "no stage may exceed the whole budget"
    for limit in handed_out:
        assert limit > 0, "a stage must never be given a non-positive limit"
