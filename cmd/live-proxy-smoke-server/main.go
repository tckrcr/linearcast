// live-proxy-smoke-server is a deterministic fake upstream for live-proxy smoke tests.
package main

import (
	"flag"
	"log"
	"net/http"
)

func main() {
	addr := flag.String("addr", "0.0.0.0:18091", "listen address")
	flag.Parse()

	mux := http.NewServeMux()
	mux.HandleFunc("/external/master.m3u8", func(w http.ResponseWriter, r *http.Request) {
		playlist(w, "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=800000\nvariant/index.m3u8?variant=1\n")
	})
	mux.HandleFunc("/external/variant/index.m3u8", func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("variant"); got != "1" {
			http.Error(w, "missing variant query", http.StatusBadRequest)
			return
		}
		playlist(w, "#EXTM3U\n#EXT-X-TARGETDURATION:6\n#EXT-X-MAP:URI=\"init/init.mp4?map=1\"\n#EXTINF:6.000,\n../seg/00000.ts?seg=1\n")
	})
	mux.HandleFunc("/external/variant/init/init.mp4", func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("map"); got != "1" {
			http.Error(w, "missing map query", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "video/mp4")
		_, _ = w.Write([]byte("external-init"))
	})
	mux.HandleFunc("/external/seg/00000.ts", func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("seg"); got != "1" {
			http.Error(w, "missing segment query", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "video/mp2t")
		_, _ = w.Write([]byte("external-segment"))
	})

	log.Printf("live proxy smoke server listening on %s", *addr)
	log.Fatal(http.ListenAndServe(*addr, mux))
}

func playlist(w http.ResponseWriter, body string) {
	w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	_, _ = w.Write([]byte(body))
}
