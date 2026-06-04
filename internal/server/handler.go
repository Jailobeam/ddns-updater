package server

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/base64"
	"encoding/hex"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"text/template"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

type handlers struct {
	ctx context.Context //nolint:containedctx
	// Objects
	db             Database
	runner         UpdateForcer
	indexTemplate  *template.Template
	configPath     string
	configEditable bool
	schemas        map[string]providerSchema
	rootURL           string
	adminPasswordPath string
	adminSessionsMu   sync.Mutex
	adminSessions     map[string]time.Time
	// Mockable functions
	timeNow func() time.Time
}

//go:embed ui/*
var uiFS embed.FS

func newHandler(ctx context.Context, rootURL, configPath string,
	db Database, runner UpdateForcer,
) http.Handler {
	rootURL = strings.TrimSuffix(rootURL, "/")

	indexTemplate := template.Must(template.ParseFS(uiFS, "ui/index.html"))

	staticFolder, err := fs.Sub(uiFS, "ui/static")
	if err != nil {
		panic(err)
	}

	schemas, err := loadProviderSchemas()
	if err != nil {
		panic(err)
	}

	handlers := &handlers{
		ctx:               ctx,
		db:                db,
		indexTemplate:     indexTemplate,
		configPath:        configPath,
		configEditable:    os.Getenv("CONFIG") == "",
		schemas:           schemas,
		rootURL:           rootURL,
		adminPasswordPath: filepath.Join(filepath.Dir(configPath), "admin-password.txt"),
		adminSessions:     make(map[string]time.Time),
		// TODO build information
		timeNow: time.Now,
		runner:  runner,
	}
	if handlers.configEditable {
		err = handlers.ensureAdminPassword()
		if err != nil {
			panic(err)
		}
	}

	router := chi.NewRouter()

	router.Use(middleware.RealIP)
	router.Use(middleware.Logger)

	if rootURL != "" {
		router.Handle(rootURL, http.RedirectHandler(rootURL+"/", http.StatusPermanentRedirect))
	}
	router.Get(rootURL+"/", handlers.index)

	router.Get(rootURL+"/update", handlers.update)
	router.Get(rootURL+"/api/state", handlers.apiState)
	router.Post(rootURL+"/api/update", handlers.apiUpdate)
	router.Post(rootURL+"/api/admin/unlock", handlers.adminUnlock)
	router.Post(rootURL+"/api/admin/lock", handlers.adminLock)
	router.Post(rootURL+"/api/admin/password", handlers.adminChangePassword)
	router.Post(rootURL+"/api/settings/period", handlers.setUpdatePeriod)
	router.Post(rootURL+"/settings/save", handlers.saveSetting)
	router.Post(rootURL+"/settings/delete", handlers.deleteSetting)
	router.Post(rootURL+"/settings/toggle", handlers.toggleSetting)

	router.Handle(rootURL+"/static/*", http.StripPrefix(rootURL+"/static/", http.FileServerFS(staticFolder)))

	return router
}

func encodeJSONToBase64(s string) string {
	return base64.StdEncoding.EncodeToString([]byte(s))
}

func (h *handlers) ensureAdminPassword() error {
	_, err := os.Stat(h.adminPasswordPath)
	if err == nil {
		return nil
	}
	if !os.IsNotExist(err) {
		return err
	}
	return h.setAdminPassword("admin")
}

func (h *handlers) adminCookieName() string {
	return "ddns_admin_session"
}

func (h *handlers) currentPeriod() string {
	period := os.Getenv("PERIOD")
	if strings.TrimSpace(period) == "" {
		return "unknown"
	}
	return period
}

func (h *handlers) isAdminUnlocked(r *http.Request) bool {
	if !h.configEditable {
		return false
	}
	cookie, err := r.Cookie(h.adminCookieName())
	if err != nil || cookie.Value == "" {
		return false
	}
	h.adminSessionsMu.Lock()
	defer h.adminSessionsMu.Unlock()
	expiry, ok := h.adminSessions[cookie.Value]
	if !ok {
		return false
	}
	if h.timeNow().After(expiry) {
		delete(h.adminSessions, cookie.Value)
		return false
	}
	return true
}

func (h *handlers) requireAdmin(w http.ResponseWriter, r *http.Request) bool {
	if h.isAdminUnlocked(r) {
		return true
	}
	httpError(w, http.StatusUnauthorized, "admin unlock required")
	return false
}

func (h *handlers) issueAdminSession(w http.ResponseWriter) error {
	tokenBytes := make([]byte, 24)
	_, err := rand.Read(tokenBytes)
	if err != nil {
		return err
	}
	token := hex.EncodeToString(tokenBytes)
	h.adminSessionsMu.Lock()
	h.adminSessions[token] = h.timeNow().Add(12 * time.Hour)
	h.adminSessionsMu.Unlock()
	http.SetCookie(w, &http.Cookie{
		Name:     h.adminCookieName(),
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
	return nil
}

func (h *handlers) clearAdminSession(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(h.adminCookieName()); err == nil {
		h.adminSessionsMu.Lock()
		delete(h.adminSessions, cookie.Value)
		h.adminSessionsMu.Unlock()
	}
	http.SetCookie(w, &http.Cookie{
		Name:     h.adminCookieName(),
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}

func (h *handlers) restartServiceSoon() error {
	cmd := exec.Command("sh", "-c", "sleep 1; systemctl daemon-reload; systemctl restart ddns-updater >/dev/null 2>&1 &")
	return cmd.Start()
}
