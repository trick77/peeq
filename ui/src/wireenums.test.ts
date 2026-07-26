import { readFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";
import {
  AVAILABILITIES,
  COOKIE_STATUSES,
  JOB_STATES,
  ROLES,
  SUMMARY_JOB_STATES,
  SUMMARY_PHASE_NAMES,
  SUMMARY_STATUSES,
  VIDEO_STATUSES,
} from "./api/enums";

// Drift guards for every wire enum named in Go by #196, mirroring what
// enumsync.test.ts already does for the category enum.
//
// Together with backend/internal/store/enums_test.go — which pins the same Go
// constants to their SQL CHECK constraints — this makes the whole chain
// checkable: a value cannot change in the migration, in Go, or here without one
// of the two tests failing.
//
// Note: this deliberately avoids the literal `new URL("../x", import.meta.url)`
// pattern, for the reason spelled out at length in enumsync.test.ts — Vite
// rewrites that shape into an asset URL under jsdom and fileURLToPath then
// throws. Resolve the directory first.
const HERE = dirname(fileURLToPath(import.meta.url));
const goFile = (rel: string) => resolve(HERE, "../../backend/", rel);

// goSliceValues reads an ordered `var <name> = []Elem{ ConstA, ConstB }`
// declaration — `[]string` for most sets, `[]Role` for auth.Roles — and
// resolves each entry through the `ConstA = "value"` constants declared in the
// same file.
//
// Two steps rather than one because the Go slices list constant NAMES, not
// literals — scraping the slice alone would compare TypeScript strings against
// Go identifiers and pass on nothing. Going through the slice (rather than
// scanning the const block directly) is what also pins the ORDER, which matters
// for SUMMARY_PHASE_NAMES: the Player renders "step N of 4" off the index.
function goSliceValues(file: string, sliceName: string): string[] {
  const source = readFileSync(goFile(file), "utf8");

  // The element type is matched loosely (`[]string` or `[]Role`): auth.Roles
  // is the one set Go gives a defined type, and it is still the same shape.
  const block = source.match(
    new RegExp(`var ${sliceName} = \\[\\]\\w+\\{([\\s\\S]*?)\\n\\}`),
  );
  if (!block) {
    throw new Error(`could not find "var ${sliceName}" in ${file}`);
  }
  const names = [...block[1].matchAll(/^\s*(\w+),/gm)].map((m) => m[1]);
  if (names.length === 0) {
    throw new Error(`"var ${sliceName}" in ${file} listed no constants`);
  }

  return names.map((name) => {
    // Tolerate an optional type between the name and "=", so a typed const
    // block (RoleAdmin Role = "admin") reads the same as an untyped one.
    const decl = source.match(
      new RegExp(`^\\s*${name}\\s+(?:\\w+\\s+)?=\\s*"([^"]*)"`, "m"),
    );
    if (!decl) {
      throw new Error(
        `${sliceName} names ${name}, but ${file} does not define it`,
      );
    }
    return decl[1];
  });
}

describe("wire enum sync", () => {
  // Order is compared, not just membership: several of these drive positional
  // UI (the phase meter's "N/4"), and a reorder is exactly the kind of change
  // that looks harmless in a diff.
  const cases: Array<{ name: string; ts: readonly string[]; go: string[] }> = [
    {
      name: "videos.status",
      ts: VIDEO_STATUSES,
      go: goSliceValues("internal/videos/status.go", "Statuses"),
    },
    {
      name: "videos.summary_status",
      ts: SUMMARY_STATUSES,
      go: goSliceValues("internal/videos/status.go", "SummaryStatuses"),
    },
    {
      name: "videos.availability",
      ts: AVAILABILITIES,
      go: goSliceValues("internal/videos/availability.go", "Availabilities"),
    },
    {
      name: "download_jobs.state",
      ts: JOB_STATES,
      go: goSliceValues("internal/jobs/state.go", "States"),
    },
    {
      name: "summary_jobs.state",
      ts: SUMMARY_JOB_STATES,
      go: goSliceValues("internal/summaryjobs/state.go", "States"),
    },
    {
      name: "settings.cookie_status",
      ts: COOKIE_STATUSES,
      go: goSliceValues("internal/settings/cookiestatus.go", "CookieStatuses"),
    },
    {
      name: "summarize phase",
      ts: SUMMARY_PHASE_NAMES,
      go: goSliceValues("internal/summarize/phase.go", "Phases"),
    },
    {
      name: "auth.Role",
      ts: ROLES,
      go: goSliceValues("internal/auth/model.go", "Roles"),
    },
  ];

  for (const c of cases) {
    it(`matches the Go values and order for ${c.name}`, () => {
      // Given: the Go side parsed off disk.
      // Then: same values, same order.
      expect(c.go.length).toBeGreaterThan(0);
      expect([...c.ts]).toEqual(c.go);
    });
  }

  // The scraper fails loudly rather than returning an empty set, so a renamed
  // slice can never make a case pass vacuously. Asserted, not assumed — an
  // empty-set bug would silently disable every case above.
  it("throws when the Go declaration it reads is missing", () => {
    expect(() =>
      goSliceValues("internal/videos/status.go", "NoSuchSlice"),
    ).toThrow(/could not find/);
  });
});
