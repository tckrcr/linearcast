import { expect, test } from "@playwright/test";
import { stubScheduleBuilderReads } from "./schedule-builder-fixture";

test("draggable episode rows keep the Add action on the same row", async ({ page }) => {
  test.fail(
    true,
    "Known Slice 6 regression: the literal is-draggable class does not match the CSS Module selector.",
  );
  await stubScheduleBuilderReads(page);

  await page.goto("/admin");
  await page.getByRole("button", { name: "Create channel" }).click();
  await page.getByRole("button", { name: "Shows" }).click();
  await page.getByLabel("Search shows and episodes").fill("S01E01");

  const row = page.locator("li.is-draggable").first();
  await expect(row).toBeVisible();

  const childCenters = await row.locator(":scope > *").evaluateAll((children) =>
    children.map((child) => {
      const rect = child.getBoundingClientRect();
      return rect.top + rect.height / 2;
    }),
  );
  expect(childCenters).toHaveLength(4);
  expect(Math.max(...childCenters) - Math.min(...childCenters)).toBeLessThanOrEqual(2);
});
