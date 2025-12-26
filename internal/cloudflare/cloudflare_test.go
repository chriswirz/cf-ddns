package cloudflare

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
)

// stub serves the handful of endpoints the client uses.
func stub(t *testing.T, patched *Record) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer tok" {
			t.Errorf("Authorization = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/zones":
			io.WriteString(w, `{"success":true,"errors":[],"result":[{"id":"zone1","name":"example.com"}]}`)
		case r.URL.Path == "/zones/zone1/dns_records" && r.Method == http.MethodGet:
			if r.URL.Query().Get("name") == "missing.example.com" {
				io.WriteString(w, `{"success":true,"errors":[],"result":[]}`)
				return
			}
			io.WriteString(w, `{"success":true,"errors":[],"result":[{"id":"rec1","type":"A","name":"home.example.com","content":"198.51.100.1","ttl":1,"proxied":false}]}`)
		case r.URL.Path == "/zones/zone1/dns_records/rec1" && r.Method == http.MethodPatch:
			if err := json.NewDecoder(r.Body).Decode(patched); err != nil {
				t.Error(err)
			}
			io.WriteString(w, `{"success":true,"errors":[],"result":{}}`)
		default:
			w.WriteHeader(http.StatusNotFound)
			io.WriteString(w, `{"success":false,"errors":[{"code":7003,"message":"no route"}],"result":null}`)
		}
	}))
	t.Cleanup(srv.Close)
	old := BaseURL
	BaseURL = srv.URL
	t.Cleanup(func() { BaseURL = old })
	return New("tok")
}

func TestUpdateFlow(t *testing.T) {
	var patched Record
	c := stub(t, &patched)
	ctx := context.Background()

	id, err := c.ZoneID(ctx, "example.com")
	if err != nil || id != "zone1" {
		t.Fatalf("ZoneID = %q, %v", id, err)
	}
	rec, err := c.FindRecord(ctx, id, "home.example.com", "A")
	if err != nil || rec == nil {
		t.Fatalf("FindRecord = %v, %v", rec, err)
	}
	if rec.Content != "198.51.100.1" {
		t.Fatalf("content = %q", rec.Content)
	}
	want := Record{Type: "A", Name: rec.Name, Content: "203.0.113.9", TTL: 60}
	if err := c.UpdateRecord(ctx, id, rec.ID, want); err != nil {
		t.Fatal(err)
	}
	if patched.Content != "203.0.113.9" || patched.TTL != 60 || patched.ID != "" {
		t.Fatalf("patched body = %+v", patched)
	}
}

func TestMissingRecordIsNotAnError(t *testing.T) {
	c := stub(t, &Record{})
	rec, err := c.FindRecord(context.Background(), "zone1", "missing.example.com", "A")
	if err != nil {
		t.Fatal(err)
	}
	if rec != nil {
		t.Fatalf("want nil record, got %+v", rec)
	}
}

func TestAPIError(t *testing.T) {
	c := stub(t, &Record{})
	err := c.UpdateRecord(context.Background(), "zone1", "nope", Record{Type: "A"})
	apiErr, ok := err.(*APIError)
	if !ok {
		t.Fatalf("want *APIError, got %T: %v", err, err)
	}
	if apiErr.Retryable() {
		t.Error("404 should not be retryable")
	}
}

// pagedStub serves `total` records across pages of `perPage`, so the client's
// pagination loop is exercised for real.
func pagedStub(t *testing.T, total int) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		page, _ := strconv.Atoi(q.Get("page"))
		if page < 1 {
			page = 1
		}
		size, _ := strconv.Atoi(q.Get("per_page"))
		if size < 1 {
			size = perPage
		}
		start := (page - 1) * size
		end := start + size
		if end > total {
			end = total
		}
		var items []Record
		for i := start; i < end; i++ {
			items = append(items, Record{
				ID:      fmt.Sprintf("rec%d", i),
				Type:    q.Get("type"),
				Name:    fmt.Sprintf("host%d.example.com", i),
				Content: "198.51.100.1",
				TTL:     1,
			})
		}
		totalPages := (total + size - 1) / size
		body, err := json.Marshal(map[string]any{
			"success": true,
			"errors":  []any{},
			"result":  items,
			"result_info": map[string]int{
				"page": page, "per_page": size, "count": len(items),
				"total_count": total, "total_pages": totalPages,
			},
		})
		if err != nil {
			t.Error(err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(body)
	}))
	t.Cleanup(srv.Close)
	old := BaseURL
	BaseURL = srv.URL
	t.Cleanup(func() { BaseURL = old })
	return New("tok")
}

func TestListRecordsPaginates(t *testing.T) {
	// 250 records is three pages of 100, and the last one is short.
	c := pagedStub(t, 250)
	recs, err := c.ListRecords(context.Background(), "zone1", "A")
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 250 {
		t.Fatalf("got %d records, want 250", len(recs))
	}
	if recs[0].Name != "host0.example.com" || recs[249].Name != "host249.example.com" {
		t.Errorf("wrong records at the edges: %q .. %q", recs[0].Name, recs[249].Name)
	}
}

func TestListRecordsExactPageBoundary(t *testing.T) {
	// A total that is an exact multiple of the page size is the case where a
	// naive loop either stops early or fetches one page too many.
	c := pagedStub(t, 200)
	recs, err := c.ListRecords(context.Background(), "zone1", "A")
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 200 {
		t.Fatalf("got %d records, want 200", len(recs))
	}
}

func TestListRecordsBothTypes(t *testing.T) {
	c := pagedStub(t, 5)
	recs, err := c.ListRecords(context.Background(), "zone1", "A", "AAAA")
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 10 {
		t.Fatalf("got %d records, want 5 of each type", len(recs))
	}
	var a, aaaa int
	for _, r := range recs {
		switch r.Type {
		case "A":
			a++
		case "AAAA":
			aaaa++
		}
	}
	if a != 5 || aaaa != 5 {
		t.Errorf("A=%d AAAA=%d, want 5 and 5", a, aaaa)
	}
}

func TestUpdateOmitsComment(t *testing.T) {
	var patched map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&patched)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"success":true,"errors":[],"result":{}}`)
	}))
	defer srv.Close()
	old := BaseURL
	BaseURL = srv.URL
	defer func() { BaseURL = old }()

	c := New("tok")
	err := c.UpdateRecord(context.Background(), "z", "r",
		Record{Type: "A", Name: "a.example.com", Content: "203.0.113.1", TTL: 1})
	if err != nil {
		t.Fatal(err)
	}
	// A PATCH carrying an empty comment would wipe one set in the dashboard.
	if _, ok := patched["comment"]; ok {
		t.Errorf("update body should not carry a comment: %v", patched)
	}
}
