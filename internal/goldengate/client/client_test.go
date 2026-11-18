package client

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/divyanshusingh2903/goldengate/internal/model"
)

// Uploads are split by count and by size, so payloads that are large but
// valid never make one request too big for the server.
func TestSendSplitsBatchesByCountAndSize(t *testing.T) {
	var sizes, counts []int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var in struct {
			Sessions []json.RawMessage `json:"sessions"`
		}
		_ = json.Unmarshal(b, &in)
		sizes, counts = append(sizes, len(b)), append(counts, len(in.Sessions))
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	c, err := New(srv.URL, "t", srv.Client(), "test")
	if err != nil {
		t.Fatal(err)
	}
	big := strings.Repeat("x", 150<<10)
	var ps []*model.SessionPayload
	for i := 0; i < 30; i++ {
		ps = append(ps, &model.SessionPayload{SessionID: "s", TitleOnly: big})
	}
	if err := c.Send(context.Background(), ps); err != nil {
		t.Fatal(err)
	}
	total := 0
	for i, n := range counts {
		total += n
		if n > BatchSize || sizes[i] > BatchBytes+4<<10 {
			t.Errorf("request %d: %d payloads, %d bytes", i, n, sizes[i])
		}
	}
	if total != 30 || len(counts) < 3 {
		t.Errorf("all payloads go, in several requests: %v", counts)
	}
}
