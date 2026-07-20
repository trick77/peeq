// Regenerates backend/internal/cookie/testdata/extension_output.txt — the
// fixture the Go parser test reads. Run after any serializer change:
//   node testdata/generate_fixture.js > ../backend/internal/cookie/testdata/extension_output.txt
// The cookie shapes here mirror what chrome.cookies.getAll really returns.
import { toNetscape, selectYouTubeCookies } from "../shared.js";

// SID, HSID and APISID Secure flags below mirror a real YouTube cookie
// export (Netscape format, exported from the maintainer's browser) and must
// not be "tidied" to true — a fixture that looks tidier than reality is what
// hid the https-only host_permissions bug this project shipped once already.
const cookies = [
  { domain: ".youtube.com", path: "/", name: "SID", value: "g.a000abc",
    secure: false, httpOnly: true, expirationDate: 1819099943.123456 },
  { domain: ".youtube.com", path: "/", name: "HSID", value: "g.a000hsid",
    secure: false, httpOnly: true, expirationDate: 1819099943.111111 },
  { domain: ".youtube.com", path: "/", name: "APISID", value: "g.a000apisid",
    secure: false, httpOnly: false, expirationDate: 1819099943.222222 },
  { domain: ".youtube.com", path: "/", name: "__Secure-1PSID", value: "g.a000def",
    secure: true, httpOnly: true, expirationDate: 1819099943.987654 },
  { domain: ".youtube.com", path: "/", name: "__Secure-3PSID", value: "g.a000ghi",
    secure: true, httpOnly: true, expirationDate: 1819099943.5 },
  { domain: ".youtube.com", path: "/", name: "SAPISID", value: "sapi123",
    secure: true, httpOnly: false, expirationDate: 1819099943.0 },
  { domain: ".youtube.com", path: "/", name: "LOGIN_INFO", value: "AFmmF2s",
    secure: true, httpOnly: true, expirationDate: 1819099943.25 },
  { domain: ".youtube.com", path: "/", name: "PREF", value: "f6=40000000",
    secure: false, httpOnly: false, expirationDate: 1819099943.75 },
  // Session cookie: no expirationDate at all.
  { domain: "www.youtube.com", path: "/", name: "YSC", value: "sessionval",
    secure: true, httpOnly: true },
  // Non-YouTube: must be filtered out before serialization.
  { domain: ".google.com", path: "/", name: "SID", value: "should-not-appear",
    secure: true, httpOnly: true, expirationDate: 1819099943.0 },
];

process.stdout.write(toNetscape(selectYouTubeCookies(cookies)));
