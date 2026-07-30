package auth

import (
	"context"
	"net/http"
	"strings"
)

// RequireAuth is an HTTP middleware that validates the Authorization header.
// Requests without a valid Bearer token receive a 401 Unauthorized response.
func RequireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header := r.Header.Get("Authorization")
		if header == "" || !strings.HasPrefix(header, "Bearer ") {
			http.Error(w, "missing authorization header", http.StatusUnauthorized)
			return
		}

		token := strings.TrimPrefix(header, "Bearer ")
		user, err := ValidateToken(token)
		if err != nil {
			http.Error(w, err.Error(), http.StatusUnauthorized)
			return
		}

		// Store user in request context for downstream handlers
		ctx := r.Context()
		ctx = context.WithValue(ctx, userKey, user)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// contextKey is an unexported type to avoid collisions in context.WithValue.
type contextKey struct{}

var userKey = &contextKey{}

// UserFromContext retrieves the authenticated user from the request context.
func UserFromContext(r *http.Request) *User {
	user, _ := r.Context().Value(userKey).(*User)
	return user
}
