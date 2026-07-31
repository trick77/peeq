import { describe, it, expect, vi, beforeEach } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";
import {
  render,
  screen,
  waitFor,
  fireEvent,
  within,
} from "@testing-library/react";
import { App } from "./App";
import {
  downloadsStatus,
  resumeYoutube,
  listDownloads,
  getMe,
  listPending,
  listSummaries,
  cancelDownload,
  cookieHealth,
  streamDownloads,
  listVideos,
  getPlaybackState,
} from "./api";
import { addDownload } from "./api/downloads";
import { searchVideos } from "./api/search";
// The Inbox reads its own module, not the barrel — the two listPending mocks
// are separate, and this is the one the rendered view actually calls.
import { listPending as listPendingApi } from "./api/pending";
import type { Job, PendingItem, User, Video } from "./api/types";

// An inbox video: discovered, summarised, but with no file yet. Its page is
// /video/<id> just like a downloaded video's.
const inboxItem: PendingItem = {
  video_id: "p1",
  channel_id: "c1",
  channel_name: "Channel One",
  title: "A video worth reading about",
  duration_seconds: 610,
  url: "https://youtube.com/watch?v=p1",
  thumbnail_url: "https://img.example/p1.jpg",
  published_at: "2026-07-28",
  discovered_at: "2026-07-29 09:00:00",
  summary_status: "done",
  auto_summary: true,
};

describe("App (static)", () => {
  it("renders Peeq", () => {
    const html = renderToStaticMarkup(<App />);
    expect(html).toContain("Peeq");
  });
});

// The barrel mock covers everything App loads on mount, plus the Library view's
// own imports so the library path renders without crashing. getPlaybackState is
// the server-side "now playing" pointer App uses as the rail's fallback; it
// defaults to empty here, which is the "nothing playing" case.
vi.mock("./api", () => ({
  getMe: vi.fn().mockResolvedValue({ id: "u1", email: "a@b.c" }),
  listDownloads: vi.fn().mockResolvedValue([]),
  cookieHealth: vi.fn().mockResolvedValue({ status: "valid" }),
  downloadsStatus: vi
    .fn()
    .mockResolvedValue({ paused: false, low_disk: false }),
  resumeYoutube: vi.fn().mockResolvedValue(undefined),
  listPending: vi.fn().mockResolvedValue([]),
  listSummaries: vi.fn().mockResolvedValue([]),
  cancelDownload: vi.fn().mockResolvedValue(undefined),
  streamDownloads: vi.fn().mockResolvedValue(undefined),
  listVideos: vi.fn().mockResolvedValue([]),
  // Up next fetches the timed schedule; History fetches the log. Both are one
  // rail click away, so the barrel needs them even in tests that never open
  // those pages.
  listUpcoming: vi.fn().mockResolvedValue({ items: [], truncated: 0 }),
  // Up next's skip action on a scheduled row. App never calls these itself, but
  // UpNext resolves them at module scope, so the mock has to carry them.
  skipScheduledScan: vi.fn(),
  skipScheduledMeta: vi.fn(),
  listActivity: vi
    .fn()
    .mockResolvedValue({ events: [], has_more: false, retained_max: 2000 }),
  getSettings: vi.fn().mockResolvedValue({}),
  setFavorite: vi.fn(),
  setWatched: vi.fn(),
  getPlaybackState: vi.fn().mockResolvedValue({ video_id: "" }),
}));

const mockVideo = vi.hoisted<Video>(() => ({
  id: "v1",
  url: "https://youtu.be/v1",
  title: "The Trillion Dollar Equation",
  channel_id: "chan1",
  channel_name: "Veritasium",
  duration_seconds: 1684,
  has_thumbnail: false,
  has_media: true,
  availability: "available",
  status: "downloaded",
  watched: false,
  resume_position_seconds: 42,
  state_version: 1,
  favorite: false,
  summary: "",
  chapters: [],
  key_points: [],
  summary_status: "pending",
  audio_language: "",
  has_subtitles: false,
  category: "uncategorized",
}));

