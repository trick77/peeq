import { test } from "node:test";
import assert from "node:assert/strict";
import { cookieLine, toNetscape, isYouTubeDomain } from "./shared.js";

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
