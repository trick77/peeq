import { useEffect, useRef, useState } from "react";
import {
  getChannelStorage,
  type ChannelStorage,
  type StorageGroup,
} from "../../api/channels";
import { formatSize } from "../../format";

// dbBytes is what a group costs in the database: everything but the files.
function dbBytes(g: StorageGroup): number {
  return (
    g.transcript_bytes +
    g.summary_bytes +
    g.search_text_bytes +
    g.vector_bytes +
    g.poster_bytes
  );
}

function size(bytes: number): string {
  return formatSize(bytes) || "0 B";
}

// The parts, in the order a reader weighs them: the search index first, since
// it is what keeping buys and usually the biggest database share.
function parts(g: StorageGroup): string {
  return [
    `search index ${size(g.search_text_bytes + g.vector_bytes)}`,
    `transcripts ${size(g.transcript_bytes)}`,
    `summaries ${size(g.summary_bytes)}`,
    `posters ${size(g.poster_bytes)}`,
  ].join(" · ");
}

// After a switch-off the purge runs in the background, so the section measures
// again every AFTER_PURGE_MS while the kept count is still falling, at most
// MAX_PURGE_POLLS times: it stops once kept reaches 0 or stops moving.
const AFTER_PURGE_MS = 3000;
const MAX_PURGE_POLLS = 20;

function videos(n: number): string {
  return `${n} ${n === 1 ? "video" : "videos"}`;
}

// StorageSection shows what this channel takes up, split the way the keep
// switch next to it is about: the files of the videos on disk, what peeq
// stores for those videos, and what it keeps after a file went. Measured on
// its own request when the Settings tab opens — it visits every video of the
// channel, too much for the header's load.
//
// It measures again when the switch flips. Only an observed on → off flip
// follows the purge (see AFTER_PURGE_MS); opening Settings on a channel that
// is already off measures once. Each answer is keyed to the channel and switch
// state it was measured for, and only the latest request may answer, so a
// stale one — another channel's, from before the flip, or a slow earlier
// measure — is never shown.
export function StorageSection({
  channelId,
  keepReads,
}: {
  channelId: string;
  keepReads: boolean;
}) {
  const key = `${channelId}:${keepReads}`;
  const [result, setResult] = useState<{
    key: string;
    storage?: ChannelStorage;
    error?: string;
  } | null>(null);
  const seen = useRef<{ channelId: string; keepReads: boolean } | null>(null);
  const latest = useRef(0);

  useEffect(() => {
    const at = `${channelId}:${keepReads}`;
    const prev = seen.current;
    const turnedOff =
      prev !== null &&
      prev.channelId === channelId &&
      prev.keepReads &&
      !keepReads;
    seen.current = { channelId, keepReads };

    let active = true;
    let timer: ReturnType<typeof setTimeout> | undefined;
    let polls = 0;
    let lastKept = Infinity;
    const measure = () => {
      const n = ++latest.current;
      getChannelStorage(channelId)
        .then((storage) => {
          if (!active || n !== latest.current) return;
          setResult({ key: at, storage });
          const kept = storage.kept.videos;
          if (
            turnedOff &&
            kept > 0 &&
            kept < lastKept &&
            polls < MAX_PURGE_POLLS
          ) {
            lastKept = kept;
            polls++;
            timer = setTimeout(measure, AFTER_PURGE_MS);
          }
        })
        .catch((e: Error) => {
          if (active && n === latest.current)
            setResult({ key: at, error: e.message });
        });
    };
    measure();
    return () => {
      active = false;
      clearTimeout(timer);
    };
  }, [channelId, keepReads]);

  const current = result?.key === key ? result : null;
  const storage = current?.storage;
  const error = current?.error;

  return (
    <div className="chan-srow">
      <div>
        <div className="lab">Storage</div>
        {error ? (
          <div className="hint">{error}</div>
        ) : !storage ? (
          <div className="hint">Measuring…</div>
        ) : (
          <div className="hint chan-storage">
            <div>
              <b>Video files</b> {size(storage.on_disk.file_bytes)} ·{" "}
              {videos(storage.on_disk.videos)} on disk
            </div>
            <div>
              <b>Their data in peeq</b> {size(dbBytes(storage.on_disk))} —{" "}
              {parts(storage.on_disk)}
            </div>
            <div>
              <b>Kept in Search</b> {size(dbBytes(storage.kept))} ·{" "}
              {videos(storage.kept.videos)} without a file —{" "}
              {parts(storage.kept)}
            </div>
            {storage.other.videos > 0 ? (
              <div>
                <b>New and in progress</b> {size(dbBytes(storage.other))} ·{" "}
                {videos(storage.other.videos)}
              </div>
            ) : null}
          </div>
        )}
      </div>
    </div>
  );
}