vi.mock("./api/videos", () => ({
  getVideo: vi.fn().mockResolvedValue(mockVideo),
  setFavorite: vi.fn(),
  setWatched: vi.fn(),
  setResume: vi.fn().mockResolvedValue(42),
  deleteVideo: vi.fn(),
  redownload: vi.fn(),
  listVideos: vi.fn().mockResolvedValue([]),
  streamUrl: (id: string) => `/api/videos/${id}/stream`,
  thumbnailUrl: (id: string) => `/api/videos/${id}/thumbnail`,
  // The Inbox card's poster. Only reached once a test puts an item in the
  // inbox — until then listPending resolves empty and no card renders.
  pendingThumbnailUrl: (id: string) => `/api/pending/${id}/thumbnail`,
}));

vi.mock("./api/search", () => ({
  subtitlesUrl: (id: string) => `/api/videos/${id}/subtitles`,
  reprocess: vi.fn(),
  searchVideos: vi.fn().mockResolvedValue([]),
}));

vi.mock("./api/downloads", async (importOriginal) => ({
  ...(await importOriginal<typeof import("./api/downloads")>()),
  addDownload: vi.fn(),
}));

vi.mock("./api/channels", () => ({
  addChannel: vi.fn(),
  listChannels: vi.fn().mockResolvedValue([]),
  updateChannel: vi.fn(),
  subscribeChannel: vi.fn(),
  unsubscribeChannel: vi.fn(),
  deleteChannel: vi.fn(),
  getChannel: vi.fn(),
  scanChannel: vi.fn(),
  listAutoUnsubscribedChannels: vi.fn().mockResolvedValue([]),
  dismissDormantChannel: vi.fn(),
  resubscribeChannel: vi.fn(),
  channelAvatarUrl: (id: string) => `/api/channels/${id}/avatar`,
  channelBannerUrl: (id: string) => `/api/channels/${id}/banner`,
}));

vi.mock("./api/pending", () => ({
  listPending: vi.fn().mockResolvedValue([]),
  downloadPending: vi.fn(),
  ignorePending: vi.fn(),
}));

// Reset the URL before every test in this file. useRoute() derives the
// initial view from window.location.pathname, and jsdom persists location
// across tests in a file — without this, the deep-link tests below (which
// enter at /video/<id>, /bogus, …) would leak their path into later
// describes that assume they start at "/". A root-level hook applies to
// every describe regardless of textual position.
beforeEach(() => {
  window.history.replaceState(null, "", "/");
  // Empty the inbox for the same reason. This is the module the rendered Inbox
  // actually calls, and a mockResolvedValue outlives clearAllMocks (which drops
  // call history, not implementations) — so without this, the one test that
  // seeds an item would leave it in every later test's inbox.
  vi.mocked(listPendingApi).mockResolvedValue([]);
});

