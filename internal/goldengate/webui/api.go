package webui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"sync"

	"github.com/divyanshusingh2903/goldengate/internal/goldengate/tui"
)

// decode reads a JSON body into v, refusing unknown fields.
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		deny(w, http.StatusBadRequest)
		return false
	}
	return true
}

func reply(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(v)
}

// fail reports a backend error as a message meant for the user. Backend
// errors are the ones the terminal UI shows on screen; they carry no secret.
func fail(w http.ResponseWriter, code int, kind, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": kind, "message": msg})
}

// out answers with v, or with the backend's error.
func out(w http.ResponseWriter, v any, err error) {
	if err != nil {
		fail(w, http.StatusUnprocessableEntity, "failed", err.Error())
		return
	}
	reply(w, v)
}

type message struct {
	Message string `json:"message"`
}

func (s *Server) routes(mux *http.ServeMux) {
	be := s.be
	mux.HandleFunc("POST /api/session", s.session)

	// --- projects, preview, sharing ---
	mux.HandleFunc("GET /api/snapshot", func(w http.ResponseWriter, r *http.Request) {
		v, err := be.Snapshot(r.Context())
		out(w, v, err)
	})
	mux.HandleFunc("GET /api/preview", func(w http.ResponseWriter, r *http.Request) {
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		v, err := be.Preview(r.Context(), r.URL.Query().Get("key"), limit)
		out(w, v, err)
	})
	mux.HandleFunc("POST /api/apply", func(w http.ResponseWriter, r *http.Request) {
		var a tui.Action
		if !decode(w, r, &a) {
			return
		}
		res, err := be.Apply(r.Context(), a)
		if err != nil {
			out(w, nil, err)
			return
		}
		// the backend's handle for "also delete from the server" is opaque
		// and stays here; the page gets an id for it
		v := struct {
			Message string
			Remote  *remotePrompt
		}{Message: res.Message}
		if res.Remote != nil {
			id := randomToken()[:16]
			s.mu.Lock()
			s.handles[id] = res.Remote.Handle
			s.mu.Unlock()
			v.Remote = &remotePrompt{Count: res.Remote.Count, ID: id}
		}
		reply(w, v)
	})
	mux.HandleFunc("POST /api/undo", func(w http.ResponseWriter, r *http.Request) {
		if !decode(w, r, &struct{}{}) {
			return
		}
		msg, err := be.Undo(r.Context())
		out(w, message{msg}, err)
	})
	mux.HandleFunc("POST /api/delete-remote", func(w http.ResponseWriter, r *http.Request) {
		var in struct{ ID string }
		if !decode(w, r, &in) {
			return
		}
		s.mu.Lock()
		h, ok := s.handles[in.ID]
		delete(s.handles, in.ID)
		s.mu.Unlock()
		if !ok {
			fail(w, http.StatusNotFound, "gone", "that offer is no longer open")
			return
		}
		msg, err := be.DeleteRemote(r.Context(), h)
		out(w, message{msg}, err)
	})

	// --- sending ---
	mux.HandleFunc("GET /api/plan", func(w http.ResponseWriter, r *http.Request) {
		v, err := be.Plan(r.Context(), tui.SyncOpts{Resummarize: r.URL.Query().Get("resummarize") == "true"})
		out(w, v, err)
	})
	mux.HandleFunc("POST /api/sync", s.syncStart)
	mux.HandleFunc("GET /api/sync/{id}/events", s.syncEvents)
	mux.HandleFunc("POST /api/sync/{id}/cancel", s.syncCancel)

	// --- server ---
	mux.HandleFunc("GET /api/server", func(w http.ResponseWriter, r *http.Request) {
		v, err := be.Server(r.Context())
		out(w, v, err)
	})
	mux.HandleFunc("POST /api/server/delete-project", func(w http.ResponseWriter, r *http.Request) {
		var in struct{ Ref string }
		if !decode(w, r, &in) {
			return
		}
		out(w, message{"deleted from the server"}, be.DeleteServerProject(r.Context(), in.Ref))
	})

	// --- settings ---
	mux.HandleFunc("GET /api/settings", func(w http.ResponseWriter, r *http.Request) {
		v, err := be.Settings(r.Context())
		out(w, v, err)
	})
	mux.HandleFunc("POST /api/settings", func(w http.ResponseWriter, r *http.Request) {
		var in tui.Settings
		if !decode(w, r, &in) {
			return
		}
		out(w, message{"saved"}, be.SaveSettings(r.Context(), in))
	})
	// a secret goes in and never comes back: the answer says only that it is set
	mux.HandleFunc("POST /api/summarizer/key", func(w http.ResponseWriter, r *http.Request) {
		var in struct{ Provider, Key string }
		if !decode(w, r, &in) {
			return
		}
		out(w, map[string]bool{"set": in.Key != ""}, be.SetSummarizerKey(r.Context(), in.Provider, in.Key))
	})
	mux.HandleFunc("POST /api/summarizer/test", func(w http.ResponseWriter, r *http.Request) {
		if !decode(w, r, &struct{}{}) {
			return
		}
		msg, err := be.TestSummarizer(r.Context())
		out(w, message{msg}, err)
	})
	mux.HandleFunc("GET /api/summarizer/models", func(w http.ResponseWriter, r *http.Request) {
		v, err := be.SummarizerModels(r.Context(), r.URL.Query().Get("provider"), r.URL.Query().Get("base_url"))
		out(w, v, err)
	})

	// --- setup ---
	mux.HandleFunc("GET /api/detect", func(w http.ResponseWriter, r *http.Request) {
		v, err := be.Detect(r.Context())
		out(w, v, err)
	})
	// Init returns the assistant token: the one value shown once, at the
	// moment it is created, because the user must copy it elsewhere. The
	// admin secret that came in is not echoed and not kept.
	mux.HandleFunc("POST /api/init", func(w http.ResponseWriter, r *http.Request) {
		var in tui.InitParams
		if !decode(w, r, &in) {
			return
		}
		v, err := be.Init(r.Context(), in)
		out(w, v, err)
	})

	// --- inbox ---
	mux.HandleFunc("GET /api/inbox", func(w http.ResponseWriter, r *http.Request) {
		v, err := be.Inbox(r.Context())
		out(w, v, err)
	})
	mux.HandleFunc("POST /api/inbox/decide", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Items    []tui.InboxItem
			Decision string
		}
		if !decode(w, r, &in) {
			return
		}
		out(w, message{"done"}, be.InboxDecide(r.Context(), in.Items, in.Decision))
	})
	mux.HandleFunc("POST /api/edit/begin", s.editBegin)
	mux.HandleFunc("POST /api/edit/finish", s.editFinish)
	mux.HandleFunc("POST /api/offers/decide", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			ID     string
			Accept bool
		}
		if !decode(w, r, &in) {
			return
		}
		msg, err := be.OfferDecide(r.Context(), in.ID, in.Accept)
		out(w, message{msg}, err)
	})
	mux.HandleFunc("GET /api/receipts", func(w http.ResponseWriter, r *http.Request) {
		v, err := be.Receipts(r.Context())
		out(w, v, err)
	})

	// --- agents: every call needs the admin secret, held here for the run ---
	mux.HandleFunc("POST /api/admin", func(w http.ResponseWriter, r *http.Request) {
		var in struct{ Secret string }
		if !decode(w, r, &in) {
			return
		}
		if in.Secret == "" {
			fail(w, http.StatusBadRequest, "failed", "the admin secret is empty")
			return
		}
		// prove it before keeping it
		if _, err := be.Agents(r.Context(), in.Secret); err != nil {
			out(w, nil, err)
			return
		}
		s.mu.Lock()
		s.admin = in.Secret
		s.mu.Unlock()
		reply(w, map[string]bool{"set": true})
	})
	mux.HandleFunc("POST /api/admin/forget", func(w http.ResponseWriter, r *http.Request) {
		if !decode(w, r, &struct{}{}) {
			return
		}
		s.mu.Lock()
		s.admin = ""
		s.mu.Unlock()
		reply(w, map[string]bool{"set": false})
	})
	mux.HandleFunc("GET /api/agents", s.withAdmin(func(w http.ResponseWriter, r *http.Request, admin string) {
		v, err := be.Agents(r.Context(), admin)
		out(w, v, err)
	}))
	mux.HandleFunc("GET /api/agents/view", s.withAdmin(func(w http.ResponseWriter, r *http.Request, admin string) {
		v, err := be.AgentView(r.Context(), admin, r.URL.Query().Get("agent"))
		out(w, v, err)
	}))
	mux.HandleFunc("POST /api/agents/grants", s.withAdmin(func(w http.ResponseWriter, r *http.Request, admin string) {
		var in struct{ Changes []tui.GrantChange }
		if !decode(w, r, &in) {
			return
		}
		out(w, message{"applied"}, be.ApplyGrants(r.Context(), admin, in.Changes))
	}))
	mux.HandleFunc("POST /api/agents/revoke-token", s.withAdmin(func(w http.ResponseWriter, r *http.Request, admin string) {
		var in struct{ TokenID string }
		if !decode(w, r, &in) {
			return
		}
		out(w, message{"revoked"}, be.RevokeAgentToken(r.Context(), admin, in.TokenID))
	}))
	// the one response that carries a secret: the new token, shown once
	mux.HandleFunc("POST /api/agents/new-token", s.withAdmin(func(w http.ResponseWriter, r *http.Request, admin string) {
		var in struct{ AgentID string }
		if !decode(w, r, &in) {
			return
		}
		v, err := be.MintAgentToken(r.Context(), admin, in.AgentID)
		out(w, v, err)
	}))
}

