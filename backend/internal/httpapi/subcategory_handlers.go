package httpapi

import (
	"net/http"
	"slices"

	"github.com/trick77/peeq/internal/videos"
	"github.com/trick77/ship/kit/httpx"
)

// subcategoryDTO is one subcategory as the UI shows it. The vocabulary lives
// in Go only (videos.Subcategories); the SPA reads labels from here rather
// than mirroring a hundred-odd entries the way it mirrors the categories.
type subcategoryDTO struct {
	ID            string `json:"id"`
	Label         string `json:"label"`
	Category      string `json:"category"`
	CategoryLabel string `json:"category_label"`
}

// toSubcategoryDTO labels a key. An unknown one (a key retired from the
// vocabulary but still stored) shows as itself rather than as nothing.
func toSubcategoryDTO(key string) subcategoryDTO {
	s, ok := videos.LookupSubcategory(key)
	if !ok {
		return subcategoryDTO{ID: key, Label: key}
	}
	return subcategoryDTO{ID: key, Label: s.Label, Category: s.Category, CategoryLabel: videos.CategoryLabel(s.Category)}
}

func toSubcategoryDTOs(keys []string) []subcategoryDTO {
	out := make([]subcategoryDTO, len(keys))
	for i, k := range keys {
		out[i] = toSubcategoryDTO(k)
	}
	return out
}

// handleListSubcategories serves the whole vocabulary, in order: what the
// channel page's editor offers.
func (s *server) handleListSubcategories(w http.ResponseWriter, _ *http.Request) {
	httpx.WriteJSON(w, http.StatusOK, toSubcategoryDTOs(videos.SubcategoryKeys()))
}

type channelSubcategoriesRequest struct {
	Subcategories *[]string `json:"subcategories"`
}

// handlePutChannelSubcategories sets a channel's subcategories by hand. The
// user's labels are final: the classifier never overwrites them. An empty list
// is not "no labels" but "decide for me": it clears the manual flag and hands
// the channel back to the classifier, so there is always a way out of a pick.
func (s *server) handlePutChannelSubcategories(w http.ResponseWriter, r *http.Request) {
	if s.channels == nil {
		httpx.WriteError(w, http.StatusServiceUnavailable, "channels are not configured")
		return
	}
	id := r.PathValue("id")
	var req channelSubcategoriesRequest
	if !httpx.DecodeJSONWith(w, r, &req, maxJSONBody, httpx.DecodeOptions{BadRequest: "invalid request body"}) {
		return
	}
	if req.Subcategories == nil {
		httpx.WriteError(w, http.StatusBadRequest, "subcategories is required")
		return
	}
	keys := *req.Subcategories
	if len(keys) > videos.MaxSubcategories {
		httpx.WriteError(w, http.StatusBadRequest, "at most 3 subcategories")
		return
	}
	for i, k := range keys {
		if !videos.ValidSubcategory(k) || slices.Contains(keys[:i], k) {
			httpx.WriteError(w, http.StatusBadRequest, "unknown or repeated subcategory: "+k)
			return
		}
	}
	c, err := s.channels.Get(id)
	if err != nil {
		httpx.ServerError(w, r, err, "load channel failed")
		return
	}
	if c == nil {
		httpx.WriteError(w, http.StatusNotFound, "channel not found")
		return
	}
	if len(keys) == 0 {
		err = s.channels.ClearSubcategories(r.Context(), id)
	} else {
		err = s.channels.SetSubcategoriesManual(r.Context(), id, keys)
	}
	if err != nil {
		httpx.ServerError(w, r, err, "save subcategories failed")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"subcategories": toSubcategoryDTOs(keys), "subcategories_manual": len(keys) > 0,
	})
}
