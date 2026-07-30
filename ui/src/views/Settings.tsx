import { useEffect, useState, type FormEvent } from "react";
import { Icon } from "../icons";
import { Button, Spinner, t } from "../ui";
import {
  getSettings,
  updateSettings,
  putCookie,
  getAPITokenStatus,
  createAPIToken,
} from "../api/settings";
import { getYtdlpVersion, updateYtdlp, type YtdlpVersion } from "../api/ytdlp";
import { formatAgo } from "../format";
import { pauseYoutube, resumeYoutube } from "../api/downloads";
import type { Settings as SettingsType } from "../api/types";
import { DOT } from "../sep";

// PRESETS mirrors ytdlp.Presets exactly (backend/internal/ytdlp/format.go)
// plus the "custom" id Resolve special-cases — the format string shown
// under each preset here must stay byte-for-byte in sync with that table.
const PRESETS: { id: string; label: string; format: string }[] = [
  {
    id: "apple-1080p",
    label: "Apple 1080p",
    format: "bestvideo[height<=1080][vcodec*=avc1]+bestaudio[acodec*=mp4a]/mp4",
  },
  {
    id: "apple-720p",
    label: "Apple 720p",
    format: "bestvideo[height<=720][vcodec*=avc1]+bestaudio[acodec*=mp4a]/mp4",
  },
  {
    id: "best-mp4",
    label: "Best available MP4",
    format: "bestvideo+bestaudio/best",
  },
  { id: "custom", label: "Custom…", format: "write your own format string" },
];

// looksLikeNetscapeCookie is a client-side sanity check only — the
// authoritative check is cookie.Validate on the backend (see
// PUT /api/settings/cookie), whose error is surfaced separately below.
function looksLikeNetscapeCookie(text: string): boolean {
  const trimmed = text.trim();
  if (trimmed === "") return false;
  return trimmed
    .split("\n")
    .some(
      (line) => line.includes(".youtube.com") && line.split("\t").length >= 7,
    );
}

// ytdlpCheckNote says what peeq knows about newer yt-dlp releases, and how
// that knowledge is doing.
//
// The rail's update indicator can only appear when a check SUCCEEDED and
// found something, so on its own it leaves a hole: a check failing forever —
// no network, a blocked API, a rate limit — looks exactly like being up to
// date. This line is where that failure is admitted, next to the Update
// button that can act on it. It always renders, so the absence of a warning
// here means something.
function ytdlpCheckNote(v: YtdlpVersion | null): string {
  if (!v) return "Checking for newer releases.";
  if (v.check_error) {
    // The error string itself is a Go network/HTTP error — accurate but not
    // useful here. What the reader needs is that the answer beside it has
    // stopped refreshing, and how stale it therefore is.
    // The trailing full stop rides on `known`, not on the base sentence: a
    // check that has never once succeeded — the first-boot failure — has no
    // last-known release and no timestamp, and would otherwise render with a
    // doubled period.
    const when = v.checked_at ? `, seen ${formatAgo(v.checked_at)}` : "";
    const known = v.latest ? ` Last known release ${v.latest}${when}.` : "";
    return `Couldn't reach GitHub to check for newer releases.${known}`;
  }
  if (!v.latest) return "Checking for newer releases.";
  if (v.update_available) {
    return `${v.latest} is available ${DOT} checked ${formatAgo(v.checked_at)}.`;
  }
  return `Latest release ${v.latest} ${DOT} checked ${formatAgo(v.checked_at)}.`;
}