// The dock only starts its 3s poll once `jobs` already holds an active
// entry, so adding the first video to an empty dock used to leave it
// invisible until a manual reload: App passed Add a no-op onQueued, and
// the SSE progress handler can't help when the job is queued but not yet
// downloading. This asserts the add itself refreshes the queue.
describe("App dock bootstrap", () => {
  beforeEach(() => {
    // testing-library truncates its failure DOM dump at 7000 chars by
    // default, which cuts off the rail's lower nav groups and makes a
    // CI-only failure here impossible to diagnose from the log.
    process.env.DEBUG_PRINT_LIMIT = "100000";
    vi.clearAllMocks();
    // Restate every mock this test depends on rather than inheriting
    // whatever an earlier describe left behind — the paused-banner tests
    // above overwrite downloadsStatus, and clearAllMocks only drops call
    // history, not implementations.
    vi.mocked(getMe).mockResolvedValue({ id: "u1", email: "a@b.c" } as User);
    vi.mocked(downloadsStatus).mockResolvedValue({
      paused: false,
      low_disk: false,
      youtube_paused: false,
      youtube_pause_reason: "",
    });
    vi.mocked(listPending).mockResolvedValue([]);
    vi.mocked(cookieHealth).mockResolvedValue({
      status: "valid",
      present: true,
    });
    vi.mocked(streamDownloads).mockResolvedValue(undefined);
  });

  it("refreshes the download queue after the Add view queues a video", async () => {
    vi.mocked(listDownloads).mockResolvedValue([]);
    vi.mocked(addDownload).mockResolvedValue({
      job_id: 7,
      video_id: "vynCRZwkWhE",
      title: "Queued video",
      channel_name: "Some channel",
      state: "pending",
      priority: 10,
    } as Job);

    render(<App />);

    // Wait for the authed shell before touching the rail: on a slow runner
    // the initial getMe() has not resolved yet, and a bare findByText would
    // race its 1s default timeout against an unrendered nav. The waits must
    // stay comfortably under this test's own timeout (last arg to it()),
    // or vitest aborts the test before the query can report what it saw.
    await screen.findByRole("button", { name: /Up next/ }, { timeout: 8000 });
    fireEvent.click(screen.getByRole("button", { name: "Add" }));

    // Once queued, Up next must show the video without a reload — this is the
    // state that used to be terminal (the add seeds the poll). The rail pill
    // deliberately stays dark: it lights for RUNNING work, and this job is
    // still only pending, so the page is what has to prove the refresh.
    vi.mocked(listDownloads).mockResolvedValue([
      {
        job_id: 7,
        video_id: "vynCRZwkWhE",
        title: "Queued video",
        state: "pending",
        priority: 10,
      } as Job,
    ]);

    const urlField = screen.getByLabelText("Video or channel URL");
    fireEvent.change(urlField, {
      target: { value: "https://www.youtube.com/watch?v=vynCRZwkWhE&t=68s" },
    });
    // The submit button now just reads "Add", which the rail's Add nav item
    // also matches — scope the click to the paste form.
    fireEvent.click(within(urlField.closest("form")!).getByRole("button"));

    fireEvent.click(screen.getByRole("button", { name: /Up next/ }));
    await waitFor(() => {
      expect(screen.getByText("Queued video")).toBeInTheDocument();
    });
  }, 20000);
});

