package rag

import (
	"strings"
)

// Filter narrows retrieval to a subset of the library — the structured half of a
// question like "do we have unwatched videos about ontology" or "does Veritasium
// have anything about ontology". The semantic half stays where it was: the topic
// still opens its lanes, and this only says which videos those lanes may draw
// from.
//
// EVERY FIELD IS ALREADY RESOLVED AND VALIDATED. ChannelIDs are real ids looked
// up against the library, Category has been checked against the vocabulary, the
// dates have been through time.Parse. Nothing a model emitted reaches SQL as
// text; the caller does that work (see httpapi/resolve_channel.go), and this
// type is the boundary that makes it obvious when someone stops. The strings
// here are still bound as parameters, never interpolated — the discipline is
// belt and braces, not one or the other.
//
// The zero value means "the whole library", so the unfiltered callers below can
// pass Filter{} and get exactly the query they had before.
type Filter struct {
	// ChannelIDs scopes to one or more channels. ChannelNames is the fallback
	// arm for rows written before channel ids were recorded (videos.channel_id
	// = ''), mirroring videos.Store.List — see the ChannelID branch there.
	ChannelIDs   []string
	ChannelNames []string
	// Watched and Favorite are three-valued: nil means the question said
	// nothing about them, which is not the same as asking for false.
	Watched  *bool
	Favorite *bool
	// Category must already satisfy videos.ValidCategory. Resolved in httpapi
	// rather than here so this package stays free of a dependency on videos.
	Category string
	// After and Before bound the release date, inclusive, as 'YYYY-MM-DD'.
	After, Before string
	// VideoIDs scopes to specific videos — "that one you just cited, what else
	// does it say about Y". Costs one more predicate now that the KNN can
	// pre-filter at all.
	VideoIDs []string
}

// Empty reports that this filter selects the whole library, so the caller can
// skip the subquery entirely and run the exact SQL it ran before filtering
// existed.
func (f Filter) Empty() bool {
	return len(f.ChannelIDs) == 0 && len(f.ChannelNames) == 0 &&
		f.Watched == nil && f.Favorite == nil &&
		f.Category == "" && f.After == "" && f.Before == "" &&
		len(f.VideoIDs) == 0
}

// predicates builds the WHERE fragments against a `videos` row aliased as alias,
// plus their bound arguments. The fragments are ANDed by the caller.
func (f Filter) predicates(alias string) ([]string, []any) {
	var conds []string
	var args []any
	col := func(name string) string { return alias + "." + name }

	if len(f.ChannelIDs) > 0 || len(f.ChannelNames) > 0 {
		var arms []string
		if len(f.ChannelIDs) > 0 {
			arms = append(arms, col("channel_id")+" IN ("+placeholders(len(f.ChannelIDs))+")")
			args = append(args, toAny(f.ChannelIDs)...)
		}
		if len(f.ChannelNames) > 0 {
			// The legacy arm: rows recorded before channel ids were, matched by
			// exact name. Gated on channel_id = '' so it can never widen a
			// by-id match into a by-name one.
			arms = append(arms, "("+col("channel_id")+" = '' AND "+
				col("channel_name")+" IN ("+placeholders(len(f.ChannelNames))+"))")
			args = append(args, toAny(f.ChannelNames)...)
		}
		conds = append(conds, "("+strings.Join(arms, " OR ")+")")
	}

	if f.Watched != nil {
		// Plain watched = 0, deliberately NOT the Library's stricter
		// "status = 'downloaded' AND watched = 0 AND resume_position_seconds = 0"
		// (videos.Store.List, case "unwatched"). That one answers "what can I
		// press play on"; this one answers "what is on the shelf that I have
		// not finished". A half-watched video is a legitimate answer to "do we
		// have unwatched videos about ontology" and would be wrong to hide.
		conds = append(conds, col("watched")+" = ?")
		args = append(args, boolToInt(*f.Watched))
	}
	if f.Favorite != nil {
		conds = append(conds, col("favorite")+" = ?")
		args = append(args, boolToInt(*f.Favorite))
	}
	if f.Category != "" {
		conds = append(conds, col("category")+" = ?")
		args = append(args, f.Category)
	}

	// Same COALESCE/date() normalization sortClauses uses, and for the same
	// reason: published_at is 'YYYY-MM-DD' while created_at is
	// 'YYYY-MM-DD HH:MM:SS', so comparing the shapes lexically would put a
	// same-day date-only value before the datetime one. Videos yt-dlp reported
	// no release date for fall back to when peeq recorded them, rather than
	// dropping out of every dated question.
	if f.After != "" || f.Before != "" {
		released := "COALESCE(" + col("published_at") + ", date(" + col("created_at") + "))"
		if f.After != "" {
			conds = append(conds, released+" >= ?")
			args = append(args, f.After)
		}
		if f.Before != "" {
			conds = append(conds, released+" <= ?")
			args = append(args, f.Before)
		}
	}

	if len(f.VideoIDs) > 0 {
		conds = append(conds, col("id")+" IN ("+placeholders(len(f.VideoIDs))+")")
		args = append(args, toAny(f.VideoIDs)...)
	}
	return conds, args
}

// chunkSubquery renders the id set this filter admits, as a subquery over
// transcript_chunks. Callers splice it into `rowid IN (...)` for the vector lane;
// the FTS lane joins videos directly instead, because FTS5 is not a top-k
// operator and a plain join there is already a pre-filter.
func (f Filter) chunkSubquery() (string, []any) {
	conds, args := f.predicates("fv")
	return `SELECT ftc.id FROM transcript_chunks ftc
			JOIN videos fv ON fv.id = ftc.video_id
			WHERE ` + strings.Join(conds, " AND "), args
}

func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

func toAny(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