// Settings — cookie / format preset / rate-limit / retention / yt-dlp
// version, per the mockup's `.settings` block. The cookie body is never
// echoed back by the backend, so this view never pre-fills the textarea —
// only cookie_status/cookie_updated_at (via GET /api/settings) are ever
// displayed.
export function Settings() {
  const [settings, setSettingsState] = useState<SettingsType | null>(null);
  const [error, setError] = useState<string | null>(null);

  const [cookieText, setCookieText] = useState("");
  const [cookieSaving, setCookieSaving] = useState(false);
  const [cookieError, setCookieError] = useState<string | null>(null);

  const [customFormat, setCustomFormat] = useState("");
  const [limitRate, setLimitRate] = useState("");
  const [throttleBase, setThrottleBase] = useState(20);
  const [retentionDays, setRetentionDays] = useState(14);
  const [minVideoDuration, setMinVideoDuration] = useState(0);

  const [ytdlp, setYtdlp] = useState<YtdlpVersion | null>(null);
  const [ytdlpBusy, setYtdlpBusy] = useState(false);
  const [ytdlpError, setYtdlpError] = useState<string | null>(null);
  // ytdlpNote is the Update button's receipt. Without it a no-op update — the
  // common case — leaves the page visually identical to a dead button. It
  // persists until the next click rather than fading, so the answer is still
  // there when you look back at it.
  const [ytdlpNote, setYtdlpNote] = useState<string | null>(null);

  const [tokenPresent, setTokenPresent] = useState(false);
  const [tokenCreatedAt, setTokenCreatedAt] = useState("");
  // freshToken holds the plaintext returned by createAPIToken. It lives only
  // here: peeq stores a hash, so leaving this page loses it for good.
  const [freshToken, setFreshToken] = useState<string | null>(null);
  const [tokenConfirming, setTokenConfirming] = useState(false);
  const [tokenBusy, setTokenBusy] = useState(false);
  const [tokenCopied, setTokenCopied] = useState(false);
  const [tokenError, setTokenError] = useState<string | null>(null);

  function load() {
    getSettings()
      .then((s) => {
        setSettingsState(s);
        setCustomFormat(s.format_custom);
        setLimitRate(s.limit_rate);
        setThrottleBase(s.throttle_base_seconds);
        setRetentionDays(s.retention_days);
        setMinVideoDuration(s.min_video_duration_seconds);
        setError(null);
      })
      .catch((e: Error) => setError(e.message));
  }

  useEffect(() => {
    load();
    getYtdlpVersion()
      .then(setYtdlp)
      .catch(() => {});
  }, []);

  useEffect(() => {
    getAPITokenStatus()
      .then((status) => {
        setTokenPresent(status.present);
        setTokenCreatedAt(status.created_at ?? "");
      })
      .catch(() => {
        // A failed status read must not blank the whole Settings page; the
        // section falls back to its empty state.
        setTokenPresent(false);
      });
  }, []);

  async function handleSaveCookie(e: FormEvent) {
    e.preventDefault();
    setCookieSaving(true);
    setCookieError(null);
    try {
      const s = await putCookie(cookieText);
      setSettingsState(s);
      setCookieText("");
    } catch (err) {
      setCookieError((err as Error).message ?? "Failed to save cookie.");
    } finally {
      setCookieSaving(false);
    }
  }

  async function handlePickPreset(id: string) {
    if (!settings) return;
    const patch =
      id === "custom"
        ? { format_preset: id, format_custom: customFormat }
        : { format_preset: id };
    try {
      const s = await updateSettings(patch);
      setSettingsState(s);
    } catch (err) {
      setError((err as Error).message);
    }
  }

  async function handleSaveCustomFormat() {
    try {
      const s = await updateSettings({
        format_preset: "custom",
        format_custom: customFormat,
      });
      setSettingsState(s);
    } catch (err) {
      setError((err as Error).message);
    }
  }

  async function handleSaveLimitRate() {
    try {
      const s = await updateSettings({ limit_rate: limitRate });
      setSettingsState(s);
    } catch (err) {
      setError((err as Error).message);
    }
  }

  async function handleSaveThrottleBase() {
    try {
      const s = await updateSettings({ throttle_base_seconds: throttleBase });
      setSettingsState(s);
    } catch (err) {
      setError((err as Error).message);
    }
  }

  // commitRetention saves retentionDays via PUT /api/settings. Deliberately
  // NOT called from the range input's onChange (which fires on every
  // integer step while dragging — up to ~80 PUTs for a single drag from 14
  // to 90); onChange only updates the displayed value locally, and this
  // fires once the user releases the slider (onMouseUp/onKeyUp/onTouchEnd).
  async function commitRetention() {
    try {
      const s = await updateSettings({ retention_days: retentionDays });
      setSettingsState(s);
    } catch (err) {
      setError((err as Error).message);
    }
  }

  async function handleSaveMinVideoDuration() {
    try {
      const s = await updateSettings({
        min_video_duration_seconds: minVideoDuration,
      });
      setSettingsState(s);
    } catch (err) {
      setError((err as Error).message);
    }
  }

  // Unlike the text/number fields above there is no local mirror state to
  // commit on blur — the checkbox renders straight off settings, so the
  // response from updateSettings is the only place the new value comes from.
  async function handleToggleSubtitlesDefault(next: boolean) {
    try {
      const s = await updateSettings({ subtitles_default: next });
      setSettingsState(s);
    } catch (err) {
      setError((err as Error).message);
    }
  }

  async function handleToggleDirectStream(next: boolean) {
    try {
      const s = await updateSettings({ direct_stream_enabled: next });
      setSettingsState(s);
    } catch (err) {
      setError((err as Error).message);
    }
  }

  async function handleCreateToken() {
    setTokenBusy(true);
    setTokenError(null);
    try {
      const created = await createAPIToken();
      setFreshToken(created.token);
      setTokenPresent(true);
      setTokenCreatedAt(created.created_at);
      setTokenConfirming(false);
    } catch (err) {
      setTokenError(
        (err as Error).message ?? "Failed to create the API token.",
      );
    } finally {
      setTokenBusy(false);
    }
  }

  async function handleCopyToken() {
    if (!freshToken) return;
    try {
      await navigator.clipboard.writeText(freshToken);
    } catch {
      // Clipboard access can be denied; the token is selectable on screen.
    }
    setTokenCopied(true);
    setTimeout(() => setTokenCopied(false), 1600);
  }

  async function handleUpdateYtdlp() {
    setYtdlpBusy(true);
    setYtdlpError(null);
    setYtdlpNote(null);
    try {
      const res = await updateYtdlp();
      // The installed version moved, so whatever the last check concluded about
      // being behind is now settled — clear it here rather than waiting for the
      // next scheduled check, or the page would keep offering an update it just
      // performed. `latest` is left alone: it is still the newest release, and
      // the note below reads correctly against the new version.
      // Falling back to a bare report rather than keeping null: the version
      // fetch failing is precisely when a user presses Update, and dropping
      // the result would leave the display reading "unknown" straight after a
      // successful install.
      setYtdlp((prev) => ({
        ...prev,
        version: res.version,
        update_available: false,
      }));
      if (res.updated && res.previous_version) {
        setYtdlpNote(`Updated ${res.previous_version} → ${res.version}.`);
      } else if (res.updated) {
        setYtdlpNote(`Installed ${res.version}.`);
      } else {
        setYtdlpNote("Already on the latest version.");
      }
    } catch (err) {
      setYtdlpError((err as Error).message ?? "Update failed.");
    } finally {
      setYtdlpBusy(false);
    }
  }

  if (error && !settings) {
    return <div className="errline">{error}</div>;
  }
  if (!settings) {
    return (
      <p
        style={{
          display: "flex",
          alignItems: "center",
          gap: 8,
          color: "var(--color-faint)",
        }}
      >
        <Spinner size="15px" />
        Loading
      </p>
    );
  }

  const cookieHealthy = settings.cookie_status === "valid";
  const looksValid = looksLikeNetscapeCookie(cookieText);

  async function handleToggleYoutubePause(paused: boolean) {
    try {
      if (paused) await pauseYoutube();
      else await resumeYoutube();
      setSettingsState(await getSettings());
    } catch (err) {
      setError((err as Error).message);
    }
  }

  return (
    <div className="settings">
      <section className="sect">
        <h2>
          YouTube activity
          <span
            className={`status-line${settings.youtube_paused ? " warn" : ""}`}
          >
            <span className="led" />
            {settings.youtube_paused
              ? settings.youtube_pause_reason
                ? `Paused${DOT}auto`
                : "Paused"
              : "Active"}
          </span>
        </h2>
        <p className="desc">
          Pause all downloads, channel scans, and metadata fetches. Nothing
          leaves Peeq while paused.
        </p>
        <label className="channel-toggle">
          <input
            type="checkbox"
            checked={settings.youtube_paused}
            onChange={(e) => handleToggleYoutubePause(e.target.checked)}
          />
          {settings.youtube_paused
            ? "YouTube activity is paused"
            : "Pause all YouTube activity"}
        </label>
        {settings.youtube_paused && settings.youtube_pause_reason ? (
          <div className="warnline">
            <Icon
              name="warning"
              size="16px"
              style={{ color: "var(--color-danger)" }}
            />
            <span>{settings.youtube_pause_reason}</span>
          </div>
        ) : null}
        {settings.youtube_paused ? (
          <div className="field-row" style={{ marginTop: 12 }}>
            <Button
              type="button"
              onClick={() => handleToggleYoutubePause(false)}
            >
              Resume
            </Button>
          </div>
        ) : null}
      </section>

      <section className="sect">
        <h2>
          YouTube cookie
          <span className={`status-line${cookieHealthy ? "" : " warn"}`}>
            <span className="led" />
            {cookieHealthy ? "Active" : settings.cookie_status}
            {settings.cookie_updated_at
              ? `${DOT}updated ${new Date(settings.cookie_updated_at).toLocaleString()}`
              : ""}
          </span>
        </h2>
        <p className="desc">
          Paste your browser's YouTube cookies (Netscape format). Peeq keeps
          yt-dlp's rotated cookie fresh automatically. The pasted text is never
          shown back to you — only its status is.
        </p>
        <form onSubmit={handleSaveCookie}>
          <textarea
            className="cookiebox"
            spellCheck={false}
            value={cookieText}
            onChange={(e) => setCookieText(e.target.value)}
            placeholder={
              "# Netscape HTTP Cookie File\n.youtube.com\tTRUE\t/\tTRUE\t...\tSID\t..."
            }
            aria-label="YouTube cookie"
          />
          <div className="field-row">
            <Button
              type="submit"
              busy={cookieSaving}
              disabled={cookieText.trim() === ""}
            >
              {cookieSaving ? "Saving" : "Save cookie"}
            </Button>
            {cookieText.trim() !== "" ? (
              <span
                style={{
                  ...t.label,
                  color: looksValid
                    ? "var(--color-online)"
                    : "var(--color-danger)",
                }}
              >
                {looksValid
                  ? "Looks like a valid Netscape cookie file."
                  : "Doesn't look like a Netscape cookie file yet."}
              </span>
            ) : null}
          </div>
          {cookieError ? <div className="errline">{cookieError}</div> : null}
        </form>
        <div className="warnline">
          <Icon
            name="warning"
            size="16px"
            style={{ color: "var(--color-danger)" }}
          />
          <span>
            <b>No cookie, no calls.</b> Peeq never touches YouTube without a
            valid cookie — it pauses the queue and asks you to re-paste instead.
          </span>
        </div>
      </section>

      <section className="sect">
        <h2>
          API token
          {tokenPresent ? (
            <span className="status-line">
              <span className="led" />
              Active
            </span>
          ) : (
            <span className="status-line idle">
              <span className="led" />
              Not set up
            </span>
          )}
        </h2>
        <p className="desc">
          Lets the Peeq browser extension send your YouTube cookie automatically
          and add videos to your queue, so you never paste a cookie or a link by
          hand. The token can only write the cookie and enqueue a video — it
          cannot read your library.
        </p>

        {freshToken ? (
          <div className="reveal">
            <div className="rhead">
              <Icon name="warning" size="15px" />
              Copy this now — it won't be shown again
            </div>
            <div className="tokenfield">
              <code>{freshToken}</code>
              <div className="acts">
                <Button
                  type="button"
                  variant="ghost"
                  icon
                  aria-label={tokenCopied ? "Token copied" : "Copy token"}
                  title={tokenCopied ? "Copied" : "Copy"}
                  style={
                    tokenCopied ? { color: "var(--color-online)" } : undefined
                  }
                  onClick={handleCopyToken}
                >
                  <Icon name={tokenCopied ? "check" : "copy"} size="16px" />
                </Button>
              </div>
            </div>
            <p className="rfoot">
              Peeq stores only a hash of this token, so it can't show it to you
              again. If you lose it, generate a new one — the old one stops
              working.
            </p>
            <div className="field-row">
              <Button
                type="button"
                variant="secondary"
                onClick={() => setFreshToken(null)}
              >
                Done
              </Button>
            </div>
          </div>
        ) : tokenPresent ? (
          <>
            <p className="meta">
              {tokenCreatedAt
                ? `Created ${new Date(tokenCreatedAt).toLocaleString()}`
                : "Token is set up."}
            </p>
            <div className="field-row">
              <Button
                type="button"
                variant="secondary"
                disabled={tokenConfirming || tokenBusy}
                onClick={() => setTokenConfirming(true)}
              >
                Generate a new token
              </Button>
              <span className="meta">
                The current token stops working immediately.
              </span>
            </div>
            {tokenConfirming ? (
              <div className="warnline">
                <Icon
                  name="warning"
                  size="16px"
                  style={{ color: "var(--color-danger)" }}
                />
                <span style={{ flex: 1 }}>
                  Generate a new token? Your extension will stop sending cookies
                  until you paste the new one.
                </span>
                <Button
                  type="button"
                  variant="danger"
                  small
                  busy={tokenBusy}
                  onClick={handleCreateToken}
                >
                  Generate
                </Button>
                <Button
                  type="button"
                  variant="secondary"
                  small
                  onClick={() => setTokenConfirming(false)}
                >
                  Cancel
                </Button>
              </div>
            ) : null}
          </>
        ) : (
          <>
            <div className="empty">No API token yet.</div>
            <div className="field-row">
              <Button
                type="button"
                busy={tokenBusy}
                onClick={handleCreateToken}
              >
                {tokenBusy ? "Generating" : "Generate token"}
              </Button>
              <span className="meta">
                You'll see the token once, right after it's created.
              </span>
            </div>
          </>
        )}
        {tokenError ? <div className="errline">{tokenError}</div> : null}
      </section>

      <section className="sect">
        <h2>Download format</h2>
        <p className="desc">
          A yt-dlp format selector. Apple presets pick H.264 / AAC so videos
          play natively — no transcoding.
        </p>
        <div className="presets">
          {PRESETS.map((preset) => (
            <button
              key={preset.id}
              type="button"
              className={`preset${settings.format_preset === preset.id ? " on" : ""}`}
              onClick={() => handlePickPreset(preset.id)}
            >
              <span className="pn">{preset.label}</span>
              <code>{preset.format}</code>
            </button>
          ))}
        </div>
        {settings.format_preset === "custom" ? (
          <div className="ctrl" style={{ marginTop: 14 }}>
            <span className="lab">Custom format string</span>
            <input
              type="text"
              value={customFormat}
              onChange={(e) => setCustomFormat(e.target.value)}
              onBlur={handleSaveCustomFormat}
              placeholder="bestvideo+bestaudio/best"
            />
          </div>
        ) : null}
      </section>

      <section className="sect">
        <div className="row2">
          <div className="ctrl">
            <span className="lab">Download speed limit</span>
            <input
              type="text"
              value={limitRate}
              onChange={(e) => setLimitRate(e.target.value)}
              onBlur={handleSaveLimitRate}
              placeholder="4.5M"
            />
            <p className="retain-note">
              Passed to yt-dlp as <b>--limit-rate</b>. Leave blank for no cap.
            </p>
          </div>
          <div className="ctrl">
            <span className="lab">
              Minimum delay between YouTube calls (seconds)
            </span>
            <input
              type="number"
              min={0}
              step={1}
              value={throttleBase}
              onChange={(e) => setThrottleBase(Number(e.target.value))}
              onBlur={handleSaveThrottleBase}
              aria-label="Minimum delay between YouTube calls (seconds)"
            />
            <p className="retain-note">
              The backend enforces a <b>20s hard floor</b> regardless of this
              value.
            </p>
          </div>
          <div className="ctrl">
            <span className="lab">
              Ignore channel videos shorter than (seconds)
            </span>
            <input
              type="number"
              min={0}
              step={1}
              value={minVideoDuration}
              onChange={(e) => setMinVideoDuration(Number(e.target.value))}
              onBlur={handleSaveMinVideoDuration}
              aria-label="Ignore channel videos shorter than (seconds)"
            />
            <p className="retain-note">
              Channel scans skip videos shorter than this (e.g. Shorts).{" "}
              <b>0</b> disables the filter.
            </p>
          </div>
          <div className="ctrl">
            <span className="lab">yt-dlp version</span>
            <div style={{ display: "flex", alignItems: "center", gap: 10 }}>
              <span className="mono" style={t.label}>
                {ytdlp?.version || "unknown"}
              </span>
              <Button
                type="button"
                variant="secondary"
                small
                busy={ytdlpBusy}
                onClick={handleUpdateYtdlp}
              >
                {ytdlpBusy ? "Updating" : "Update"}
              </Button>
            </div>
            <p className="retain-note">{ytdlpCheckNote(ytdlp)}</p>
            {ytdlpError ? <p className="retain-note">{ytdlpError}</p> : null}
            {ytdlpNote ? <p className="retain-note">{ytdlpNote}</p> : null}
          </div>
        </div>
      </section>

      <section className="sect">
        <h2>Playback</h2>
        <p className="desc">
          How videos start when you open them in the player.
        </p>
        {/* Each toggle and the note explaining it are one .toggle-field, so the
            note is tied to its own control rather than floating equidistant
            between two of them. */}
        <div className="toggle-field">
          <label className="channel-toggle">
            <input
              type="checkbox"
              checked={settings.subtitles_default}
              onChange={(e) => handleToggleSubtitlesDefault(e.target.checked)}
            />
            Show subtitles by default
          </label>
          <p className="retain-note">
            Videos that have subtitles start with them on. The player&rsquo;s
            subtitles button still works per video — flipping it there changes
            this setting too.
          </p>
        </div>
        <div className="toggle-field">
          <label className="channel-toggle">
            <input
              type="checkbox"
              checked={settings.direct_stream_enabled}
              onChange={(e) => handleToggleDirectStream(e.target.checked)}
            />
            Allow direct playback links (for AirPlay)
          </label>
          <p className="retain-note">
            Your TV opens the video itself and can&rsquo;t sign in as you, so
            Peeq creates a private link that works without signing in. Links
            expire after 12 hours, and switching this off kills every one at
            once. Only Apple 1080p and 720p downloads play on a TV.
          </p>
        </div>
      </section>

      <section className="sect">
        <h2>Automatic cleanup</h2>
        <p className="desc">
          Keep the disk from filling up — the video file of a watched video is
          removed on its own once it is old enough.
        </p>
        <div className="slider-row">
          <span className="lab" style={{ margin: 0 }}>
            Remove file after
          </span>
          <input
            type="range"
            min={1}
            max={90}
            value={retentionDays}
            onChange={(e) => setRetentionDays(Number(e.target.value))}
            onMouseUp={commitRetention}
            onTouchEnd={commitRetention}
            onKeyUp={commitRetention}
            aria-label="Retention days"
          />
          <span className="val">{retentionDays} days</span>
        </div>
        {/* "Delete" and "expire" both read as "the whole video goes away",
            which is not what happens: cleanup reclaims the media file and
            nothing else. Saying so is what makes the setting safe to turn
            down — the cost of a short retention is a re-download, not a lost
            summary. */}
        <p className="retain-note">
          <b>Only the video file goes.</b> The summary, transcript, chapters and
          poster stay, the video keeps turning up in search, and you can
          re-download it whenever you want.
        </p>
        <p className="retain-note">
          <b>Unwatched videos are never touched.</b>{" "}
          <b>Favorites are kept forever</b>, even after you watch them. Only
          watched, un-favorited videos expire.
        </p>
      </section>
    </div>
  );
}