// Deep links: the URL is the source of truth for which page is open (route.ts),
// which is what retired the old nowPlaying sessionStorage reload-restore. A
// cold-loaded /video/<id> reopens the Player; a rail click pushes the matching
// URL; a back/forward re-derives the view from the URL via popstate.
//
// The server-side pointer (getPlaybackState) does not change that rule: it only
// answers what "Now playing" means when the URL carries no video id, and a cold
// "/" still lands on the Library. The last tests in here pin exactly that.
describe("App deep links", () => {
  beforeEach(() => {
    // testing-library truncates its failure DOM dump at 7000 chars, which
    // cuts off the rail's lower nav groups and makes a CI-only failure here
    // impossible to diagnose from the log.
    process.env.DEBUG_PRINT_LIMIT = "100000";
    vi.clearAllMocks();
    // Restate every mock this describe depends on rather than inheriting
    // whatever an earlier describe left behind — the paused-banner tests
    // overwrite downloadsStatus, and clearAllMocks only drops call history,
    // not implementations.
    vi.mocked(getMe).mockResolvedValue({ id: "u1", email: "a@b.c" } as User);
    vi.mocked(listDownloads).mockResolvedValue([]);
    vi.mocked(downloadsStatus).mockResolvedValue({
      paused: false,
      low_disk: false,
      youtube_paused: false,
      youtube_pause_reason: "",
    });
    vi.mocked(listPending).mockResolvedValue([]);
    vi.mocked(cookieHealth).mockResolvedValue({
      status: "valid",
      present: true,
    });
    vi.mocked(streamDownloads).mockResolvedValue(undefined);
    // Default both video sources to empty so a test that overrides one does
    // not leak its fixture into the next (clearAllMocks keeps implementations).
    vi.mocked(listVideos).mockResolvedValue([]);
    vi.mocked(searchVideos).mockResolvedValue([]);
    vi.mocked(getPlaybackState).mockResolvedValue({ video_id: "" });
  });

  // Only the Player view renders a <video>; the SearchBar's "Search titles"
  // box is rendered only on the Library (App renders the bar at all only for
  // library/channels). Use those as unambiguous discriminators — the pages
  // carry no titles, and "Library"/"Now playing" always appear as rail labels.
  it("a cold /video/<id> deep link opens the Player on that video", async () => {
    window.history.replaceState(null, "", "/video/v1");
    render(<App />);
    await waitFor(() => expect(document.querySelector("video")).not.toBeNull());
    // A valid path is preserved verbatim — not rewritten by normalization.
    expect(window.location.pathname).toBe("/video/v1");
  });

  it("a cold / lands on the Library", async () => {
    render(<App />);
    await screen.findByPlaceholderText("Search titles");
    expect(document.querySelector("video")).toBeNull();
  });

  it("clicking a Library video opens the Player and pushes /video/<id>", async () => {
    vi.mocked(listVideos).mockResolvedValue([mockVideo]);
    render(<App />);

    // mockVideo is partially watched (resume > 0), so it sits under "In
    // progress", not the default "Unwatched" filter — switch to All to list it.
    fireEvent.click(await screen.findByRole("button", { name: /^All \d/ }));
    fireEvent.click(
      await screen.findByRole("button", {
        name: "Open The Trillion Dollar Equation",
      }),
    );

    await waitFor(() => expect(document.querySelector("video")).not.toBeNull());
    expect(window.location.pathname).toBe("/video/v1");
  });

  it("opening a search match pushes /video/<id> (openVideoAt)", async () => {
    vi.mocked(searchVideos).mockResolvedValue([
      {
        video: mockVideo,
        matches: [
          {
            start_seconds: 90,
            snippet: "matched moment here",
            distance: 0.1,
            kind: "transcript",
          },
        ],
      },
    ]);
    render(<App />);
    await screen.findByPlaceholderText("Search titles");

    fireEvent.click(screen.getByRole("button", { name: "Search" }));
    // Find, not the default Ask tab: this test is about the deep link a match
    // produces, and Find gets there without an answer stream to mock.
    fireEvent.click(await screen.findByRole("button", { name: "Find" }));
    // Matched by its accessible name, not its placeholder: the placeholder is
    // mode-dependent copy on the search view now.
    const input = await screen.findByLabelText("Find words");
    fireEvent.change(input, { target: { value: "equation" } });
    fireEvent.submit(input.closest("form")!);

    // Click the match snippet; the click bubbles to the match button's onOpen.
    fireEvent.click(await screen.findByText("matched moment here"));

    await waitFor(() => expect(document.querySelector("video")).not.toBeNull());
    expect(window.location.pathname).toBe("/video/v1");
  });

  it("normalizes an unknown path to / and shows the Library", async () => {
    window.history.replaceState(null, "", "/bogus");
    render(<App />);
    await screen.findByPlaceholderText("Search titles");
    // useRoute's mount normalization replaceState's the bogus entry to the
    // canonical path so the address bar matches what is shown.
    expect(window.location.pathname).toBe("/");
  });

  it("clicking a rail item pushes its URL", async () => {
    render(<App />);
    await screen.findByPlaceholderText("Search titles");

    fireEvent.click(screen.getByRole("button", { name: "Inbox" }));

    expect(await screen.findByText("Your inbox is empty.")).toBeInTheDocument();
    expect(window.location.pathname).toBe("/inbox");
  });

  it("a cold / stays on the Library even when a video is persisted as now playing", async () => {
    // The anti-hijack rule. The pointer is the rail's fallback, not a redirect:
    // opening peeq must not drop you into a video you happen to be part-way
    // through. This test is what protects that decision.
    vi.mocked(getPlaybackState).mockResolvedValue({ video_id: "v1" });
    render(<App />);
    await screen.findByPlaceholderText("Search titles");
    expect(document.querySelector("video")).toBeNull();
    expect(window.location.pathname).toBe("/");
  });

  it("the rail's Now playing opens the persisted video and pushes /video/<id>", async () => {
    vi.mocked(getPlaybackState).mockResolvedValue({ video_id: "v1" });
    vi.mocked(listVideos).mockResolvedValue([mockVideo]);
    render(<App />);
    await screen.findByPlaceholderText("Search titles");
    // Wait for the pointer to land before clicking: a passive effect having
    // been scheduled is not the same as it having run (React 19 defers them to
    // a macrotask), and clicking too early would test the empty-pointer path.
    await waitFor(() => expect(getPlaybackState).toHaveBeenCalled());

    fireEvent.click(screen.getByRole("button", { name: "Now playing" }));

    await waitFor(() => expect(document.querySelector("video")).not.toBeNull());
    // The URL has to name the video, or a refresh would lose what is on screen.
    expect(window.location.pathname).toBe("/video/v1");
  });

  it("re-reads the pointer on each Now playing click, so a cleared one is not reopened", async () => {
    // The pointer is server state other actions clear: marking the pointed-at
    // video watched from a Library card, or deleting it, clears it server-side.
    // Trusting the copy loaded at bootstrap would have this click reopen a
    // finished video at 0:00 — exactly what the clear rule exists to prevent.
    vi.mocked(getPlaybackState).mockResolvedValue({ video_id: "v1" });
    vi.mocked(listVideos).mockResolvedValue([mockVideo]);
    render(<App />);
    await screen.findByPlaceholderText("Search titles");
    await waitFor(() => expect(getPlaybackState).toHaveBeenCalled());

    // Something else clears it — another tab, or a Library card in this one.
    vi.mocked(getPlaybackState).mockResolvedValue({ video_id: "" });

    fireEvent.click(screen.getByRole("button", { name: "Now playing" }));

    expect(await screen.findByText(/Nothing playing/i)).toBeInTheDocument();
    // And the URL must not claim a video either.
    expect(window.location.pathname).toBe("/video");
  });

  it("leaves Now playing empty when the pointer can't be loaded", async () => {
    // A convenience that fails must degrade to the behaviour peeq had before
    // it existed, not to an error.
    vi.mocked(getPlaybackState).mockRejectedValue(new Error("boom"));
    render(<App />);
    await screen.findByPlaceholderText("Search titles");
    await waitFor(() => expect(getPlaybackState).toHaveBeenCalled());

    fireEvent.click(screen.getByRole("button", { name: "Now playing" }));

    expect(await screen.findByText(/Nothing playing/i)).toBeInTheDocument();
  });

  // An inbox video's page shares the Player's URL — /video/<id> is the video's
  // page whether or not a file has arrived yet, deliberately, so that nothing
  // moves once it does (Inbox.tsx). These two pin the shell telling the cases
  // apart anyway: the rail must not announce "Now playing" over a video with no
  // file, and the fileless id must not become what "Now playing" means.
  //
  // getVideo is mocked to return mockVideo for every id, so the page these open
  // is not literally fileless — what is being pinned is the shell's handling of
  // where the page was opened from, which is the signal the fix keys on. A test
  // that needs a genuinely fileless video belongs in Player's own suite.
  async function openInboxSummary() {
    vi.mocked(listPendingApi).mockResolvedValue([inboxItem]);
    render(<App />);
    await screen.findByPlaceholderText("Search titles");
    await waitFor(() => expect(getPlaybackState).toHaveBeenCalled());

    fireEvent.click(screen.getByRole("button", { name: /^Inbox/ }));
    const title = await screen.findByText("A video worth reading about");
    const poster = (title.closest("article") as HTMLElement).querySelector(
      ".thumb",
    ) as HTMLElement;
    fireEvent.click(poster);
    await waitFor(() => expect(window.location.pathname).toBe("/video/p1"));
  }

  it("keeps the rail on Inbox while an inbox video's summary is open", async () => {
    // The summary page is reached from the Inbox and nowhere else, so leaving
    // Inbox lit says where you are. Lighting "Now playing" instead said both
    // where you weren't and something untrue — there is no file to play.
    await openInboxSummary();

    expect(screen.getByRole("button", { name: /^Inbox/ })).toHaveAttribute(
      "aria-current",
      "page",
    );
    expect(
      screen.getByRole("button", { name: "Now playing" }),
    ).not.toHaveAttribute("aria-current");
  });

  it("does not let a read inbox summary become what Now playing means", async () => {
    // navigate() merges onto the current route, so videoId survives leaving the
    // Player — which is what lets "Now playing" return to your video after a
    // detour. A fileless inbox video landing in that memory used to shadow the
    // real pointer for the rest of the session: read one summary and every
    // later "Now playing" click reopened it instead of the video being watched.
    vi.mocked(getPlaybackState).mockResolvedValue({ video_id: "v1" });
    vi.mocked(listVideos).mockResolvedValue([mockVideo]);
    await openInboxSummary();

    // Leave for any other page, then ask for what is playing.
    fireEvent.click(screen.getByRole("button", { name: "Library" }));
    await screen.findByPlaceholderText("Search titles");
    fireEvent.click(screen.getByRole("button", { name: "Now playing" }));

    await waitFor(() => expect(window.location.pathname).toBe("/video/v1"));
  });

  it("answers Now playing with the pointer while a summary is on screen", async () => {
    // The other half of the same guard, and the one that keeps this click from
    // being dead. On a summary page the URL carries an id, which is normally
    // the signal to short-circuit — clicking "Now playing" on a video you are
    // watching must not re-read a pointer another device could have moved, or
    // it would navigate you out of what you are playing. A summary is not that,
    // so the click has to reach the pointer instead of landing back on itself.
    vi.mocked(getPlaybackState).mockResolvedValue({ video_id: "v1" });
    vi.mocked(listVideos).mockResolvedValue([mockVideo]);
    await openInboxSummary();

    fireEvent.click(screen.getByRole("button", { name: "Now playing" }));

    await waitFor(() => expect(window.location.pathname).toBe("/video/v1"));
    // And the rail follows: the page showing is no longer the inbox video's.
    expect(screen.getByRole("button", { name: "Now playing" })).toHaveAttribute(
      "aria-current",
      "page",
    );
  });

  it("re-derives the view from the URL on back/forward (popstate)", async () => {
    render(<App />);
    await screen.findByPlaceholderText("Search titles");

    // Navigate into Inbox — pushes /inbox.
    fireEvent.click(screen.getByRole("button", { name: "Inbox" }));
    await screen.findByText("Your inbox is empty.");
    expect(window.location.pathname).toBe("/inbox");

    // Simulate the browser Back button: the URL returns to the previous entry
    // and a popstate fires. jsdom's real history.back() is async and quirky,
    // so drive the listener directly — this is the exact contract useRoute's
    // popstate handler implements (re-parse window.location). Real
    // back/forward is covered by browser verification.
    window.history.replaceState(null, "", "/");
    window.dispatchEvent(new Event("popstate"));

    await screen.findByPlaceholderText("Search titles");
    expect(document.querySelector("video")).toBeNull();
  });
});

