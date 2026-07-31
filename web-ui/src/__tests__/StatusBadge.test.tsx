import { render } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { StatusBadge } from "../ui/StatusBadge";
import type { StatusTone } from "../ui/StatusBadge";

describe("StatusBadge", () => {
  it("renders its label", () => {
    const { getByText } = render(<StatusBadge tone="good">online</StatusBadge>);
    expect(getByText("online")).toBeTruthy();
  });

  it("applies a distinct class per tone alongside the shared base class", () => {
    const tones: StatusTone[] = ["good", "warn", "danger", "neutral"];
    const seen = new Set<string>();
    let base = "";
    for (const tone of tones) {
      const { container } = render(<StatusBadge tone={tone}>{tone}</StatusBadge>);
      const classes = (container.firstElementChild as HTMLElement).className.split(" ").filter(Boolean);
      // One base class shared by every tone, plus exactly one tone class.
      expect(classes, tone).toHaveLength(2);
      base = base || classes[0];
      expect(classes[0], tone).toBe(base);
      seen.add(classes[1]);
    }
    expect(seen.size).toBe(tones.length);
  });

  it("defaults to the neutral tone", () => {
    const { container: withDefault } = render(<StatusBadge>x</StatusBadge>);
    const { container: explicit } = render(<StatusBadge tone="neutral">x</StatusBadge>);
    expect((withDefault.firstElementChild as HTMLElement).className).toBe(
      (explicit.firstElementChild as HTMLElement).className,
    );
  });

  it("sets title only when given one", () => {
    const { container: withTitle } = render(<StatusBadge tone="warn" title="offline for 4m">offline</StatusBadge>);
    expect(withTitle.firstElementChild?.getAttribute("title")).toBe("offline for 4m");
    const { container: without } = render(<StatusBadge tone="warn">offline</StatusBadge>);
    expect(without.firstElementChild?.hasAttribute("title")).toBe(false);
  });
});
