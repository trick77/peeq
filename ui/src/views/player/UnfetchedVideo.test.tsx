import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { UnfetchedVideo } from "./UnfetchedVideo";
import type { Video } from "../../api/types";

vi.mock("../../api/pending", () => ({
  downloadPending: vi.fn(),
  ignorePending: vi.fn(),
}));

import { downloadPending, ignorePending } from "../../api/pending";

function video(overrides: Partial<Video> = {}): Video {
  return {
    id: "v1",
    title: "Why this dam was built to fail on purpose",
    channel_id: "c1",
    channel_name: "Practical Engineering",
    duration_seconds: 1122,
    published_at: "2026-07-28",
    status: "new",
    summary: "First paragraph about fuse plugs.\n\nSecond paragraph.",
    summary_status: "done",
    has_subtitles: true,
    ...overrides,
  } as Video;
}

describe("UnfetchedVideo", () => {
  beforeEach(() => {
    vi.mocked(downloadPending).mockReset();
    vi.mocked(ignorePending).mockReset();
    vi.mocked(downloadPending).mockResolvedValue(undefined);
    vi.mocked(ignorePending).mockResolvedValue(undefined);
  });

  it("renders the summary as paragraphs and offers both decisions", async () => {
    render(<UnfetchedVideo video={video()} />);

    expect(screen.getByText("First paragraph about fuse plugs.")).toBeTruthy();
    expect(screen.getByText("Second paragraph.")).toBeTruthy();
    expect(screen.getByRole("button", { name: /Download/ })).toBeTruthy();
    expect(screen.getByRole("button", { name: /Ignore/ })).toBeTruthy();
  });

  // There is no media, so nothing on this page may imply there is. A <video>
  // element here would try to load a stream that cannot exist.
  it("renders no media element", () => {
    const { container } = render(<UnfetchedVideo video={video()} />);
    expect(container.querySelector("video")).toBeNull();
  });

  it("queues a download and says so without navigating away", async () => {
    const onQueued = vi.fn();
    render(<UnfetchedVideo video={video()} onQueued={onQueued} />);

    await userEvent.click(screen.getByRole("button", { name: /Download/ }));

    expect(downloadPending).toHaveBeenCalledWith("v1");
    expect(onQueued).toHaveBeenCalled();
    expect(await screen.findByText("Queued for download.")).toBeTruthy();
  });

  // Ignoring deletes the row and the summary server-side, so the page's subject
  // is gone: the caller has to be told to leave.
  it("tells the caller to leave after an ignore", async () => {
    const onDismissed = vi.fn();
    render(<UnfetchedVideo video={video()} onDismissed={onDismissed} />);

    await userEvent.click(screen.getByRole("button", { name: /Ignore/ }));

    expect(ignorePending).toHaveBeenCalledWith("v1");
    expect(onDismissed).toHaveBeenCalled();
  });

  // The captions are already on disk — reading them is what produced the
  // summary — so the panel costs a fetch of a file peeq already has. The
  // download and copy controls are the point: you can take the text away
  // without ever fetching the video.
  it("offers the transcript with its downloads and copy button", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue({
        ok: true,
        text: async () =>
          "WEBVTT\n\n00:00:05.000 --> 00:00:07.000\nA spoken line.\n",
      }),
    );
    render(<UnfetchedVideo video={video()} />);

    await userEvent.click(screen.getByRole("button", { name: /Transcript/ }));
    await screen.findByText("A spoken line.");

    expect(screen.getByRole("button", { name: /\.txt/ })).toBeTruthy();
    expect(screen.getByRole("link", { name: /\.vtt/ })).toBeTruthy();
    expect(screen.getByRole("button", { name: /Copy text/ })).toBeTruthy();
    vi.unstubAllGlobals();
  });

  it("shows no transcript panel for a video with no captions", () => {
    render(<UnfetchedVideo video={video({ has_subtitles: false })} />);
    expect(screen.queryByRole("button", { name: /Transcript/ })).toBeNull();
  });

  // One status, two videos: has_subtitles is what separates "YouTube has no
  // captions" from "they turned out to be music". The copy splits on it,
  // because a page that says the title and the channel are all peeq knows
  // contradicts the transcript panel sitting right underneath it.
  it("explains a video with no speech instead of showing a spinner", () => {
    render(
      <UnfetchedVideo
        video={video({
          summary: "",
          summary_status: "no_transcript",
          has_subtitles: false,
        })}
      />,
    );
    expect(screen.getByText(/No speech in this video/)).toBeTruthy();
  });

  // A no_transcript video that HAS captions — where the Inbox card's "Read
  // transcript" lands. The transcript IS the page, so it arrives expanded
  // rather than as a folded accordion the user has to press a second time, and
  // the copy above it must not claim the title and channel are all peeq has.
  it("opens the transcript expanded when there is no summary to read", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue({
        ok: true,
        text: async () => "WEBVTT\n\n00:00:05.000 --> 00:00:07.000\nla la la\n",
      }),
    );
    render(
      <UnfetchedVideo
        video={video({ summary: "", summary_status: "no_transcript" })}
      />,
    );

    expect(await screen.findByText("la la la")).toBeTruthy();
    expect(screen.queryByText(/all peeq knows about it/)).toBeNull();
    expect(screen.getByText(/captions it fetched are below/)).toBeTruthy();
    vi.unstubAllGlobals();
  });

  it("says it is still summarizing a video whose captions have not landed", () => {
    render(
      <UnfetchedVideo
        video={video({ summary: "", summary_status: "pending" })}
      />,
    );
    expect(screen.getByText(/Summarizing this video/)).toBeTruthy();
  });

  // The eyebrow above the title is the same line the library card and the
  // Player carry, and it has to behave the same way in all three: the channel
  // navigates, and the date says what kind of date it is. Only "aired" can
  // appear here — nothing has been downloaded, which is the point of the page.
  describe("the eyebrow above the title", () => {
    it("navigates to the channel when onOpenChannel is provided", async () => {
      const onOpenChannel = vi.fn();
      render(<UnfetchedVideo video={video()} onOpenChannel={onOpenChannel} />);

      await userEvent.click(
        screen.getByRole("button", { name: "Practical Engineering" }),
      );

      expect(onOpenChannel).toHaveBeenCalledWith("c1");
    });

    // The page is reachable from places with nowhere to navigate to, so the
    // name has to survive as plain text rather than a dead button.
    it("renders the channel as plain text when onOpenChannel is absent", () => {
      render(<UnfetchedVideo video={video()} />);

      expect(
        screen.queryByRole("button", { name: "Practical Engineering" }),
      ).toBeNull();
      expect(screen.getByText("Practical Engineering")).toBeTruthy();
    });

    // A channel peeq has not resolved a name for yet still has to be nameable,
    // and the id is the only handle there is. Both branches carry the fallback,
    // so both are checked.
    it("falls back to the channel id when the name is unknown", async () => {
      const onOpenChannel = vi.fn();
      const { unmount } = render(
        <UnfetchedVideo
          video={video({ channel_name: "" })}
          onOpenChannel={onOpenChannel}
        />,
      );

      await userEvent.click(screen.getByRole("button", { name: "c1" }));
      expect(onOpenChannel).toHaveBeenCalledWith("c1");
      unmount();

      const { container } = render(
        <UnfetchedVideo video={video({ channel_name: "" })} />,
      );
      expect(container.querySelector(".chan-name")?.textContent).toBe("c1");
    });

    it("labels the publish date as aired", () => {
      const { container } = render(<UnfetchedVideo video={video()} />);

      expect(container.querySelector(".by")?.textContent).toContain("aired");
    });

    // published_at is unknown for some live streams and premieres, and a bare
    // "aired" with no date behind it is worse than no fragment at all.
    it("drops the date fragment when published_at is unknown", () => {
      const { container } = render(
        <UnfetchedVideo video={video({ published_at: undefined })} />,
      );

      const by = container.querySelector(".by");
      expect(by?.textContent).toContain("Practical Engineering");
      expect(by?.textContent).not.toContain("aired");
      expect(by?.querySelector(".dot")).toBeNull();
    });
  });
});

