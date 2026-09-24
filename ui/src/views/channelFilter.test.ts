import { describe, it, expect } from "vitest";
import { channelMatchesFilter } from "./channelFilter";
import type { Channel } from "../api/types";

const ch = (o: Partial<Channel>): Channel =>
  ({
    id: "c",
    name: "C",
    added: true,
    subscribed: false,
    autodownload: false,
    dormant: false,
    ...o,
  }) as Channel;

describe("channelMatchesFilter", () => {
  it.each([
    ["all", ch({}), true],
    ["all", ch({ added: false }), true],
    ["subscribed", ch({ subscribed: true }), true],
    ["subscribed", ch({ subscribed: false }), false],
    ["notsubscribed", ch({ added: true, subscribed: false }), true],
    ["notsubscribed", ch({ added: true, subscribed: true }), false],
    // A download-only row is not "not subscribed": it was never added.
    ["notsubscribed", ch({ added: false, subscribed: false }), false],
    ["downloaded", ch({ added: false }), true],
    ["downloaded", ch({ added: true }), false],
    ["autodownload", ch({ autodownload: true, subscribed: true }), true],
    ["autodownload", ch({ autodownload: false, subscribed: true }), false],
  ] as const)("%s: %o → %s", (f, c, want) => {
    expect(channelMatchesFilter(c, f)).toBe(want);
  });
});
