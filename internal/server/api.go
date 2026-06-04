package server

import (
	"encoding/json"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"
)

type apiStateResponse struct {
	ConfigEntries   []configEntry `json:"configEntries"`
	Settings        []map[string]any `json:"settings"`
	ConfiguredCount int           `json:"configuredCount"`
	ProviderCount   int           `json:"providerCount"`
	CurrentPeriod   string        `json:"currentPeriod"`
	AdminUnlocked   bool          `json:"adminUnlocked"`
	Editable        bool          `json:"editable"`
	PasswordIsDefault bool        `json:"passwordIsDefault"`
}

func (h *handlers) apiState(w http.ResponseWriter, r *http.Request) {
	pageData, err := h.makePageData("", "")
	if err != nil {
		httpError(w, http.StatusInternalServerError, "failed building webpage data: "+err.Error())
		return
	}
	config, err := readRawConfig(h.configPath)
	if err != nil {
		httpError(w, http.StatusInternalServerError, "failed reading config: "+err.Error())
		return
	}
	pageData.AdminUnlocked = h.isAdminUnlocked(r)
	w.Header().Set("Content-Type", "application/json")
	passwordIsDefault, _, err := h.adminPasswordMatches(defaultAdminPassword)
	if err != nil {
		passwordIsDefault = false
	}
	_ = json.NewEncoder(w).Encode(apiStateResponse{
		ConfigEntries:   pageData.ConfigEntries,
		Settings:        config.Settings,
		ConfiguredCount: pageData.ConfiguredCount,
		ProviderCount:   pageData.ProviderCount,
		CurrentPeriod:   pageData.CurrentPeriod,
		AdminUnlocked:   pageData.AdminUnlocked,
		Editable:        pageData.Editable,
		PasswordIsDefault: passwordIsDefault,
	})
}

func (h *handlers) adminUnlock(w http.ResponseWriter, r *http.Request) {
	if !h.configEditable {
		httpError(w, http.StatusBadRequest, "admin unlock is disabled because configuration editing is disabled")
		return
	}
	err := r.ParseForm()
	if err != nil {
		httpError(w, http.StatusBadRequest, "failed parsing form: "+err.Error())
		return
	}
	password := r.Form.Get("password")
	matches, migrated, err := h.adminPasswordMatches(password)
	if err != nil {
		httpError(w, http.StatusInternalServerError, "checking admin password: "+err.Error())
		return
	}
	if !matches {
		httpError(w, http.StatusUnauthorized, "invalid admin password")
		return
	}
	err = h.issueAdminSession(w)
	if err != nil {
		httpError(w, http.StatusInternalServerError, "issuing admin session: "+err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"ok":                true,
		"message":           "Admin mode unlocked",
		"passwordMigrated":  migrated,
		"passwordIsDefault": password == defaultAdminPassword,
	})
}

func (h *handlers) adminLock(w http.ResponseWriter, r *http.Request) {
	h.clearAdminSession(w, r)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"ok":      true,
		"message": "Admin mode locked",
	})
}

func (h *handlers) adminChangePassword(w http.ResponseWriter, r *http.Request) {
	if !h.requireAdmin(w, r) {
		return
	}

	err := r.ParseForm()
	if err != nil {
		httpError(w, http.StatusBadRequest, "failed parsing form: "+err.Error())
		return
	}

	newPassword := strings.TrimSpace(r.Form.Get("new_password"))
	confirmPassword := strings.TrimSpace(r.Form.Get("confirm_password"))
	if newPassword == "" {
		httpError(w, http.StatusBadRequest, "new password is required")
		return
	}
	if newPassword != confirmPassword {
		httpError(w, http.StatusBadRequest, "password confirmation does not match")
		return
	}

	err = h.setAdminPassword(newPassword)
	if err != nil {
		httpError(w, http.StatusBadRequest, err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"ok":      true,
		"message": "Admin password changed successfully",
	})
}

func (h *handlers) setUpdatePeriod(w http.ResponseWriter, r *http.Request) {
	if !h.requireAdmin(w, r) {
		return
	}
	err := r.ParseForm()
	if err != nil {
		httpError(w, http.StatusBadRequest, "failed parsing form: "+err.Error())
		return
	}
	period := strings.TrimSpace(r.Form.Get("period"))
	if period == "" {
		httpError(w, http.StatusBadRequest, "missing period")
		return
	}
	duration, err := time.ParseDuration(period)
	if err != nil || duration < time.Minute {
		httpError(w, http.StatusBadRequest, "period must be a valid duration of at least 1m")
		return
	}

	servicePath := "/etc/systemd/system/ddns-updater.service"
	contentBytes, err := os.ReadFile(servicePath)
	if err != nil {
		httpError(w, http.StatusInternalServerError, "reading service file: "+err.Error())
		return
	}
	content := string(contentBytes)
	re := regexp.MustCompile(`(?m)^Environment=PERIOD=.*$`)
	if re.MatchString(content) {
		content = re.ReplaceAllString(content, "Environment=PERIOD="+period)
	} else {
		content += "\nEnvironment=PERIOD=" + period + "\n"
	}
	err = os.WriteFile(servicePath, []byte(content), 0o644)
	if err != nil {
		httpError(w, http.StatusInternalServerError, "writing service file: "+err.Error())
		return
	}
	err = h.restartServiceSoon()
	if err != nil {
		httpError(w, http.StatusInternalServerError, "scheduling service restart: "+err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"ok":      true,
		"message": "Update interval saved. The service will restart now.",
	})
}
