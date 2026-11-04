package review

import (
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"net/url"
)

func (s *Server) Store(f Factory) (*Store, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if st, ok := s.stores[f.Dir]; ok {
		return st, nil
	}
	st, err := OpenStore(f.Dir, s.now)
	if err != nil {
		return nil, err
	}
	s.stores[f.Dir] = st
	return st, nil
}

func (s *Server) OpenCount(fileURL string) (int, bool) {
	u, err := url.Parse(fileURL)
	if err != nil || u.Scheme != "file" {
		return 0, false
	}
	f, rel, ok := s.locate(u.Path)
	if !ok {
		return 0, false
	}
	st, err := s.Store(f)
	if err != nil {
		return 0, false
	}
	return st.OpenCount(rel), true
}

func (s *Server) ThreadsChanged(f Factory) {
	s.broadcast(event{"threads", f.Key})
}

func (s *Server) mutation(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if origin := r.Header.Get("Origin"); origin != "" && origin != "http://"+r.Host {
			http.Error(w, "cross-origin request refused", http.StatusForbidden)
			return
		}
		if r.Method != http.MethodDelete {
			if kind, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); kind != "application/json" {
				http.Error(w, "send JSON", http.StatusUnsupportedMediaType)
				return
			}
		}
		next(w, r)
	}
}

func (s *Server) storeFor(w http.ResponseWriter, r *http.Request) (Factory, *Store, bool) {
	f, ok := s.factory(r.PathValue("key"))
	if !ok {
		http.NotFound(w, r)
		return Factory{}, nil, false
	}
	st, err := s.Store(f)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return Factory{}, nil, false
	}
	return f, st, true
}

func (s *Server) listThreads(w http.ResponseWriter, r *http.Request) {
	_, st, ok := s.storeFor(w, r)
	if !ok {
		return
	}
	threads := st.Threads(r.URL.Query().Get("page"))
	if threads == nil {
		threads = []Thread{}
	}
	writeJSON(w, http.StatusOK, threads)
}

func (s *Server) createThread(w http.ResponseWriter, r *http.Request) {
	var n NewThread
	if !readJSON(w, r, &n) {
		return
	}
	s.respond(w, r, func(st *Store) (any, error) { return st.Create(n) })
}

func (s *Server) updateThread(w http.ResponseWriter, r *http.Request) {
	var c Change
	if !readJSON(w, r, &c) {
		return
	}
	s.respond(w, r, func(st *Store) (any, error) { return st.Update(r.PathValue("id"), c) })
}

func (s *Server) replyThread(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Text string `json:"text"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	s.respond(w, r, func(st *Store) (any, error) { return st.Reply(r.PathValue("id"), "you", body.Text, false) })
}

func (s *Server) deleteThread(w http.ResponseWriter, r *http.Request) {
	s.respond(w, r, func(st *Store) (any, error) { return map[string]bool{"deleted": true}, st.Delete(r.PathValue("id")) })
}

func (s *Server) respond(w http.ResponseWriter, r *http.Request, change func(*Store) (any, error)) {
	f, st, ok := s.storeFor(w, r)
	if !ok {
		return
	}
	result, err := change(st)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.ThreadsChanged(f)
	writeJSON(w, http.StatusOK, result)
}

func readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(v); err != nil {
		http.Error(w, "bad JSON: "+err.Error(), http.StatusBadRequest)
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
