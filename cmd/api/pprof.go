package main

import (
	"log"
	"net"
	"net/http"
	"net/http/pprof"
	"os"
	"strings"
	"time"
)

// defaultPprofAddr keeps the profiler on loopback: it is reachable from a shell
// on the server (curl, go tool pprof) and from nowhere else.
const defaultPprofAddr = "127.0.0.1:6060"

// startPprof serves Go's CPU, heap and goroutine profiles on its own listener,
// never on the public router. PPROF_ADDR moves it; "off" disables it. Any
// address that isn't loopback is refused, so a typo can't publish it.
//
// On the server:
//
//	go tool pprof -top http://127.0.0.1:6060/debug/pprof/profile?seconds=30
//	curl -s http://127.0.0.1:6060/debug/pprof/goroutine?debug=1 | head -50
func startPprof() {
	addr := strings.TrimSpace(os.Getenv("PPROF_ADDR"))
	if addr == "" {
		addr = defaultPprofAddr
	}
	if strings.EqualFold(addr, "off") {
		return
	}
	host, _, err := net.SplitHostPort(addr)
	if ip := net.ParseIP(host); err != nil || ip == nil || !ip.IsLoopback() {
		log.Printf("pprof: refusing non-loopback PPROF_ADDR %q", addr)
		return
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	server := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("pprof: listener stopped: %v", err)
		}
	}()
}
