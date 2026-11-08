package videos

import "github.com/trick77/peeq/internal/rag"

// Video mirrors the columns of the videos table this package reads or
// writes. Fields left at their zero value on Upsert fall back to the
// column defaults on insert.
//
// Table: videos
type Video struct {
	ID              string
	URL             string
	Title           string
	ChannelID       string
	ChannelName     string
	DurationSeconds int64
	PublishedAt     string
	Description     string
	// HasThumbnail is whether a poster is stored for this video (see
	// videoColumns). Migration 0028 took the thumbnail_path column that used to
	// stand in for this: a string claiming a file exists was something any
	// metadata write could blank out from under a perfectly good image, and did.
	HasThumbnail bool
	// ThumbnailVersion is the poster's updated_at as a unix stamp, empty when
	// there is none. It exists so the frontend can put it in the thumbnail URL:
	// a URL that changes when the bytes change is what lets the response say
	// "immutable" and be believed, and it is also what makes a re-downloaded
	// poster appear at once instead of after the cache window.
	ThumbnailVersion      string
	MediaPath             string
	FilesizeBytes         int64
	FormatUsed            string
	RequestedFormat       string
	Availability          string
	Status                string
	ErrorMessage          string
	SponsorblockSegments  string
	Watched               bool
	WatchedAt             string
	ResumePositionSeconds float64
	// StateVersion is the row's watched-state generation counter (migration
	// 0014). It is read-only from this struct's point of view: only SetWatched
	// and SetResume's auto-watch path bump it, and both return the new value
	// rather than expecting a caller to set this field.
	StateVersion  int64
	Favorite      bool
	FavoritedAt   string
	CreatedAt     string
	DownloadedAt  string
	AudioLanguage string
	// HasTranscript is whether captions are stored for this video, the same
	// shape as HasThumbnail and for the same reason.
	HasTranscript bool
	Summary       string
	Chapters      string
	KeyPoints     string
	SummaryStatus string
	SummaryError  string
	EmbedModel    string
	EmbedDim      int
	// EmbedRev is the CONTENT recipe the stored chunks follow (which kinds of
	// chunk exist), as opposed to EmbedModel/EmbedDim which describe the model.
	// Below rag.SearchableRecipeRev means the index is unusable and needs
	// rebuilding; below rag.ChunkRecipeRev only that it lacks a newer kind.
	// Ask Indexed() rather than comparing here — see the note on that method.
	EmbedRev int
	Category string
	// ChatUsage is what the chat model has spent analysing this video, summed
	// over every attempt. Zero on a video analysed before migration 0032.
	ChatUsage ChatUsage
	// MediaContainer, VideoCodec, VideoHeight and AudioCodec are what the
	// downloaded file actually is, filled in by mediaprobe. They carry
	// ffprobe's raw values ("mp4", "h264", 1080, "aac"); the UI does the
	// friendly naming. All are empty/zero until the file has been probed.
	MediaContainer string
	VideoCodec     string
	VideoHeight    int64
	AudioCodec     string
	// ProbedAt is when the probe was last ATTEMPTED, success or failure.
	// Empty means never attempted, which is what the backfill sweep selects.
	ProbedAt string
	// MediaType/LiveStatus/YTTags/YTCategories come straight from yt-dlp (see
	// ytdlp.Meta). YTTags/YTCategories are JSON arrays stored as TEXT, like
	// Chapters and KeyPoints, and are YouTube's own labels — not Category,
	// which is peeq's classification enum.
	MediaType    string
	LiveStatus   string
	YTTags       string
	YTCategories string
	// ChannelKeepReads is the owning channel's keep_reads setting, read through
	// the join videoColumns already makes rather than through a channels store
	// the readers would otherwise have to hold — the same way ChannelName
	// arrives. It belongs to the channel, not the video: no write path on this
	// struct touches it, and COALESCE makes a video whose channel row was never
	// created read as on: off purges on delete, and purging a video no switch
	// was ever shown for is the wrong default.
	ChannelKeepReads bool
}

