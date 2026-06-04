package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/qdm12/ddns-updater/internal/constants"
	providersdocs "github.com/qdm12/ddns-updater/docs"
	jsonparams "github.com/qdm12/ddns-updater/internal/params"
	"github.com/qdm12/ddns-updater/internal/records"
)

type providerField struct {
	Name        string `json:"name"`
	Label       string `json:"label"`
	Type        string `json:"type"`
	Description string `json:"description"`
	Required    bool   `json:"required"`
	Secret      bool   `json:"secret"`
	Common      bool   `json:"common"`
}

type providerSchema struct {
	Name   string          `json:"name"`
	Title  string          `json:"title"`
	Doc    string          `json:"doc"`
	Fields []providerField `json:"fields"`
}

type rawConfig struct {
	Settings []map[string]any `json:"settings"`
}

type configEntry struct {
	Index     int
	Provider  string
	Domain    string
	Owner     string
	IPVersion string
	Summary   string
	Disabled  bool
	Status    string
	StatusTone string
	CurrentIP string
	PreviousIPs string
}

type recordsHTMLRow struct {
	Domain      string
	Owner       string
	Provider    string
	IPVersion   string
	Status      string
	CurrentIP   string
	PreviousIPs string
}

type pageData struct {
	Rows             []recordsHTMLRow
	ConfigEntries    []configEntry
	ConfigJSONB64    string
	SchemasJSONB64   string
	RootURL          string
	Message          string
	Error            string
	Editable         bool
	AdminUnlocked    bool
	PasswordIsDefault bool
	DisabledReason   string
	StatusTableEmpty bool
	ProviderCount    int
	ConfiguredCount  int
	CurrentPeriod    string
}

var (
	jsonBlockRegex = regexp.MustCompile("(?s)```json\\s*(.*?)```")
	titleRegex     = regexp.MustCompile(`(?m)^#\s+(.+)$`)
	quotedRegex    = regexp.MustCompile(`"([^"]+)"`)
	trailingComma  = regexp.MustCompile(`,(\s*[}\]])`)
	htmlTagRegex   = regexp.MustCompile(`<[^>]*>`)
)

func loadProviderSchemas() (schemas map[string]providerSchema, err error) {
	entries, err := fs.ReadDir(providersdocs.FS, ".")
	if err != nil {
		return nil, fmt.Errorf("reading embedded provider docs: %w", err)
	}

	schemas = make(map[string]providerSchema, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".md" {
			continue
		}
		schema, err := schemaFromDoc(entry.Name())
		if err != nil {
			return nil, fmt.Errorf("building schema from %s: %w", entry.Name(), err)
		}
		schemas[schema.Name] = schema
	}
	return schemas, nil
}

