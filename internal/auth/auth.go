package auth

import (
	"crypto/subtle"
	"net/http"
	"os"
	"strings"
)

type Config struct {
	adminToken string
}

const EnvAdminToken = "DRAW_ADMIN_TOKEN"

func LoadConfig() Config {
	return Config{adminToken: strings.TrimSpace(os.Getenv(EnvAdminToken))}
}

func (c Config) AdminEnabled() bool {
	return c.adminToken != ""
}

func (c Config) IsAdmin(token string) bool {
	if !c.AdminEnabled() {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(token), []byte(c.adminToken)) == 1
}

type HandlerFunc = func(http.ResponseWriter, *http.Request)

func (c Config) RequireAdmin(next HandlerFunc) HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := bearerToken(r)
		if token == "" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if !c.IsAdmin(token) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		next(w, r)
	}
}

func bearerToken(r *http.Request) string {
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(auth, "Bearer "))
}

func Redact(token string) string {
	if len(token) <= 4 {
		return strings.Repeat("*", len(token))
	}
	return token[:2] + "***" + token[len(token)-2:]
}

type SessionAuth interface {
	Authorized(user string) bool
}

type disabledSessionAuth struct{}

func DisabledSessionAuth() SessionAuth {
	return disabledSessionAuth{}
}

func (disabledSessionAuth) Authorized(user string) bool {
	return false
}