// Indexed reports whether the video's stored chunks are usable by search right
// now: embedded at all, and against a recipe search can still use
// (rag.SearchableRecipeRev, which trails rag.ChunkRecipeRev when a recipe only
// added a kind).
//
// Both halves are needed, and this is the single definition of that pair. Model
// alone is not enough because embed_model is set once and never cleared, so a
// video whose chapters changed under it (SetKeyPoints zeroes embed_rev in the
// same statement that writes them) would still claim to be indexed while search
// served chunks built from an older analysis. Rev alone is not enough because a
// video that was never embedded has rev 0 for a different reason.
//
// The summarize worker asks this before its fallback embed, and the video DTO
// reports it to the Player — which needs it because an embedding failure no
// longer shows up as a summary failure, and "here is your summary, but this
// video is not findable yet" is otherwise unsayable.
func (v Video) Indexed() bool {
	return v.EmbedModel != "" && v.EmbedRev >= rag.SearchableRecipeRev
}

// ChatUsage is what the chat model has spent on one video, in tokens.
//
// The two token lanes are not disjoint: CachedTokens is a SUBSET of
// PromptTokens, and the completion count already includes the model's reasoning
// tokens. Anything deriving a figure from these has to know that.
//
// Deliberately plain int64s rather than an llm.Usage. This package must not
// import internal/llm — it is the storage layer, and a dependency on the client
// that happens to produce the numbers would invert that.
//
// Table: videos
type ChatUsage struct {
	PromptTokens     int64
	CachedTokens     int64
	CompletionTokens int64
}

// Empty reports whether nothing has been accounted for this video, which is the
// state of every row analysed before migration 0032 as well as of any video the
// endpoint never reported usage for. Callers use it to omit a figure rather than
// display a confident zero.
func (u ChatUsage) Empty() bool {
	return u.PromptTokens == 0 && u.CachedTokens == 0 && u.CompletionTokens == 0
}

// ChannelRef is one channel as it appears in the library, for resolving a name
// a reader typed back to the id searches filter on.
//
// Table: -
type ChannelRef struct {
	ID     string
	Name   string
	Handle string
}

// MediaRef is what the media range handler needs from a video: where the
// file is and what to call it on a save-to-disk.
//
// Table: videos
type MediaRef struct {
	ID        string
	Title     string
	MediaPath string
}

// ProbeCandidate is one video whose media file has never been probed.
//
// Table: videos
type ProbeCandidate struct {
	ID        string
	MediaPath string
}

// SponsorblockCandidate is one video due a SponsorBlock segment lookup.
// DurationSeconds travels with it because the client needs it to reject
// segments submitted against a differently-cut copy of the video.
//
// Table: videos
type SponsorblockCandidate struct {
	ID              string
	DurationSeconds int64
}

// StorageGroup is what peeq stores for a set of videos: their media files on
// disk, and in the database their transcripts, summaries (prose, chapters, key
// points, in-depth text), search index and posters.
//
// Table: -
type StorageGroup struct {
	Videos          int   `json:"videos"`
	FileBytes       int64 `json:"file_bytes"`
	TranscriptBytes int64 `json:"transcript_bytes"`
	SummaryBytes    int64 `json:"summary_bytes"`
	// SearchTextBytes is the chunk text, counted twice: transcript_chunks
	// holds it and the full-text index keeps its own copy. FTS5's inverted
	// index on top is not attributable to a channel and is left out.
	SearchTextBytes int64 `json:"search_text_bytes"`
	// VectorBytes is chunks × vector width × 4 (float32): the live payload.
	// Space vec0 keeps after deletes belongs to no channel
	// (rag.CompactVectors reclaims it at boot).
	VectorBytes int64 `json:"vector_bytes"`
	PosterBytes int64 `json:"poster_bytes"`
}

// Thumbnail is one stored poster: the bytes plus what is needed to serve them.
//
// Table: video_thumbnails
type Thumbnail struct {
	Mime      string
	Bytes     []byte
	UpdatedAt string
}

// Transcript is one stored WebVTT track.
//
// Table: video_transcripts
type Transcript struct {
	// Source is TranscriptSourceDownload or TranscriptSourceCaption.
	Source string
	// VTT is the raw WebVTT text, stored verbatim. The <track> element, the
	// browser-side parser and the user-facing .vtt download all want the bytes
	// exactly as yt-dlp wrote them, so this is never normalized or re-rendered.
	VTT       string
	UpdatedAt string
}