// Task 13: global YouTube-paused banner. youtube_paused takes precedence
// over the healthy default and offers a one-click Resume that calls the
// kill-switch endpoint and refetches status.
describe("App youtube-paused banner", () => {
  it("shows the YouTube-paused banner with a Resume action", async () => {
    vi.mocked(downloadsStatus).mockResolvedValue({
      paused: false,
      low_disk: false,
      youtube_paused: true,
      youtube_pause_reason: "",
    });
    vi.mocked(resumeYoutube).mockResolvedValue(undefined);
    render(<App />);
    const resume = await screen.findByRole("button", { name: /resume/i });
    fireEvent.click(resume);
    await waitFor(() => expect(resumeYoutube).toHaveBeenCalled());
  });
});

// Task 11: the channel page is a detail destination reached only by clicking
// a channel name (like the player is reached from a video card), never a
// rail item — this locks that in as a regression guard.
describe("App routing", () => {
  it("channel is not in the nav rail", async () => {
    render(<App />);
    await screen.findByPlaceholderText("Search titles");
    expect(screen.queryByRole("button", { name: /^channel$/i })).toBeNull();
  });

  // Task 11/15: the channel page has no rail entry — it's reached only by
  // clicking a channel-name link. This drives the real flow (Channels list
  // -> click a channel name -> Channel page -> "All channels" back button)
  // to exercise App's openChannel/ViewSwitch wiring end to end, rather than
  // unit-testing those functions directly.
  it("clicking a channel name from Channels opens the Channel page, and the back button returns", async () => {
    const { listChannels, getChannel } = await import("./api/channels");
    vi.mocked(listChannels).mockReset();
    vi.mocked(listChannels).mockResolvedValue([
      {
        id: "UCa",
        name: "Uncanny Expeditions",
        handle: "@UncannyExpeditions",
        subscribed: true,
        autodownload: false,
        auto_summary: true,
        format_override: "",
        pending_count: 0,
        downloaded_count: 3,
        added: true,
        dormant: false,
      },
    ]);
    vi.mocked(getChannel).mockReset();
    vi.mocked(getChannel).mockResolvedValue({
      id: "UCa",
      name: "Uncanny Expeditions",
      handle: "@UncannyExpeditions",
      description: "",
      has_avatar: false,
      has_banner: false,
      verified: false,
      resolve_ok: true,
      gone: false,
      added: true,
      added_at: "2026-03-14 09:00:00",
      archived_count: 3,
      runtime_seconds: 600,
      disk_bytes: 1024,
      newest_published_at: "2026-07-18T00:00:00Z",
      subscribed: true,
      autodownload: false,
      format_override: "",
      last_scanned_at: "2026-07-20 08:00:00",
      next_scan_at: "2026-07-20 14:00:00",
      pending_count: 0,
    });

    render(<App />);
    await screen.findByPlaceholderText("Search titles");

    fireEvent.click(screen.getByRole("button", { name: "Channels" }));
    const channelLink = await screen.findByRole("button", {
      name: "Uncanny Expeditions",
    });
    fireEvent.click(channelLink);

    expect(
      await screen.findByRole("button", { name: /all channels/i }),
    ).toBeInTheDocument();
    await waitFor(() => expect(getChannel).toHaveBeenCalledWith("UCa"));

    fireEvent.click(screen.getByRole("button", { name: /all channels/i }));

    await waitFor(() => {
      expect(
        screen.queryByRole("button", { name: /all channels/i }),
      ).toBeNull();
    });
  });

  it("the Inbox nav item renders the Inbox view", async () => {
    render(<App />);
    await screen.findByPlaceholderText("Search titles");

    fireEvent.click(screen.getByRole("button", { name: "Inbox" }));

    expect(await screen.findByText("Your inbox is empty.")).toBeInTheDocument();
  });
});

