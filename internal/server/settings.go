package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

func (h *handlers) saveSetting(w http.ResponseWriter, r *http.Request) {
	if !h.configEditable {
		httpError(w, http.StatusBadRequest, `configuration editing is disabled because "CONFIG" is set`)
		return
	}
	if !h.requireAdmin(w, r) {
		return
	}

	err := r.ParseForm()
	if err != nil {
		httpError(w, http.StatusBadRequest, "failed parsing form: "+err.Error())
		return
	}

	payload := r.Form.Get("payload")
	if payload == "" {
		httpError(w, http.StatusBadRequest, "missing payload")
		return
	}

	var setting map[string]any
	err = json.Unmarshal([]byte(payload), &setting)
	if err != nil {
		httpError(w, http.StatusBadRequest, "invalid payload json: "+err.Error())
		return
	}

	config, err := readRawConfig(h.configPath)
	if err != nil {
		httpError(w, http.StatusInternalServerError, err.Error())
		return
	}

	indexText := r.Form.Get("index")
	if indexText == "" {
		config.Settings = append(config.Settings, setting)
	} else {
		index, err := strconv.Atoi(indexText)
		if err != nil || index < 0 || index >= len(config.Settings) {
			httpError(w, http.StatusBadRequest, "invalid index")
			return
		}
		config.Settings[index] = setting
	}

	warnings, updateErrors, err := h.validateAndApplyConfig(config)
	if err != nil {
		httpError(w, http.StatusBadRequest, err.Error())
		return
	}

	message := "Configuration saved successfully."
	if len(warnings) > 0 {
		message += " Warnings: " + strings.Join(warnings, " | ")
	}
	errMessage := ""
	if len(updateErrors) > 0 {
		errMessage = "The configuration was saved, but the immediate update returned:\n" + joinErrors(updateErrors)
	}

	pageData, err := h.makePageData(message, errMessage)
	if err != nil {
		httpError(w, http.StatusInternalServerError, "failed building webpage data: "+err.Error())
		return
	}
	pageData.AdminUnlocked = h.isAdminUnlocked(r)

	err = h.indexTemplate.ExecuteTemplate(w, "index.html", pageData)
	if err != nil {
		httpError(w, http.StatusInternalServerError, "failed generating webpage: "+err.Error())
	}
}

func (h *handlers) deleteSetting(w http.ResponseWriter, r *http.Request) {
	if !h.configEditable {
		httpError(w, http.StatusBadRequest, `configuration editing is disabled because "CONFIG" is set`)
		return
	}
	if !h.requireAdmin(w, r) {
		return
	}

	err := r.ParseForm()
	if err != nil {
		httpError(w, http.StatusBadRequest, "failed parsing form: "+err.Error())
		return
	}

	index, err := strconv.Atoi(r.Form.Get("index"))
	if err != nil {
		httpError(w, http.StatusBadRequest, "invalid index")
		return
	}

	config, err := readRawConfig(h.configPath)
	if err != nil {
		httpError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if index < 0 || index >= len(config.Settings) {
		httpError(w, http.StatusBadRequest, "invalid index")
		return
	}

	config.Settings = append(config.Settings[:index], config.Settings[index+1:]...)

	warnings, updateErrors, err := h.validateAndApplyConfig(config)
	if err != nil {
		httpError(w, http.StatusBadRequest, err.Error())
		return
	}

	message := "Configuration entry deleted."
	if len(warnings) > 0 {
		message += " Warnings: " + strings.Join(warnings, " | ")
	}
	errMessage := ""
	if len(updateErrors) > 0 {
		errMessage = "The remaining configuration was saved, but the immediate update returned:\n" + joinErrors(updateErrors)
	}

	pageData, err := h.makePageData(message, errMessage)
	if err != nil {
		httpError(w, http.StatusInternalServerError, "failed building webpage data: "+err.Error())
		return
	}
	pageData.AdminUnlocked = h.isAdminUnlocked(r)

	err = h.indexTemplate.ExecuteTemplate(w, "index.html", pageData)
	if err != nil {
		httpError(w, http.StatusInternalServerError, "failed generating webpage: "+err.Error())
	}
}

func (h *handlers) toggleSetting(w http.ResponseWriter, r *http.Request) {
	if !h.configEditable {
		httpError(w, http.StatusBadRequest, `configuration editing is disabled because "CONFIG" is set`)
		return
	}
	if !h.requireAdmin(w, r) {
		return
	}

	err := r.ParseForm()
	if err != nil {
		httpError(w, http.StatusBadRequest, "failed parsing form: "+err.Error())
		return
	}

	index, err := strconv.Atoi(r.Form.Get("index"))
	if err != nil {
		httpError(w, http.StatusBadRequest, "invalid index")
		return
	}

	config, err := readRawConfig(h.configPath)
	if err != nil {
		httpError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if index < 0 || index >= len(config.Settings) {
		httpError(w, http.StatusBadRequest, "invalid index")
		return
	}

	setting := config.Settings[index]
	disabled, _ := setting["disabled"].(bool)
	if disabled {
		delete(setting, "disabled")
	} else {
		setting["disabled"] = true
	}
	config.Settings[index] = setting

	warnings, updateErrors, err := h.validateAndApplyConfig(config)
	if err != nil {
		httpError(w, http.StatusBadRequest, err.Error())
		return
	}

	message := "Configuration entry activated."
	if !disabled {
		message = "Configuration entry deactivated."
	}
	if len(warnings) > 0 {
		message += " Warnings: " + strings.Join(warnings, " | ")
	}
	errMessage := ""
	if len(updateErrors) > 0 {
		errMessage = "The configuration was saved, but the immediate update returned:\n" + joinErrors(updateErrors)
	}

	pageData, err := h.makePageData(message, errMessage)
	if err != nil {
		httpError(w, http.StatusInternalServerError, "failed building webpage data: "+err.Error())
		return
	}
	pageData.AdminUnlocked = h.isAdminUnlocked(r)

	err = h.indexTemplate.ExecuteTemplate(w, "index.html", pageData)
	if err != nil {
		httpError(w, http.StatusInternalServerError, "failed generating webpage: "+err.Error())
	}
}

func joinErrors(errs []error) string {
	if len(errs) == 0 {
		return ""
	}
	parts := make([]string, 0, len(errs))
	for _, err := range errs {
		parts = append(parts, err.Error())
	}
	return strings.Join(parts, "\n")
}

func (h *handlers) updatePageWithMessage(w http.ResponseWriter, message, errMessage string, status int) {
	pageData, err := h.makePageData(message, errMessage)
	if err != nil {
		httpError(w, http.StatusInternalServerError, "failed building webpage data: "+err.Error())
		return
	}
	w.WriteHeader(status)
	err = h.indexTemplate.ExecuteTemplate(w, "index.html", pageData)
	if err != nil {
		httpError(w, http.StatusInternalServerError, fmt.Sprintf("failed generating webpage: %v", err))
	}
}
