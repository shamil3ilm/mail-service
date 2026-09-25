package api

import (
	"net/http"
	"net/http/pprof"

	"github.com/shamil3ilm/mail-service/internal/metrics"
)

// AdminRouter serves pprof + Prometheus metrics on a separate port.
// This port MUST NEVER be exposed publicly — bind to loopback or use a
// firewall/VPN. Split from the public router to make that boundary explicit.
func AdminRouter(reg *metrics.Registry) http.Handler {
	mux := http.NewServeMux()

	// net/http/pprof registers its handlers on DefaultServeMux via init()
	// which we bypass by wiring them directly onto our own mux.
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)

	if reg != nil {
		mux.HandleFunc("/metrics", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
			reg.Write(w)
		})
	}

	return mux
}
