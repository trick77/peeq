package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"testing"

	"github.com/trick77/peeq/internal/channels"
	"github.com/trick77/peeq/internal/videos"
)

func TestSubcategoriesCatalog(t *testing.T) {
	h := New(channelsTestDeps(t, &testResolver{}))
	rec := doReq(t, h, loginAndGetCookie(t, h), http.MethodGet, "/api/subcategories", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var got []subcategoryDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != len(videos.Subcategories) {
		t.Fatalf("%d entries, want %d", len(got), len(videos.Subcategories))
	}
	i := slices.IndexFunc(got, func(s subcategoryDTO) bool { return s.ID == "sports/cycling" })
	if i < 0 || got[i].Label != "Cycling" || got[i].Category != "sports" || got[i].CategoryLabel != "Sports & Fitness" {
		t.Fatalf("sports/cycling = %+v", got)
	}
}

func TestChannelSubcategories_detailAndEdit(t *testing.T) {
	deps := channelsTestDeps(t, &testResolver{})
	h := New(deps)
	cookie := loginAndGetCookie(t, h)
	ctx := context.Background()
	if err := deps.Channels.Upsert(channels.Channel{ID: "UCgcn", Name: "GCN"}); err != nil {
		t.Fatal(err)
	}
	if _, err := deps.Channels.SetSubcategoriesIfAuto(ctx, "UCgcn", []string{"sports/cycling"}, 3); err != nil {
		t.Fatal(err)
	}

	detail := func() channelDetail {
		t.Helper()
		rec := doReq(t, h, cookie, http.MethodGet, "/api/channels/UCgcn", nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("detail status %d: %s", rec.Code, rec.Body)
		}
		var d channelDetail
		if err := json.Unmarshal(rec.Body.Bytes(), &d); err != nil {
			t.Fatal(err)
		}
		return d
	}
	if d := detail(); d.SubcategoriesManual || len(d.Subcategories) != 1 || d.Subcategories[0].ID != "sports/cycling" || d.Subcategories[0].Label != "Cycling" {
		t.Fatalf("detail = %+v", d.Subcategories)
	}

	rec := doReq(t, h, cookie, http.MethodPut, "/api/channels/UCgcn/subcategories",
		[]byte(`{"subcategories":["tech/wearables","sports/cycling"]}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("put status %d: %s", rec.Code, rec.Body)
	}
	d := detail()
	if !d.SubcategoriesManual || len(d.Subcategories) != 2 || d.Subcategories[0].ID != "tech/wearables" {
		t.Fatalf("after put = %+v manual=%v", d.Subcategories, d.SubcategoriesManual)
	}

	// An empty list hands the channel back to the classifier.
	if rec := doReq(t, h, cookie, http.MethodPut, "/api/channels/UCgcn/subcategories", []byte(`{"subcategories":[]}`)); rec.Code != http.StatusOK {
		t.Fatalf("clear status %d", rec.Code)
	}
	if d := detail(); d.SubcategoriesManual || len(d.Subcategories) != 0 {
		t.Fatalf("after clear = %+v manual=%v", d.Subcategories, d.SubcategoriesManual)
	}
}

func TestChannelSubcategories_putRejectsBadInput(t *testing.T) {
	deps := channelsTestDeps(t, &testResolver{})
	h := New(deps)
	cookie := loginAndGetCookie(t, h)
	if err := deps.Channels.Upsert(channels.Channel{ID: "UCgcn", Name: "GCN"}); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"unknown key": `{"subcategories":["sports/knitting"]}`,
		"bare id":     `{"subcategories":["cycling"]}`,
		"too many":    `{"subcategories":["ai/building","ai/models","ai/infra","ai/business"]}`,
		"duplicate":   `{"subcategories":["ai/building","ai/building"]}`,
		"no field":    `{}`,
	} {
		if rec := doReq(t, h, cookie, http.MethodPut, "/api/channels/UCgcn/subcategories", []byte(body)); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", name, rec.Code)
		}
	}
	if rec := doReq(t, h, cookie, http.MethodPut, "/api/channels/UCnope/subcategories", []byte(`{"subcategories":[]}`)); rec.Code != http.StatusNotFound {
		t.Errorf("unknown channel: status %d, want 404", rec.Code)
	}
}

func TestGetVideo_carriesSubcategories(t *testing.T) {
	deps, _ := videosTestDeps(t)
	h := New(deps)
	cookie := loginAndGetCookie(t, h)
	if err := deps.Videos.Upsert(videos.Video{ID: "v1", URL: "u", Title: "T"}); err != nil {
		t.Fatal(err)
	}
	if err := deps.Videos.SetCategory("v1", "sports"); err != nil {
		t.Fatal(err)
	}
	if _, err := deps.Videos.SetSubcategoriesIfCategory(context.Background(), "v1", "sports", []string{"sports/cycling", "sports/fitness"}); err != nil {
		t.Fatal(err)
	}
	rec := doReq(t, h, cookie, http.MethodGet, "/api/videos/v1", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var v struct {
		Subcategories []subcategoryDTO `json:"subcategories"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	if len(v.Subcategories) != 2 || v.Subcategories[0].Label != "Cycling" || v.Subcategories[1].ID != "sports/fitness" {
		t.Fatalf("subcategories = %+v", v.Subcategories)
	}
}