func schemaFromDoc(name string) (schema providerSchema, err error) {
	contentBytes, err := fs.ReadFile(providersdocs.FS, name)
	if err != nil {
		return schema, err
	}
	content := string(contentBytes)

	titleMatch := titleRegex.FindStringSubmatch(content)
	if len(titleMatch) >= 2 {
		schema.Title = strings.TrimSpace(titleMatch[1])
	} else {
		schema.Title = strings.TrimSuffix(name, ".md")
	}
	schema.Doc = name

	exampleMatch := jsonBlockRegex.FindStringSubmatch(content)
	if len(exampleMatch) < 2 {
		return schema, fmt.Errorf("json example block not found")
	}

	sanitizedExample := sanitizeJSONExample(exampleMatch[1])
	var example struct {
		Settings []map[string]any `json:"settings"`
	}
	err = json.Unmarshal([]byte(sanitizedExample), &example)
	if err != nil {
		return schema, fmt.Errorf("decoding json example: %w", err)
	}
	if len(example.Settings) == 0 {
		return schema, errors.New("settings example is empty")
	}

	firstSetting := example.Settings[0]
	providerName, ok := firstSetting["provider"].(string)
	if !ok || providerName == "" {
		return schema, errors.New("provider field not found in example")
	}
	schema.Name = providerName

	requiredDescriptions, optionalDescriptions := parseFieldDescriptions(content)

	orderedFieldNames := make([]string, 0, len(firstSetting)+len(requiredDescriptions)+len(optionalDescriptions))
	appendField := func(fieldName string) {
		if fieldName == "" || fieldName == "provider" {
			return
		}
		if !slices.Contains(orderedFieldNames, fieldName) {
			orderedFieldNames = append(orderedFieldNames, fieldName)
		}
	}

	for _, fieldName := range []string{"domain", "ip_version", "ipv6_suffix"} {
		appendField(fieldName)
	}
	for key := range firstSetting {
		appendField(key)
	}
	for _, key := range sortedDescriptionKeys(requiredDescriptions) {
		appendField(key)
	}
	for _, key := range sortedDescriptionKeys(optionalDescriptions) {
		appendField(key)
	}

	fields := make([]providerField, 0, len(orderedFieldNames))
	for _, fieldName := range orderedFieldNames {
		exampleValue, hasExample := firstSetting[fieldName]
		description, required := requiredDescriptions[fieldName]
		if !required {
			description = optionalDescriptions[fieldName]
		}
		fields = append(fields, providerField{
			Name:        fieldName,
			Label:       humanizeFieldName(fieldName),
			Type:        inferFieldType(fieldName, description, exampleValue, hasExample),
			Description: description,
			Required:    required,
			Secret:      isSecretField(fieldName),
			Common:      isCommonField(fieldName),
		})
	}
	schema.Fields = fields
	return schema, nil
}

func sanitizeJSONExample(example string) string {
	lines := strings.Split(example, "\n")
	filtered := make([]string, 0, len(lines))
	for _, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "//") {
			continue
		}
		filtered = append(filtered, line)
	}
	sanitized := strings.Join(filtered, "\n")
	return trailingComma.ReplaceAllString(sanitized, "$1")
}

func parseFieldDescriptions(content string) (required, optional map[string]string) {
	required = make(map[string]string)
	optional = make(map[string]string)
	target := ""
	inAlternativeGroup := false

	for _, line := range strings.Split(content, "\n") {
		indentation := len(line) - len(strings.TrimLeft(line, " \t"))
		trimmed := strings.TrimSpace(line)
		switch trimmed {
		case "### Compulsory parameters":
			target = "required"
			inAlternativeGroup = false
			continue
		case "### Optional parameters":
			target = "optional"
			inAlternativeGroup = false
			continue
		}

		if target == "required" && strings.HasPrefix(trimmed, "- One of the following") {
			inAlternativeGroup = true
			continue
		}

		if inAlternativeGroup && (indentation == 0 || !strings.HasPrefix(trimmed, "- ")) {
			inAlternativeGroup = false
		}

		if !strings.HasPrefix(trimmed, "- ") || target == "" {
			continue
		}

		matches := quotedRegex.FindAllStringSubmatch(trimmed, -1)
		if len(matches) == 0 {
			continue
		}

		description := strings.TrimSpace(strings.TrimPrefix(trimmed, "- "))
		for _, match := range matches {
			fieldName := strings.TrimSpace(match[1])
			switch target {
			case "required":
				if inAlternativeGroup && indentation > 0 {
					if _, exists := optional[fieldName]; !exists {
						optional[fieldName] = "One of the following: " + description
					}
					continue
				}
				if _, exists := required[fieldName]; !exists {
					required[fieldName] = description
				}
			case "optional":
				if _, exists := optional[fieldName]; !exists {
					optional[fieldName] = description
				}
			}
		}
	}
	return required, optional
}

func sortedDescriptionKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}

func inferFieldType(fieldName, description string, exampleValue any, hasExample bool) string {
	if isCommonField(fieldName) {
		return "text"
	}

	switch exampleValue.(type) {
	case bool:
		return "checkbox"
	case float64:
		return "number"
	case map[string]any, []any:
		return "json"
	}

	lowerDescription := strings.ToLower(description)
	switch {
	case fieldName == "credentials":
		return "json"
	case strings.Contains(lowerDescription, "integer"):
		return "number"
	case strings.Contains(lowerDescription, "set to `true`"),
		strings.Contains(lowerDescription, "set to true"),
		strings.Contains(lowerDescription, "can be set to true"):
		return "checkbox"
	case hasExample:
		return "text"
	default:
		return "text"
	}
}

