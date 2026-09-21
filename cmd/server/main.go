package main

import (
	"embed"
	"encoding/json"
	"flag"
	"log"
	"net/http"

	"yemai/internal/forensic"
	"yemai/internal/store"
)

//go:embed web/*
var webFS embed.FS

type Server struct {
	st  *store.Store
	lim forensic.Limits
}

func main() {
	addr := flag.String("addr", "127.0.0.1:5246", "listen address")
	dataDir := flag.String("data", "data", "data directory")
	flag.Parse()

	st, err := store.Open(*dataDir)
	if err != nil {
		log.Fatalf("open store: %v", err)
	}
	defer st.Close()

	srv := &Server{st: st, lim: forensic.DefaultLimits}
	mux := srv.routes()
	log.Printf("页脉探针 listening on http://%s (data: %s)", *addr, *dataDir)
	if err := http.ListenAndServe(*addr, mux); err != nil {
		log.Fatal(err)
	}
}

func (s *Server) routes() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", s.handleIndex)
	mux.HandleFunc("GET /api/documents", s.handleList)
	mux.HandleFunc("POST /api/documents", s.handleUpload)
	mux.HandleFunc("GET /api/documents/{id}", s.handleGet)
	mux.HandleFunc("GET /api/documents/{id}/branches", s.handleBranches)
	mux.HandleFunc("POST /api/documents/{id}/branches", s.handleFork)
	mux.HandleFunc("GET /api/branches/{bid}/decisions", s.handleDecisions)
	mux.HandleFunc("PUT /api/branches/{bid}/decisions", s.handlePutDecision)
	mux.HandleFunc("GET /api/documents/{id}/hex", s.handleHex)
	mux.HandleFunc("GET /api/documents/{id}/candidate/{cid}", s.handleCandidate)
	mux.HandleFunc("GET /api/documents/{id}/object/{cid}/bytes", s.handleObjectBytes)
	mux.HandleFunc("GET /api/documents/{id}/export.zip", s.handleExport)
	return mux
}

func writeJSON(w http.ResponseWriter, v any, code int) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, map[string]string{"error": msg}, code)
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	b, err := webFS.ReadFile("web/index.html")
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(b)
}
