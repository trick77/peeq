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

# Download-mode: detected by the presence of an -o <template> argument
# (only Download passes one). Prints two --newline progress lines, then
# either fails the way FAKE_YTDLP_STDERR/FAKE_YTDLP_EXIT direct (to
# exercise the retryable/terminal cleanup paths) or writes a dummy media
# file, thumbnail, and info.json into the templated output directory and
# exits 0.
#
# The info.json mirrors REAL yt-dlp: the video's own chapters under
# "chapters", and --sponsorblock-mark's result under the separate
# "sponsorblock_chapters" key. It must never fabricate a
# "[SponsorBlock]: …" title inside "chapters" — real yt-dlp does not write
# one there, and a fake that did is why peeq shipped a SponsorBlock pipeline
# that parsed nothing for months while every test passed.
outtmpl=""
prev=""
for arg in "$@"; do
  if [ "$prev" = "-o" ]; then outtmpl="$arg"; fi
  prev="$arg"
done

if [ -n "$outtmpl" ]; then
  outdir=$(dirname "$outtmpl")
  mkdir -p "$outdir"
  id="${FAKE_YTDLP_ID:-testid}"

  echo "[download]  10.0% of   50.00MiB at    2.00MiB/s ETA 01:23"

  if [ -n "${FAKE_YTDLP_STDERR:-}" ]; then
    echo "$FAKE_YTDLP_STDERR" 1>&2
    exit "${FAKE_YTDLP_EXIT:-1}"
  fi

  echo "[download] 100% of 50.00MiB in 00:05"

  echo "dummy video content" > "$outdir/$id.mp4"
  echo "dummy thumbnail" > "$outdir/$id.jpg"
  printf 'WEBVTT\n\n00:00:00.000 --> 00:00:01.000\nhello\n' > "$outdir/$id.${FAKE_YTDLP_SUBLANG:-en}.vtt"
  cat > "$outdir/$id.info.json" <<EOF
{
  "id": "$id",
  "channel_id": "${FAKE_YTDLP_CHANNEL_ID:-UCabc}",
  "ext": "mp4",
  "language": "${FAKE_YTDLP_SUBLANG:-en}",
  "upload_date": "${FAKE_YTDLP_UPLOAD_DATE-20240115}",
  "chapters": [
    {"start_time": 0, "end_time": 10, "title": "Intro"}
  ],
  "sponsorblock_chapters": [
    {"start_time": 10, "end_time": 25, "category": "sponsor",
     "title": "Sponsor", "type": "skip"}
  ]
}
EOF
  exit 0
fi

if [ -n "${FAKE_YTDLP_STDERR:-}" ]; then
  echo "$FAKE_YTDLP_STDERR" 1>&2
  exit "${FAKE_YTDLP_EXIT:-1}"
fi

if [ -n "${FAKE_YTDLP_JSON:-}" ]; then
  echo "$FAKE_YTDLP_JSON"
  exit 0
fi

# Default metadata JSON, used when a test doesn't override FAKE_YTDLP_JSON.
# availability defaults to "public" (real yt-dlp's value for a normal,
# publicly-listed video) rather than being omitted, so tests exercise the
# same raw vocabulary real yt-dlp actually returns.
echo '{"availability": "public"}'
exit 0