func isSecretField(fieldName string) bool {
	lower := strings.ToLower(fieldName)
	for _, token := range []string{"password", "token", "secret", "key", "credentials"} {
		if strings.Contains(lower, token) {
			return true
		}
	}
	return false
}

func isCommonField(fieldName string) bool {
	switch fieldName {
	case "domain", "ip_version", "ipv6_suffix":
		return true
	default:
		return false
	}
}

func humanizeFieldName(fieldName string) string {
	parts := strings.FieldsFunc(fieldName, func(r rune) bool {
		return r == '_' || r == '.'
	})
	for i, part := range parts {
		if part == "" {
			continue
		}
		parts[i] = strings.ToUpper(part[:1]) + strings.ToLower(part[1:])
	}
	return strings.Join(parts, " ")
}

func readRawConfig(path string) (config rawConfig, err error) {
	bytes, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return rawConfig{Settings: []map[string]any{}}, nil
		}
		return config, fmt.Errorf("reading config file: %w", err)
	}
	if len(bytes) == 0 {
		return rawConfig{Settings: []map[string]any{}}, nil
	}

	err = json.Unmarshal(bytes, &config)
	if err != nil {
		return config, fmt.Errorf("decoding config file: %w", err)
	}
	if config.Settings == nil {
		config.Settings = []map[string]any{}
	}
	return config, nil
}

func marshalRawConfig(config rawConfig) (jsonBytes []byte, err error) {
	buffer := bytes.NewBuffer(nil)
	encoder := json.NewEncoder(buffer)
	encoder.SetIndent("", "  ")
	err = encoder.Encode(config)
	if err != nil {
		return nil, fmt.Errorf("encoding config: %w", err)
	}
	return buffer.Bytes(), nil
}

func writeRawConfig(path string, jsonBytes []byte) (err error) {
	err = os.MkdirAll(filepath.Dir(path), 0o777)
	if err != nil {
		return fmt.Errorf("creating config directory: %w", err)
	}
	tempPath := path + ".tmp"
	err = os.WriteFile(tempPath, jsonBytes, 0o666)
	if err != nil {
		return fmt.Errorf("writing temporary config: %w", err)
	}
	err = os.Rename(tempPath, path)
	if err != nil {
		return fmt.Errorf("renaming temporary config: %w", err)
	}
	return nil
}

func maskSecret(value string) string {
	if value == "" {
		return ""
	}
	if len(value) <= 6 {
		return strings.Repeat("*", len(value))
	}
	return value[:3] + strings.Repeat("*", len(value)-6) + value[len(value)-3:]
}

