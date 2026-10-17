import { useEffect, useState } from "react";
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

function videos(n: number): string {
  return `${n} ${n === 1 ? "video" : "videos"}`;
}

// StorageSection shows what this channel takes up, split the way the keep
// switch next to it is about: the files of the videos on disk, what peeq
// stores for those videos, and what it keeps after a file went. Measured on
// its own request when the Settings tab opens — it visits every video of the
// channel, too much for the header's load.
export function StorageSection({ channelId }: { channelId: string }) {
  const [storage, setStorage] = useState<ChannelStorage | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    let active = true;
    getChannelStorage(channelId)
      .then((s) => {
        if (active) setStorage(s);
      })
      .catch((e: Error) => {
        if (active) setError(e.message);
      });
    return () => {
      active = false;
    };
  }, [channelId]);

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
              <b>Their transcripts and search</b>{" "}
              {size(dbBytes(storage.on_disk))} — {parts(storage.on_disk)}
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