// Queue + summaries wiring: App owns both lanes' data. A summary SSE event
// updates the in-flight set (and the live phase) without a reload, and the
// Queue page cancels a download through App's shared handler.
describe("App queue and summaries", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.mocked(getMe).mockResolvedValue({ id: "u1", email: "a@b.c" } as User);
    vi.mocked(downloadsStatus).mockResolvedValue({
      paused: false,
      low_disk: false,
      youtube_paused: false,
      youtube_pause_reason: "",
    });
    vi.mocked(listPending).mockResolvedValue([]);
    vi.mocked(cookieHealth).mockResolvedValue({
      status: "valid",
      present: true,
    });
    vi.mocked(streamDownloads).mockResolvedValue(undefined);
    vi.mocked(listDownloads).mockResolvedValue([]);
    vi.mocked(listSummaries).mockResolvedValue([]);
    vi.mocked(cancelDownload).mockResolvedValue(undefined);
  });

  // The rail used to grey Inbox and Up next out once their counts loaded empty.
  // It no longer does — every item reads at full strength, and emptiness is
  // said by the absent count pill and by the page's own empty state.
  it("never greys the rail's Inbox or Up next, loaded or not", async () => {
    render(<App />);
    await screen.findByRole("button", { name: /Library/ }, { timeout: 8000 });

    // Both lists resolve empty in this suite's beforeEach, so this is the state
    // that used to dim.
    await waitFor(() => {
      expect(
        screen.getByRole("button", { name: "Up next" }).className,
      ).not.toContain("idle");
    });
    expect(
      screen.getByRole("button", { name: "Inbox" }).className,
    ).not.toContain("idle");
  }, 20000);

  it("reflects a summary SSE event in the rail and on Up next", async () => {
    // The summarize worker publishes a "summary" phase event on the same SSE
    // stream as download progress; App must route it to the summaries lane.
    vi.mocked(streamDownloads).mockImplementation((onEvent) => {
      onEvent({
        event: "summary",
        data: { video_id: "s1", status: "running", phase: "summarizing" },
      });
      return Promise.resolve();
    });
    // The event triggers a re-list; return the now-active job.
    vi.mocked(listSummaries).mockResolvedValue([
      {
        id: 1,
        video_id: "s1",
        title: "A clip being summarized",
        channel_name: "Chan",
        state: "running",
      },
    ]);

    render(<App />);
    await screen.findByRole("button", { name: /Library/ }, { timeout: 8000 });

    // The rail pill picks up the in-flight summary once the re-list settles.
    // A RUNNING summary lights it exactly as a running download would — the
    // gate is "something is moving", not "a download is moving".
    await waitFor(() => {
      expect(screen.getByRole("button", { name: /Up next/ })).toHaveTextContent(
        "1",
      );
    });

    // Up next shows the job in its own lane with its live phase.
    fireEvent.click(screen.getByRole("button", { name: /Up next/ }));
    expect(
      await screen.findByText("A clip being summarized"),
    ).toBeInTheDocument();
    expect(screen.getByText("Summarising")).toBeInTheDocument();
  }, 20000);

  it("refreshes the Inbox count when an activity SSE event arrives", async () => {
    // A scan can surface new videos to decide while the user is on another
    // page. Without this refresh the rail keeps the count it loaded at
    // navigation time — and since the rail now greys Inbox out at 0, a stale
    // count claims there is nothing to decide when there is.
    vi.mocked(listPending).mockResolvedValue([]);
    vi.mocked(streamDownloads).mockImplementation((onEvent) => {
      // The next fetch is what the scan surfaced.
      vi.mocked(listPending).mockResolvedValue([
        { video_id: "p1", channel_id: "c1", title: "Fresh upload" },
        { video_id: "p2", channel_id: "c1", title: "Another one" },
      ] as unknown as Awaited<ReturnType<typeof listPending>>);
      onEvent({
        event: "activity",
        data: { id: 7, at: "2026-07-25 08:00:00", kind: "scan", outcome: "ok" },
      });
      return Promise.resolve();
    });

    render(<App />);
    await screen.findByRole("button", { name: /Library/ }, { timeout: 8000 });

    await waitFor(() => {
      expect(screen.getByRole("button", { name: /Inbox/ })).toHaveTextContent(
        "2",
      );
    });
  }, 20000);

  it("cancels a download from Up next", async () => {
    vi.mocked(listDownloads).mockResolvedValue([
      {
        job_id: 5,
        video_id: "v5",
        title: "Downloading clip",
        state: "running",
        priority: 10,
      } as Job,
    ]);

    render(<App />);
    await screen.findByRole("button", { name: /Library/ }, { timeout: 8000 });

    fireEvent.click(screen.getByRole("button", { name: /Up next/ }));
    const cancel = await screen.findByRole("button", { name: /cancel/i });
    fireEvent.click(cancel);

    await waitFor(() => expect(cancelDownload).toHaveBeenCalledWith(5));
  }, 20000);
});