type remotePrompt struct {
	Count int
	ID    string
}

// withAdmin runs h with the admin secret, or says that it is needed.
func (s *Server) withAdmin(h func(http.ResponseWriter, *http.Request, string)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		admin := s.admin
		s.mu.Unlock()
		if admin == "" {
			fail(w, http.StatusUnauthorized, "admin_required", "the admin secret is needed for this; it is kept in memory for this run only")
			return
		}
		h(w, r, admin)
	}
}

// --- editing a summary ---
//
// The terminal UI hands the backend's private form file to an editor. Here
// the form's text goes to the page and comes back; the file stays private to
// this process and the backend deletes it when the edit is finished.

func (s *Server) editBegin(w http.ResponseWriter, r *http.Request) {
	var in struct{ Item tui.InboxItem }
	if !decode(w, r, &in) {
		return
	}
	path, err := s.be.EditBegin(r.Context(), in.Item)
	if err != nil {
		out(w, nil, err)
		return
	}
	text, err := os.ReadFile(path) //nolint:gosec // G304: the form file the backend just created for this edit
	if err != nil {
		_, _ = s.be.EditFinish(r.Context(), in.Item, "") // let the backend clean up
		fail(w, http.StatusInternalServerError, "failed", "could not read the edit form")
		return
	}
	id := randomToken()[:16]
	s.mu.Lock()
	s.edits[id] = edit{item: in.Item, path: path}
	s.mu.Unlock()
	reply(w, map[string]string{"id": id, "text": string(text)})
}

