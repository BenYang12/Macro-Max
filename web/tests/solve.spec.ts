import { expect, test, type Page } from "@playwright/test";

const STORE_ID = "09700117";

async function mockSolveAPI(page: Page, authorizeFails = false) {
  await page.route("**/api/**", async (route) => {
    const request = route.request();
    const url = new URL(request.url());

    if (url.pathname === "/api/foods") {
      await route.fulfill({ json: { foods: [{ name: "Chicken breast", protein_g_per_100g: 31 }] } });
      return;
    }
    if (url.pathname === "/api/targets") {
      expect(request.method()).toBe("POST");
      const body = request.postDataJSON();
      expect(body).toMatchObject({ protein_g_daily: 180, budget_cents_weekly: 12000 });
      expect(body).not.toHaveProperty("store_id");
      await route.fulfill({ json: { capability_token: "test-capability", target: {
        id: 42, label: "web", protein_g_daily: 180, carbs_g_daily: 200,
        fat_g_daily: 60, calories_max_daily: null, budget_cents_weekly: 12000,
        store_id: STORE_ID, diet_tags: [], exclude_food_ids: [],
        created_at: "2026-08-02T12:00:00Z",
      } } });
      return;
    }
    if (url.pathname === "/api/solve") {
		expect(request.headers()["authorization"]).toBe("Bearer test-capability");
      expect(request.postDataJSON()).toEqual({ target_id: 42 });
      await route.fulfill({ json: { basket: {
        status: "optimal",
        items: [{ product_id: 7, product_name: "Chicken breast", food_name: "Chicken breast", packs: 2, grams: 1000, cost_cents: 1298 }],
        total_cost_cents: 1298,
        achieved: { protein_g: 1260, carbs_g: 1400, fat_g: 420, calories: 14500 },
        solve_seconds: 0.004,
      } } });
      return;
    }
    if (url.pathname === "/api/kroger/authorize") {
      expect(request.method()).toBe("POST");
      expect(request.postDataJSON()).toEqual({ target_id: 42 });
		expect(request.headers()["authorization"]).toBe("Bearer test-capability");
      if (authorizeFails) {
        await route.fulfill({
          status: 403,
          json: { error: { code: "invalid_origin", message: "request origin is not allowed" } },
        });
      } else {
        await route.fulfill({ json: { authorize_url: "/kroger-login" } });
      }
      return;
    }
    throw new Error(`Unexpected API request: ${request.method()} ${url.pathname}`);
  });
}

test("solves against the fixed University Place catalog", async ({ page }) => {
  await mockSolveAPI(page);
  await page.goto("/");
  await expect(page.getByText("Harris Teeter · University Place")).toBeVisible();
  await expect(page.getByText(/2110 S\. Estes Drive/)).toBeVisible();
  await page.getByRole("button", { name: "Find the cheapest basket" }).click();
  await expect(page.getByRole("heading", { name: "Your least-cost basket" })).toBeVisible();
  await expect(page.locator(".result-price", { hasText: "$12.98" })).toBeVisible();
  await expect(page.getByText(/of \$120\.00 budget/)).toBeVisible();
  await expect(page.getByRole("button", { name: "Turn this into a week of meals" })).toHaveCount(0);
  const cartEnabled = process.env.NEXT_PUBLIC_KROGER_CART === "true";
  await expect(page.getByRole("button", { name: "Add to my Kroger cart" })).toHaveCount(cartEnabled ? 1 : 0);
  await expect(page.getByRole("heading", { name: "Take it with you" })).toHaveCount(cartEnabled ? 1 : 0);
});

test("does not claim fields are highlighted when the store catalog is unavailable", async ({ page }) => {
  await page.route("**/api/**", async (route) => {
    const url = new URL(route.request().url());
    if (url.pathname === "/api/foods") {
      await route.fulfill({ json: { foods: [] } });
      return;
    }
    if (url.pathname === "/api/targets") {
      await route.fulfill({ json: { capability_token: "test-capability", target: { id: 42 } } });
      return;
    }
    if (url.pathname === "/api/solve") {
      await route.fulfill({
        status: 422,
        json: {
          error: {
            code: "validation_failed",
            message: "the request contained invalid values",
            fields: { store_id: "no available products" },
          },
        },
      });
      return;
    }
    throw new Error(`Unexpected API request: ${route.request().method()} ${url.pathname}`);
  });

  await page.goto("/");
  await page.getByRole("button", { name: "Find the cheapest basket" }).click();
  await expect(page.getByRole("heading", { name: "Store catalog unavailable" })).toBeVisible();
  await expect(page.getByText("Check the highlighted fields")).toHaveCount(0);
});

test("shows and clears the cart callback result", async ({ page }) => {
  await page.route("**/api/foods", (route) => route.fulfill({ json: { foods: [] } }));
  await page.goto("/?cart=success");
  await expect(page.getByText("Basket added to your Kroger cart")).toBeVisible();
  await expect(page).toHaveURL(/\/$/);
});

test("translates a safe cart callback code into recovery copy", async ({ page }) => {
  await page.route("**/api/foods", (route) => route.fulfill({ json: { foods: [] } }));
  await page.goto("/?cart_error=expired_state");
  await expect(page.getByText("The Kroger authorization took too long. Please try again.")).toBeVisible();
  await expect(page).toHaveURL(/\/$/);
});

test("keeps the basket visible when cart initiation fails", async ({ page }) => {
  test.skip(process.env.NEXT_PUBLIC_KROGER_CART !== "true", "cart UI is disabled by default");
  await mockSolveAPI(page, true);
  await page.goto("/");
  await page.getByRole("button", { name: "Find the cheapest basket" }).click();
  await page.getByRole("button", { name: "Add to my Kroger cart" }).click();
  await expect(page.getByRole("heading", { name: "Could not add the basket" })).toBeVisible();
  await expect(page.getByText(/request did not come from this application/)).toBeVisible();
  await expect(page.getByRole("heading", { name: "Shopping list" })).toBeVisible();
});

test("opens Kroger authorization in a popup", async ({ page }) => {
  test.skip(process.env.NEXT_PUBLIC_KROGER_CART !== "true", "cart UI is disabled by default");
  await mockSolveAPI(page);
  await page.goto("/");
  await page.getByRole("button", { name: "Find the cheapest basket" }).click();

  const popupPromise = page.waitForEvent("popup");
  await page.getByRole("button", { name: "Add to my Kroger cart" }).click();
  const popup = await popupPromise;
  await popup.waitForURL("**/kroger-login");

  await expect(page.getByRole("heading", { name: "Shopping list" })).toBeVisible();
  await popup.close();
});
