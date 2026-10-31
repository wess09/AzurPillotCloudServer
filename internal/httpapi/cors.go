package httpapi

import "net/http"

func publicCORSMiddleware(origins []string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Only public statistics are browser HTTP APIs. Retired paths must stay
		// absent even for preflight requests; management authenticates over WS.
		switch r.URL.Path {
		case "/api/v3/get/analysis", "/api/v3/get/commission", "/api/v3/get/commission/options", "/api/v3/get/research", "/api/v3/get/research/options", "/api/v3/post/drops":
		default:
			next.ServeHTTP(w, r)
			return
		}
		origin := r.Header.Get("Origin")
		allowed := false
		for _, value := range origins {
			if origin != "" && origin == value {
				allowed = true
				break
			}
		}
		if allowed {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Add("Vary", "Origin")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-AzurPilot-ID")
			w.Header().Set("Access-Control-Max-Age", "600")
		}
		if r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != "" {
			if !allowed {
				http.Error(w, "Origin not allowed", http.StatusForbidden)
				return
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}