func (s *Server) editFinish(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ID     string
		Text   string
		Cancel bool
	}
	if !decode(w, r, &in) {
		return
	}
	s.mu.Lock()
	e, ok := s.edits[in.ID]
	delete(s.edits, in.ID)
	s.mu.Unlock()
	if !ok {
		fail(w, http.StatusNotFound, "gone", "that edit is no longer open")
		return
	}
	if in.Cancel {
		_, _ = s.be.EditFinish(r.Context(), e.item, "") // the backend removes the form
		reply(w, message{"edit cancelled"})
		return
	}
	if err := os.WriteFile(e.path, []byte(in.Text), 0o600); err != nil {
		_, _ = s.be.EditFinish(r.Context(), e.item, "")
		fail(w, http.StatusInternalServerError, "failed", "could not save the edit form")
		return
	}
	msg, err := s.be.EditFinish(r.Context(), e.item, e.path)
	out(w, message{msg}, err)
}

// --- sending, with progress as server-sent events ---

type syncRun struct {
	id     string
	cancel context.CancelFunc
	mu     sync.Mutex
	cond   *sync.Cond
	events []tui.SyncEvent
	done   bool
	err    string
}

func (s *Server) syncStart(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Dry         bool
		Resummarize bool
	}
	if !decode(w, r, &in) {
		return
	}
	s.mu.Lock()
	if s.run != nil {
		s.run.mu.Lock()
		busy := !s.run.done
		s.run.mu.Unlock()
		if busy {
			s.mu.Unlock()
			fail(w, http.StatusConflict, "busy", "a send is already running")
			return
		}
	}
	ctx, cancel := context.WithCancel(context.Background()) // outlives the request that started it
	run := &syncRun{id: randomToken()[:16], cancel: cancel}
	run.cond = sync.NewCond(&run.mu)
	s.run = run
	s.mu.Unlock()
	go func() {
		err := s.be.Sync(ctx, in.Dry, tui.SyncOpts{Resummarize: in.Resummarize}, func(e tui.SyncEvent) {
			run.mu.Lock()
			run.events = append(run.events, e)
			run.mu.Unlock()
			run.cond.Broadcast()
		})
		cancel()
		run.mu.Lock()
		run.done = true
		if err != nil && !errors.Is(err, context.Canceled) {
			run.err = err.Error()
		}
		run.mu.Unlock()
		run.cond.Broadcast()
	}()
	reply(w, map[string]string{"id": run.id})
}