func sortedSettingKeys(entry map[string]any) []string {
	keys := make([]string, 0, len(entry))
	for key := range entry {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}

func summarizeEntry(entry map[string]any) string {
	providerName, _ := entry["provider"].(string)
	if providerName == "cloudflare" {
		return summarizeCloudflareEntry(entry)
	}

	parts := make([]string, 0, len(entry))
	for _, key := range sortedSettingKeys(entry) {
		if key == "provider" || key == "domain" || key == "ip_version" || key == "ipv6_suffix" || key == "disabled" {
			continue
		}
		value := entry[key]
		switch typed := value.(type) {
		case bool:
			if typed {
				parts = append(parts, fmt.Sprintf("%s enabled", humanizeFieldName(key)))
			}
		case float64:
			parts = append(parts, fmt.Sprintf("%s %s", humanizeFieldName(key),
				strconv.FormatFloat(typed, 'f', -1, 64)))
		case string:
			if isSecretField(key) {
				parts = append(parts, fmt.Sprintf("%s set", humanizeFieldName(key)))
				continue
			}

			display := typed
			if len(display) > 42 {
				display = display[:39] + "..."
			}
			parts = append(parts, fmt.Sprintf("%s %s", humanizeFieldName(key), display))
		default:
			if typed == nil {
				continue
			}
			jsonBytes, err := json.Marshal(typed)
			if err != nil {
				parts = append(parts, fmt.Sprintf("%s configured", humanizeFieldName(key)))
				continue
			}
			parts = append(parts, fmt.Sprintf("%s %s", humanizeFieldName(key), string(jsonBytes)))
		}
	}
	if len(parts) == 0 {
		return "No provider-specific fields"
	}
	return strings.Join(parts, " | ")
}

func summarizeCloudflareEntry(entry map[string]any) string {
	parts := make([]string, 0, 4)

	switch {
	case stringFromAny(entry["token"]) != "":
		parts = append(parts, "API token set")
	case stringFromAny(entry["user_service_key"]) != "":
		parts = append(parts, "User service key set")
	case stringFromAny(entry["email"]) != "" || stringFromAny(entry["key"]) != "":
		parts = append(parts, "Email + global API key set")
	}

	if stringFromAny(entry["zone_identifier"]) != "" {
		parts = append(parts, "Zone ID set")
	}

	if ttl := numberStringFromAny(entry["ttl"]); ttl != "" {
		parts = append(parts, "TTL "+ttl)
	}

	if enabled, ok := entry["proxied"].(bool); ok && enabled {
		parts = append(parts, "Proxy enabled")
	}

	if len(parts) == 0 {
		return "Cloudflare settings pending"
	}
	return strings.Join(parts, " | ")
}

func stringFromAny(value any) string {
	s, _ := value.(string)
	return s
}

func numberStringFromAny(value any) string {
	number, ok := value.(float64)
	if !ok {
		return ""
	}
	return strconv.FormatFloat(number, 'f', -1, 64)
}

type liveEntryData struct {
	Owner       string
	Status      string
	StatusTone  string
	CurrentIP   string
	PreviousIPs string
}

func entryKey(domain, ipVersion string) string {
	return strings.ToLower(strings.TrimSpace(domain)) + "|" + strings.ToLower(strings.TrimSpace(ipVersion))
}

func stripHTMLString(value string) string {
	withoutTags := htmlTagRegex.ReplaceAllString(value, "")
	return strings.TrimSpace(html.UnescapeString(withoutTags))
}

func statusTone(status string) string {
	switch status {
	case string(constants.SUCCESS):
		return "success"
	case string(constants.FAIL):
		return "error"
	case string(constants.UPTODATE):
		return "uptodate"
	case string(constants.UPDATING):
		return "updating"
	default:
		return "unset"
	}
}

func buildLiveEntryMap(records []records.Record, now time.Time) map[string]liveEntryData {
	liveMap := make(map[string]liveEntryData, len(records))
	for _, record := range records {
		row := record.HTML(now)
		key := entryKey(record.Provider.BuildDomainName(), record.Provider.IPVersion().String())
		liveMap[key] = liveEntryData{
			Owner:       row.Owner,
			Status:      stripHTMLString(row.Status),
			StatusTone:  statusTone(string(record.Status)),
			CurrentIP:   stripHTMLString(row.CurrentIP),
			PreviousIPs: stripHTMLString(row.PreviousIPs),
		}
	}
	return liveMap
}

func buildConfigEntries(settings []map[string]any, liveMap map[string]liveEntryData) []configEntry {
	entries := make([]configEntry, 0, len(settings))
	for index, setting := range settings {
		providerName, _ := setting["provider"].(string)
		domain, _ := setting["domain"].(string)
		ipVersion, _ := setting["ip_version"].(string)
		disabled, _ := setting["disabled"].(bool)
		if ipVersion == "" {
			ipVersion = "ipv4 or ipv6"
		}
		live := liveMap[entryKey(domain, ipVersion)]
		statusText := live.Status
		statusTone := live.StatusTone
		currentIP := live.CurrentIP
		previousIPs := live.PreviousIPs
		if disabled {
			statusText = "Inactive"
			statusTone = "unset"
			currentIP = "N/A"
			previousIPs = "N/A"
		} else {
			if statusText == "" {
				statusText = "No live status yet"
			}
			if statusTone == "" {
				statusTone = "unset"
			}
			if currentIP == "" {
				currentIP = "N/A"
			}
			if previousIPs == "" {
				previousIPs = "N/A"
			}
		}
		entries = append(entries, configEntry{
			Index:       index,
			Provider:    html.EscapeString(providerName),
			Domain:      html.EscapeString(domain),
			Owner:       html.EscapeString(live.Owner),
			IPVersion:   html.EscapeString(ipVersion),
			Summary:     html.EscapeString(summarizeEntry(setting)),
			Disabled:    disabled,
			Status:      html.EscapeString(statusText),
			StatusTone:  html.EscapeString(statusTone),
			CurrentIP:   html.EscapeString(currentIP),
			PreviousIPs: html.EscapeString(previousIPs),
		})
	}
	return entries
}

func buildRows(records []records.Record, now time.Time) []recordsHTMLRow {
	rows := make([]recordsHTMLRow, 0, len(records))
	for _, record := range records {
		row := record.HTML(now)
		rows = append(rows, recordsHTMLRow{
			Domain:      row.Domain,
			Owner:       row.Owner,
			Provider:    row.Provider,
			IPVersion:   row.IPVersion,
			Status:      row.Status,
			CurrentIP:   row.CurrentIP,
			PreviousIPs: row.PreviousIPs,
		})
	}
	return rows
}

func (h *handlers) makePageData(message, errMessage string) (data pageData, err error) {
	config, err := readRawConfig(h.configPath)
	if err != nil {
		return data, err
	}

	configJSON, err := json.Marshal(config.Settings)
	if err != nil {
		return data, fmt.Errorf("encoding current settings: %w", err)
	}

	schemas := make([]providerSchema, 0, len(h.schemas))
	for _, schema := range h.schemas {
		schemas = append(schemas, schema)
	}
	slices.SortFunc(schemas, func(a, b providerSchema) int {
		return strings.Compare(a.Title, b.Title)
	})
	schemasJSON, err := json.Marshal(schemas)
	if err != nil {
		return data, fmt.Errorf("encoding provider schemas: %w", err)
	}

	rows := buildRows(h.db.SelectAll(), h.timeNow())
	liveMap := buildLiveEntryMap(h.db.SelectAll(), h.timeNow())
	passwordIsDefault, _, passwordErr := h.adminPasswordMatches(defaultAdminPassword)
	if passwordErr != nil {
		passwordIsDefault = false
	}
	return pageData{
		Rows:             rows,
		ConfigEntries:    buildConfigEntries(config.Settings, liveMap),
		ConfigJSONB64:    encodeJSONToBase64(string(configJSON)),
		SchemasJSONB64:   encodeJSONToBase64(string(schemasJSON)),
		RootURL:          h.rootURL,
		Message:          html.EscapeString(message),
		Error:            html.EscapeString(errMessage),
		Editable:         h.configEditable,
		AdminUnlocked:    false,
		PasswordIsDefault: passwordIsDefault,
		DisabledReason:   html.EscapeString(`The "CONFIG" environment variable is set and overrides config.json.`),
		StatusTableEmpty: len(rows) == 0,
		ProviderCount:    len(h.schemas),
		ConfiguredCount:  len(config.Settings),
		CurrentPeriod:    html.EscapeString(h.currentPeriod()),
	}, nil
}

func (h *handlers) validateAndApplyConfig(config rawConfig) (warnings []string, updateErrors []error, err error) {
	jsonBytes, err := marshalRawConfig(config)
	if err != nil {
		return nil, nil, err
	}

	providers, warnings, err := jsonparams.ExtractProvidersFromBytes(jsonBytes)
	if err != nil {
		return warnings, nil, err
	}

	err = writeRawConfig(h.configPath, jsonBytes)
	if err != nil {
		return warnings, nil, err
	}

	err = h.db.ReplaceProviders(providers)
	if err != nil {
		return warnings, nil, fmt.Errorf("reloading providers in memory: %w", err)
	}

	updateErrors = h.runner.ForceUpdate(h.ctx)
	return warnings, updateErrors, nil
}