// The stepper is what makes reading a backlog bearable: open, read, decide,
// next, without returning to the grid between each one.
describe("UnfetchedVideo stepper", () => {
  beforeEach(() => {
    vi.mocked(downloadPending).mockReset();
    vi.mocked(ignorePending).mockReset();
    vi.mocked(downloadPending).mockResolvedValue(undefined);
    vi.mocked(ignorePending).mockResolvedValue(undefined);
  });

  const order = ["a1", "v1", "z9"];

  it("shows the position and steps both ways", async () => {
    const onOpenInboxVideo = vi.fn();
    render(
      <UnfetchedVideo
        video={video()}
        inboxOrder={order}
        onOpenInboxVideo={onOpenInboxVideo}
      />,
    );

    expect(screen.getByText("2 of 3")).toBeTruthy();

    await userEvent.click(screen.getByRole("button", { name: /Prev/ }));
    expect(onOpenInboxVideo).toHaveBeenLastCalledWith("a1");

    await userEvent.click(screen.getByRole("button", { name: /Next/ }));
    expect(onOpenInboxVideo).toHaveBeenLastCalledWith("z9");
  });

  // An end of the list is a fact, not a failure: the arrow greys out and keeps
  // its place rather than vanishing and shifting the one beside it.
  it("disables the arrow at each end rather than hiding it", () => {
    const { unmount } = render(
      <UnfetchedVideo
        video={video({ id: "a1" })}
        inboxOrder={order}
        onOpenInboxVideo={vi.fn()}
      />,
    );
    expect(
      screen.getByRole("button", { name: /Prev/ }).hasAttribute("disabled"),
    ).toBe(true);
    expect(
      screen.getByRole("button", { name: /Next/ }).hasAttribute("disabled"),
    ).toBe(false);
    unmount();

    render(
      <UnfetchedVideo
        video={video({ id: "z9" })}
        inboxOrder={order}
        onOpenInboxVideo={vi.fn()}
      />,
    );
    expect(
      screen.getByRole("button", { name: /Next/ }).hasAttribute("disabled"),
    ).toBe(true);
  });

  // A video reached from the Library or a link has no inbox position, and a
  // disabled pair of arrows on those pages would just be furniture.
  it("shows no stepper for a video that is not in the inbox order", () => {
    render(
      <UnfetchedVideo
        video={video({ id: "notInList" })}
        inboxOrder={order}
        onOpenInboxVideo={vi.fn()}
      />,
    );
    expect(screen.queryByRole("button", { name: /Next/ })).toBeNull();
    expect(screen.queryByText(/of 3/)).toBeNull();
  });

  it("shows no stepper before the inbox has ever been opened", () => {
    render(<UnfetchedVideo video={video()} inboxOrder={[]} />);
    expect(screen.queryByRole("button", { name: /Next/ })).toBeNull();
  });

  // Deciding moves you on — that is the point of opening these one after
  // another. Going back to the grid after every decision is precisely the round
  // trip the stepper exists to avoid.
  it("advances to the next video after a decision", async () => {
    const onOpenInboxVideo = vi.fn();
    const onDismissed = vi.fn();
    render(
      <UnfetchedVideo
        video={video()}
        inboxOrder={order}
        onOpenInboxVideo={onOpenInboxVideo}
        onDismissed={onDismissed}
      />,
    );

    await userEvent.click(screen.getByRole("button", { name: /Download/ }));

    expect(downloadPending).toHaveBeenCalledWith("v1");
    expect(onOpenInboxVideo).toHaveBeenCalledWith("z9");
    expect(onDismissed).not.toHaveBeenCalled();
  });

  // The last video has nowhere to advance to, so an ignore has to leave: its
  // row and summary are deleted server-side, and the page's subject is gone.
  it("leaves when the last video is ignored", async () => {
    const onDismissed = vi.fn();
    render(
      <UnfetchedVideo
        video={video({ id: "z9" })}
        inboxOrder={order}
        onOpenInboxVideo={vi.fn()}
        onDismissed={onDismissed}
      />,
    );

    await userEvent.click(screen.getByRole("button", { name: /Ignore/ }));

    expect(ignorePending).toHaveBeenCalledWith("z9");
    expect(onDismissed).toHaveBeenCalled();
  });
});