func (s *Server) runByID(id string) *syncRun {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.run == nil || !same(id, s.run.id) {
		return nil
	}
	return s.run
}

func (s *Server) syncCancel(w http.ResponseWriter, r *http.Request) {
	if !decode(w, r, &struct{}{}) {
		return
	}
	run := s.runByID(r.PathValue("id"))
	if run == nil {
		fail(w, http.StatusNotFound, "gone", "no such send")
		return
	}
	run.cancel()
	reply(w, message{"cancelling"})
}

// syncEvents streams a run's events from the beginning, then one "end".
func (s *Server) syncEvents(w http.ResponseWriter, r *http.Request) {
	run := s.runByID(r.PathValue("id"))
	fl, ok := w.(http.Flusher)
	if run == nil || !ok {
		fail(w, http.StatusNotFound, "gone", "no such send")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	// wake the wait below when the browser goes away
	stop := context.AfterFunc(r.Context(), func() { run.cond.Broadcast() })
	defer stop()
	next := 0
	for {
		run.mu.Lock()
		for next >= len(run.events) && !run.done && r.Context().Err() == nil {
			run.cond.Wait()
		}
		batch := append([]tui.SyncEvent(nil), run.events[next:]...)
		done, errText := run.done, run.err
		run.mu.Unlock()
		if r.Context().Err() != nil {
			return
		}
		for _, e := range batch {
			b, _ := json.Marshal(e)
			fmt.Fprintf(w, "event: sync\ndata: %s\n\n", b)
		}
		next += len(batch)
		if done && len(batch) == 0 {
			b, _ := json.Marshal(map[string]string{"error": errText})
			fmt.Fprintf(w, "event: end\ndata: %s\n\n", b)
			fl.Flush()
			return
		}
		fl.Flush()
	}
}
