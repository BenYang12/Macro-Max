"""Validation and defaults shared by the whole-pack optimizer."""

from solver.v1 import solver_pb2

DEFAULT_MIN_PORTION_GRAMS = 200.0
DEFAULT_DIVERSITY_LAMBDA = 0.05
DEFAULT_MAX_KCAL_SHARE = 0.30
DEFAULT_TIME_LIMIT_SECONDS = 10.0
ATWATER_KCAL_PER_G = {"protein": 4.0, "carbs": 4.0, "fat": 9.0}
DERIVED_CALORIE_HEADROOM = 1.1
FALLBACK_MAX_GRAMS_WEEK = 20_000.0


def resolve_options(opts):
    return {
        "min_portion_grams": opts.min_portion_grams or DEFAULT_MIN_PORTION_GRAMS,
        "diversity_lambda": opts.diversity_lambda or DEFAULT_DIVERSITY_LAMBDA,
        "max_kcal_share": opts.max_kcal_share_per_food or DEFAULT_MAX_KCAL_SHARE,
        "time_limit_seconds": opts.time_limit_seconds or DEFAULT_TIME_LIMIT_SECONDS,
        "min_protein_sources": opts.min_protein_sources,
        "min_vegetables": opts.min_vegetables,
        "min_fruits": opts.min_fruits,
    }


def calorie_ceiling(targets):
    if targets.calories_max > 0:
        return targets.calories_max
    implied = (
        ATWATER_KCAL_PER_G["protein"] * targets.protein_g
        + ATWATER_KCAL_PER_G["carbs"] * targets.carbs_g
        + ATWATER_KCAL_PER_G["fat"] * targets.fat_g
    )
    return implied * DERIVED_CALORIE_HEADROOM


def validate(request):
    if not request.foods:
        return "no foods provided: nothing to choose from"
    if request.budget_cents <= 0:
        return f"budget_cents must be positive, got {request.budget_cents}"
    targets = request.targets
    if targets.protein_g < 0 or targets.carbs_g < 0 or targets.fat_g < 0:
        return "macro targets must not be negative"
    if targets.protein_g == 0 and targets.carbs_g == 0 and targets.fat_g == 0:
        return "all macro targets are zero: the empty basket is trivially optimal"
    for food in request.foods:
        if food.pack_grams <= 0:
            return f"product {food.product_id} has non-positive pack_grams {food.pack_grams}"
        if food.pack_price_cents <= 0:
            return f"product {food.product_id} has non-positive pack_price_cents {food.pack_price_cents}"
    return None


def error_response(message):
    return solver_pb2.SolveResponse(
        status=solver_pb2.SOLVE_STATUS_ERROR,
        message=message,
    )
