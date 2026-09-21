package main

import (
	"flag"
	"log"
	"net/http"
	"os"
	"path/filepath"

	"pagevein/internal/app"
	"pagevein/internal/store"
	"pagevein/internal/web"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:5246", "listen address")
	dataDir := flag.String("data", "data", "data directory (SQLite + original bytes)")
	flag.Parse()

	if err := os.MkdirAll(*dataDir, 0o755); err != nil {
		log.Fatal(err)
	}
	st, err := store.Open(filepath.Join(*dataDir, "pagevein.db"))
	if err != nil {
		log.Fatal(err)
	}
	defer st.Close()

	a, err := app.New(st, *dataDir)
	if err != nil {
		log.Fatal(err)
	}
	srv := web.New(a)

	log.Printf("页脉探针 listening on http://%s (data dir %s)", *addr, *dataDir)
	if err := http.ListenAndServe(*addr, srv.Handler()); err != nil {
		log.Fatal(err)
	}
}
