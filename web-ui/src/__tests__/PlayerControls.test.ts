import { describe, expect, it } from "vitest";
import { isSubtitleLanguageAllowed } from "../PlayerControls";

describe("isSubtitleLanguageAllowed", () => {
  it("allows tracks while preferences are empty or still loading", () => {
    expect(isSubtitleLanguageAllowed("eng", [])).toBe(true);
  });

  it("filters tracks when preferences are explicitly configured", () => {
    expect(isSubtitleLanguageAllowed("ENG", ["eng"])).toBe(true);
    expect(isSubtitleLanguageAllowed("spa", ["eng"])).toBe(false);
  });
});
