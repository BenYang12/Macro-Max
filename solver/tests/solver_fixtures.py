"""Small protobuf builders shared by optimizer tests."""

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
