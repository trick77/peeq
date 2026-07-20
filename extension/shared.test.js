import { test } from "node:test";
import assert from "node:assert/strict";
import { cookieLine, toNetscape, isYouTubeDomain } from "./shared.js";
import {
  GATE_COOKIE_NAMES, DISPLAY_COOKIE_NAMES,
  selectYouTubeCookies, hasSessionCookie, countDisplayCookies,
} from "./shared.js";

// Chrome's real cookie shape: expirationDate is a FLOAT, session cookies omit
// it entirely, domain carries a leading dot, and httpOnly/secure are distinct.
// Stubs that pre-truncate or collapse those fields would make the code under
// test structurally untestable (see PR #15).
const SID = {
  domain: ".youtube.com", path: "/", name: "SID", value: "abc123",
  secure: true, httpOnly: true, expirationDate: 1819099943.123456,
};
const PREF = {
  domain: ".youtube.com", path: "/", name: "PREF", value: "f6=40000000",
  secure: false, httpOnly: false, expirationDate: 1819099943.9,
};
const SESSION_ONLY = {
  domain: "www.youtube.com", path: "/", name: "YSC", value: "xyz",
  secure: true, httpOnly: true,
};

test("cookieLine writes secure in column 4, not httpOnly", () => {
  // PREF is httpOnly:false secure:false; SID is httpOnly:true secure:true.
  // A serializer that wrote httpOnly into column 4 would still pass on SID,
  // so the discriminating case is a cookie where the two differ.
  const mixed = { ...PREF, secure: true, httpOnly: false };
  const fields = cookieLine(mixed).split("\t");
  assert.equal(fields.length, 7);
  assert.equal(fields[3], "TRUE", "column 4 must be `secure`");
});

test("cookieLine marks httpOnly with the #HttpOnly_ domain prefix", () => {
  assert.ok(cookieLine(SID).startsWith("#HttpOnly_.youtube.com\t"));
  assert.ok(cookieLine(PREF).startsWith(".youtube.com\t"));
});

test("cookieLine sets includeSubdomains from the leading dot", () => {
  assert.equal(cookieLine(PREF).split("\t")[1], "TRUE");
  assert.equal(cookieLine(SESSION_ONLY).split("\t")[1], "FALSE");
});

test("cookieLine truncates the float expiry to an integer", () => {
  assert.equal(cookieLine(PREF).split("\t")[4], "1819099943");
});

test("cookieLine writes 0 for a session cookie with no expirationDate", () => {
  assert.equal(cookieLine(SESSION_ONLY).split("\t")[4], "0");
});

test("toNetscape emits a header and one line per cookie", () => {
  const out = toNetscape([SID, PREF]);
  const lines = out.split("\n");
  assert.ok(lines[0].startsWith("# Netscape HTTP Cookie File"));
  const data = lines.filter((l) => l.trim() !== "" && !l.startsWith("# "));
  assert.equal(data.length, 2);
  assert.ok(out.endsWith("\n"), "file must end with a newline");
});

test("toNetscape uses tabs, never spaces, as separators", () => {
  for (const line of toNetscape([SID, PREF]).split("\n")) {
    if (line.trim() === "" || line.startsWith("# ")) continue;
    assert.equal(line.split("\t").length, 7, `not 7 tab fields: ${line}`);
  }
});

test("isYouTubeDomain accepts youtube.com and subdomains, rejects lookalikes", () => {
  assert.equal(isYouTubeDomain(".youtube.com"), true);
  assert.equal(isYouTubeDomain("youtube.com"), true);
  assert.equal(isYouTubeDomain("www.youtube.com"), true);
  assert.equal(isYouTubeDomain("notyoutube.com"), false);
  assert.equal(isYouTubeDomain("youtube.com.evil.test"), false);
  assert.equal(isYouTubeDomain("google.com"), false);
});

const yt = (name) => ({
  domain: ".youtube.com", path: "/", name, value: "v",
  secure: true, httpOnly: true, expirationDate: 1819099943.5,
});

test("the gate is exactly Validate's trio", () => {
  assert.deepEqual([...GATE_COOKIE_NAMES].sort(),
    ["SID", "__Secure-1PSID", "__Secure-3PSID"].sort());
});

test("the display set is the five reported names", () => {
  assert.equal(DISPLAY_COOKIE_NAMES.length, 5);
  for (const gated of GATE_COOKIE_NAMES) {
    assert.ok(DISPLAY_COOKIE_NAMES.includes(gated),
      `${gated} gates the button so it must also be displayed`);
  }
});

test("selectYouTubeCookies keeps only YouTube entries", () => {
  const mixed = [yt("SID"), { ...yt("SID"), domain: ".google.com" }];
  const kept = selectYouTubeCookies(mixed);
  assert.equal(kept.length, 1);
  assert.equal(kept[0].domain, ".youtube.com");
});

test("hasSessionCookie is true for any one of the trio", () => {
  for (const name of GATE_COOKIE_NAMES) {
    assert.equal(hasSessionCookie([yt(name)]), true, `${name} should gate open`);
  }
});

test("hasSessionCookie is false for a jar with no session cookie", () => {
  // The anonymous-jar case: sending this would overwrite peeq's good cookie.
  assert.equal(hasSessionCookie([yt("PREF"), yt("VISITOR_INFO1_LIVE")]), false);
  assert.equal(hasSessionCookie([]), false);
});

test("hasSessionCookie ignores a session cookie on a non-YouTube domain", () => {
  assert.equal(hasSessionCookie([{ ...yt("SID"), domain: ".google.com" }]), false);
});

test("countDisplayCookies counts only display-set members, without double counting", () => {
  assert.equal(countDisplayCookies([yt("SID"), yt("SAPISID"), yt("PREF")]), 2);
  assert.equal(countDisplayCookies([yt("SID"), yt("SID")]), 1, "duplicates count once");
  assert.equal(countDisplayCookies([]), 0);
});

test("a 3-of-5 jar still passes the gate", () => {
  // The display set is informational; only the trio gates. A jar missing
  // SAPISID/LOGIN_INFO is perfectly valid and must not disable the button.
  const jar = [yt("SID"), yt("__Secure-1PSID"), yt("__Secure-3PSID")];
  assert.equal(countDisplayCookies(jar), 3);
  assert.equal(hasSessionCookie(jar), true);
});
