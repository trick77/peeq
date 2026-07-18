#!/bin/sh
# fake-ytdlp.sh is a stub yt-dlp binary for tests. It never touches the
# network or a real yt-dlp install. Behavior is controlled entirely by
# environment variables so one script covers every ytdlp package test
# scenario (cookie gate marker, metadata JSON, error classification,
# version reporting).
set -eu

if [ -n "${FAKE_YTDLP_MARKER:-}" ]; then
  touch "$FAKE_YTDLP_MARKER"
fi

for arg in "$@"; do
  if [ "$arg" = "--version" ]; then
    echo "${FAKE_YTDLP_VERSION:-2024.01.01}"
    exit 0
  fi
done

if [ -n "${FAKE_YTDLP_STDERR:-}" ]; then
  echo "$FAKE_YTDLP_STDERR" 1>&2
  exit "${FAKE_YTDLP_EXIT:-1}"
fi

if [ -n "${FAKE_YTDLP_JSON:-}" ]; then
  echo "$FAKE_YTDLP_JSON"
  exit 0
fi

echo '{}'
exit 0
