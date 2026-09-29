package middleware

import (
	"log"
	"net/http"
	"runtime/debug"
)

// RecoverMiddleware wraps an HTTP handler and recovers from any panics,
// preventing the entire server from crashing. It logs the stack trace and
// returns a 500 Internal Server Error to the client.
func RecoverMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if err := recover(); err != nil {
				// প্রিন্ট হবে টার্মিনালে যাতে ডেভেলপার বুঝতে পারে কোথায় ভুল হয়েছে
				log.Printf("🔥 PANIC RECOVERED: %v\n%s", err, debug.Stack())
				
				// ক্লায়েন্টকে ৫00 এরর পাঠানো হবে
				http.Error(w, `{"error": "Internal Server Error"}`, http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}
