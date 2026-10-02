import { useCallback, useRef } from "react";
import type { Dispatch, SetStateAction } from "react";
import { setFavorite, setWatched } from "../api";
import type { LibraryVideo } from "../api/types";

// useVideoToggles owns the favorite and watched toggles of a card grid: flip
// the field locally so the card answers at once, send the change, and on
// failure put the card back and say why. A card that silently flips and flips
// back reads as a broken button, and the next move is to click it again.
//
// Both callbacks keep one identity for the life of the grid, which is what
// lets a memoised VideoCard skip the renders it has no part in. They read the
// current list and the current handlers through refs instead of closing over
// them.
export function useVideoToggles(
  videos: LibraryVideo[],
  setVideos: Dispatch<SetStateAction<LibraryVideo[]>>,
  handlers: {
    /** Called with the failure's message after the card has been put back. */
    onError: (message: string) => void;
    /** Called once the server has taken a change. */
    onSettled?: () => void;
  },
): {
  toggleFavorite: (id: string) => void;
  toggleWatched: (id: string) => void;
} {
  const videosRef = useRef(videos);
  videosRef.current = videos;
  const handlersRef = useRef(handlers);
  handlersRef.current = handlers;

  const toggle = useCallback(
    async (
      id: string,
      patch: (v: LibraryVideo) => Partial<LibraryVideo>,
      revert: (v: LibraryVideo) => Partial<LibraryVideo>,
      send: (v: LibraryVideo) => Promise<unknown>,
    ) => {
      const current = videosRef.current.find((v) => v.id === id);
      if (!current) return;
      const apply = (p: Partial<LibraryVideo>) =>
        setVideos((prev) =>
          prev.map((v) => (v.id === id ? { ...v, ...p } : v)),
        );
      apply(patch(current));
      try {
        await send(current);
        handlersRef.current.onSettled?.();
      } catch (e) {
        apply(revert(current));
        handlersRef.current.onError((e as Error).message);
      }
    },
    [setVideos],
  );

  const toggleFavorite = useCallback(
    (id: string) =>
      void toggle(
        id,
        (v) => ({ favorite: !v.favorite }),
        (v) => ({ favorite: v.favorite }),
        (v) => setFavorite(id, !v.favorite),
      ),
    [toggle],
  );

  // The API answers with the watched flag alone, and the server zeroes the
  // resume position in both directions, so the reset is mirrored here: without
  // it, un-watching a card would make its progress bar appear (VideoCard only
  // draws it when !watched) still showing the position the server just cleared.
  const toggleWatched = useCallback(
    (id: string) =>
      void toggle(
        id,
        (v) => ({ watched: !v.watched, resume_position_seconds: 0 }),
        (v) => ({
          watched: v.watched,
          resume_position_seconds: v.resume_position_seconds,
        }),
        (v) => setWatched(id, !v.watched),
      ),
    [toggle],
  );

  return { toggleFavorite, toggleWatched };
}
