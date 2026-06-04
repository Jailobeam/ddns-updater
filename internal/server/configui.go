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

	providersdocs "github.com/qdm12/ddns-updater/docs"
	"github.com/qdm12/ddns-updater/internal/constants"
	jsonparams "github.com/qdm12/ddns-updater/internal/params"
	"github.com/qdm12/ddns-updater/internal/records"
)

type providerField struct {
	Name         string `json:"name"`
	Label        string `json:"label"`
	Type         string `json:"type"`
	Description  string `json:"description"`
	Required     bool   `json:"required"`
	Secret       bool   `json:"secret"`
	Common       bool   `json:"common"`
	ChoiceGroup  string `json:"choiceGroup,omitempty"`
	ChoiceOption string `json:"choiceOption,omitempty"`
	Placeholder  string `json:"placeholder,omitempty"`
}

type providerChoiceOption struct {
	Value       string   `json:"value"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Fields      []string `json:"fields"`
}

type providerChoiceGroup struct {
	Name    string                 `json:"name"`
	Label   string                 `json:"label"`
	Help    string                 `json:"help"`
	Options []providerChoiceOption `json:"options"`
}

type providerSchema struct {
	Name         string                `json:"name"`
	Title        string                `json:"title"`
	Doc          string                `json:"doc"`
	NoteTitle    string                `json:"noteTitle,omitempty"`
	NoteBody     string                `json:"noteBody,omitempty"`
	Fields       []providerField       `json:"fields"`
	ChoiceGroups []providerChoiceGroup `json:"choiceGroups,omitempty"`
}

type rawConfig struct {
	Settings []map[string]any `json:"settings"`
}

type configEntry struct {
	Index       int
	Provider    string
	Domain      string
	Owner       string
	IPVersion   string
	Summary     string
	Disabled    bool
	Status      string
	StatusTone  string
	CurrentIP   string
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
	Rows              []recordsHTMLRow
	ConfigEntries     []configEntry
	ConfigJSONB64     string
	SchemasJSONB64    string
	RootURL           string
	Message           string
	Error             string
	Editable          bool
	AdminUnlocked     bool
	PasswordIsDefault bool
	DisabledReason    string
	StatusTableEmpty  bool
	ProviderCount     int
	ConfiguredCount   int
	CurrentPeriod     string
}

var (
	jsonBlockRegex       = regexp.MustCompile("(?s)```json\\s*(.*?)```")
	titleRegex           = regexp.MustCompile(`(?m)^#\s+(.+)$`)
	codeQuotedFieldRegex = regexp.MustCompile("`\"([^\"]+)\"`")
	trailingComma        = regexp.MustCompile(`,(\s*[}\]])`)
	htmlTagRegex         = regexp.MustCompile(`<[^>]*>`)
	markdownLinkRegex    = regexp.MustCompile(`\[(.*?)\]\((.*?)\)`)
	multiSpaceRegex      = regexp.MustCompile(`\s+`)
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
	schema.NoteTitle, schema.NoteBody = parseProviderNote(schema.Title, content)

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
	schema.NoteTitle, schema.NoteBody = normalizeProviderNote(schema.Name, schema.Title, schema.NoteBody)

	requiredDescriptions, optionalDescriptions, choiceGroups, choiceFields := parseFieldDescriptions(content)

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
		if required && descriptionSuggestsOptional(description) {
			required = false
		}
		normalizedDescription := normalizeFieldDescription(fieldName, description)
		fields = append(fields, providerField{
			Name:         fieldName,
			Label:        humanizeFieldName(fieldName),
			Type:         inferFieldType(fieldName, description, exampleValue, hasExample),
			Description:  normalizedDescription,
			Required:     required,
			Secret:       isSecretField(fieldName),
			Common:       isCommonField(fieldName),
			ChoiceGroup:  choiceFields[fieldName].groupName,
			ChoiceOption: choiceFields[fieldName].optionValue,
			Placeholder:  inferFieldPlaceholder(fieldName, description),
		})
	}
	schema.Fields = fields
	schema.ChoiceGroups = normalizeChoiceGroups(choiceGroups)
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

type choiceFieldRef struct {
	groupName   string
	optionValue string
}

func parseFieldDescriptions(content string) (required, optional map[string]string, choiceGroups []providerChoiceGroup,
	choiceFields map[string]choiceFieldRef) {
	required = make(map[string]string)
	optional = make(map[string]string)
	choiceFields = make(map[string]choiceFieldRef)
	target := ""
	var activeGroup *providerChoiceGroup
	var headingGroup *providerChoiceGroup
	activeHeadingOption := ""
	groupCounter := 0

	for _, line := range strings.Split(content, "\n") {
		indentation := len(line) - len(strings.TrimLeft(line, " \t"))
		trimmed := strings.TrimSpace(line)
		switch trimmed {
		case "### Compulsory parameters":
			target = "required"
			activeGroup = nil
			headingGroup = nil
			activeHeadingOption = ""
			continue
		case "### Optional parameters":
			target = "optional"
			activeGroup = nil
			headingGroup = nil
			activeHeadingOption = ""
			continue
		}

		if target == "required" && strings.HasPrefix(trimmed, "- One of the following") {
			groupCounter++
			groupName := fmt.Sprintf("choice_group_%d", groupCounter)
			help := sanitizeGuideText(strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(trimmed, "- "), ":")))
			label := inferChoiceGroupLabel(help)
			choiceGroups = append(choiceGroups, providerChoiceGroup{
				Name:  groupName,
				Label: label,
				Help:  help,
			})
			activeGroup = &choiceGroups[len(choiceGroups)-1]
			headingGroup = nil
			activeHeadingOption = ""
			continue
		}

		if target == "required" && strings.HasPrefix(trimmed, "#### ") {
			activeGroup = nil
			if headingGroup == nil {
				groupCounter++
				groupName := fmt.Sprintf("choice_group_%d", groupCounter)
				choiceGroups = append(choiceGroups, providerChoiceGroup{
					Name:  groupName,
					Label: "Mode",
					Help:  "Choose the setup mode for this provider.",
				})
				headingGroup = &choiceGroups[len(choiceGroups)-1]
			}
			optionTitle := normalizeHeadingChoiceTitle(strings.TrimSpace(strings.TrimPrefix(trimmed, "#### ")))
			optionValue := slugChoiceValue(optionTitle)
			if !choiceGroupHasOption(*headingGroup, optionValue) {
				headingGroup.Options = append(headingGroup.Options, providerChoiceOption{
					Value:       optionValue,
					Title:       optionTitle,
					Description: optionTitle,
					Fields:      []string{},
				})
			}
			activeHeadingOption = optionValue
			continue
		}

		if activeGroup != nil && (indentation == 0 || !strings.HasPrefix(trimmed, "- ")) {
			activeGroup = nil
		}

		if !strings.HasPrefix(trimmed, "- ") || target == "" {
			continue
		}

		matches := codeQuotedFieldRegex.FindAllStringSubmatch(trimmed, -1)
		if len(matches) == 0 {
			continue
		}

		description := strings.TrimSpace(strings.TrimPrefix(trimmed, "- "))
		for _, match := range matches {
			fieldName := strings.TrimSpace(match[1])
			if !isLikelyFieldName(fieldName) {
				continue
			}
			switch target {
			case "required":
				if headingGroup != nil && activeHeadingOption != "" {
					if _, exists := optional[fieldName]; !exists {
						optional[fieldName] = description
					}
					if !descriptionSuggestsOptional(description) {
						for optionIndex := range headingGroup.Options {
							if headingGroup.Options[optionIndex].Value != activeHeadingOption {
								continue
							}
							if !slices.Contains(headingGroup.Options[optionIndex].Fields, fieldName) {
								headingGroup.Options[optionIndex].Fields = append(headingGroup.Options[optionIndex].Fields, fieldName)
							}
							break
						}
					}
					choiceFields[fieldName] = choiceFieldRef{
						groupName:   headingGroup.Name,
						optionValue: activeHeadingOption,
					}
					continue
				}

				if activeGroup != nil && indentation > 0 {
					if _, exists := optional[fieldName]; !exists {
						optional[fieldName] = "One of the following: " + description
					}
					optionFields := quotedFieldNames(description)
					if descriptionSuggestsOptional(description) {
						optionFields = nil
					}
					optionValue := buildChoiceOptionValue(optionFields)
					title := buildChoiceOptionTitle(description, optionFields)
					if !choiceGroupHasOption(*activeGroup, optionValue) {
						activeGroup.Options = append(activeGroup.Options, providerChoiceOption{
							Value:       optionValue,
							Title:       title,
							Description: sanitizeGuideText(description),
							Fields:      optionFields,
						})
					}
					choiceFields[fieldName] = choiceFieldRef{
						groupName:   activeGroup.Name,
						optionValue: optionValue,
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
	return required, optional, choiceGroups, choiceFields
}

func parseProviderNote(title, content string) (noteTitle, noteBody string) {
	parts := strings.Split(content, "## Configuration")
	if len(parts) == 0 {
		return "", ""
	}
	intro := strings.TrimSpace(parts[0])
	lines := strings.Split(intro, "\n")
	filtered := make([]string, 0, len(lines))
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			filtered = append(filtered, "")
			continue
		}
		if strings.HasPrefix(trimmed, "# ") {
			continue
		}
		filtered = append(filtered, sanitizeGuideText(trimmed))
	}
	noteBody = strings.TrimSpace(strings.Join(filtered, "\n"))
	noteBody = strings.ReplaceAll(noteBody, "\n\n\n", "\n\n")
	if noteBody == "" {
		return "", ""
	}
	return title, noteBody
}

func normalizeProviderNote(providerName, title, body string) (noteTitle, noteBody string) {
	if providerName == "" {
		return title, compactNoteBody(body)
	}

	if note, ok := map[string]string{
		"cloudflare":   "Use the zone ID from Cloudflare and choose one authentication method below.",
		"custom":       "Use your own update URL. The updater appends the detected IP automatically.",
		"digitalocean": "Use a personal access token with DNS access for the target domain.",
		"gcp":          "Use your Google Cloud project, zone, and full JSON service account credentials.",
		"hetzner":      "Legacy provider. Prefer Hetzner Cloud for new setups when possible.",
		"hetznercloud": "Uses the Hetzner Cloud DNS API, not the legacy Hetzner DNS API.",
		"ionos":        "Use your IONOS API key in <prefix>.<key> format for Dynamic DNS.",
		"porkbun":      "Requires an API key, a secret API key, and API access enabled for the domain.",
	}[providerName]; ok {
		return title, note
	}

	return title, compactNoteBody(body)
}

func compactNoteBody(body string) string {
	body = sanitizeGuideText(body)
	if body == "" {
		return ""
	}

	if strings.Contains(body, "\n\n") {
		body = strings.TrimSpace(strings.Split(body, "\n\n")[0])
	}

	if len(body) > 180 {
		if dot := strings.Index(body[:180], ". "); dot > 0 {
			body = body[:dot+1]
		} else {
			body = body[:177] + "..."
		}
	}
	return strings.TrimSpace(body)
}

func descriptionSuggestsOptional(description string) bool {
	lower := strings.ToLower(sanitizeGuideText(description))
	return strings.HasPrefix(lower, "optional ") ||
		strings.Contains(lower, " optional ") ||
		strings.Contains(lower, "defaults to") ||
		strings.Contains(lower, "if left empty")
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

func inferFieldPlaceholder(fieldName, description string) string {
	lowerDescription := strings.ToLower(description)
	switch fieldName {
	case "zone_identifier":
		return "Zone ID"
	case "url":
		return "https://example.com/update"
	case "ipv4key":
		return "ipv4"
	case "ipv6key":
		return "ipv6"
	case "ttl":
		if strings.Contains(lowerDescription, "automatic") {
			return "1 for automatic"
		}
	}

	if strings.Contains(lowerDescription, "json credentials") {
		return "{\n  \"type\": \"service_account\"\n}"
	}
	return ""
}

func normalizeFieldDescription(fieldName, description string) string {
	description = sanitizeGuideText(description)
	if description == "" {
		return ""
	}

	if short, ok := map[string]string{
		"domain":           "Full hostname to update, for example home.example.com.",
		"ip_version":       "Choose whether to update IPv4, IPv6, or whichever public IP is available.",
		"ipv6_suffix":      "Optional stable IPv6 suffix. Leave empty to use the detected IPv6 address directly.",
		"zone_identifier":  "The provider zone ID. This is usually not the domain name.",
		"ttl":              "DNS record time to live in seconds. Use 1 when the provider supports automatic TTL.",
		"token":            "API token with permission to update DNS records.",
		"email":            "Account email used together with the global API key.",
		"key":              "Global API key used together with the account email.",
		"user_service_key": "User service key for providers that support it.",
		"credentials":      "Paste the full JSON credentials for this provider or service account.",
		"project":          "Project identifier used by the provider account.",
		"zone":             "DNS zone name or zone identifier used by the provider.",
		"url":              "Update URL without the IP value. The updater will append the IP for you.",
		"ipv4key":          "Query parameter name that should receive the IPv4 address.",
		"ipv6key":          "Query parameter name that should receive the IPv6 address.",
		"success_regex":    "Regular expression that marks a successful provider response.",
		"proxied":          "Enable provider proxying for this record when supported.",
	}[fieldName]; ok {
		return short
	}

	description = codeQuotedFieldRegex.ReplaceAllString(description, "")
	description = strings.TrimSpace(strings.TrimPrefix(description, "One of the following:"))
	description = strings.ReplaceAll(description, " For example:", ". Example:")

	prefixes := []string{
		"is the ",
		"is your ",
		"is a ",
		"is an ",
		"can be ",
	}

	for _, prefix := range prefixes {
		marker := humanizeFieldName(fieldName) + " " + prefix
		if strings.HasPrefix(strings.ToLower(description), strings.ToLower(marker)) {
			description = strings.TrimSpace(description[len(marker):])
			break
		}
	}

	if len(description) > 180 {
		if dot := strings.Index(description[:180], ". "); dot > 0 {
			description = description[:dot+1]
		} else {
			description = description[:177] + "..."
		}
	}

	if description == "" {
		return ""
	}

	description = strings.TrimSpace(strings.Trim(description, "-:"))
	description = strings.ToUpper(description[:1]) + description[1:]
	if !strings.HasSuffix(description, ".") {
		description += "."
	}
	return description
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
	if label, ok := map[string]string{
		"zone_identifier":  "Zone ID",
		"user_service_key": "User service key",
		"ip_version":       "IP version",
		"ipv6_suffix":      "IPv6 suffix",
		"ipv4key":          "IPv4 query key",
		"ipv6key":          "IPv6 query key",
		"success_regex":    "Success regex",
	}[fieldName]; ok {
		return label
	}

	acronyms := map[string]string{
		"api":  "API",
		"dns":  "DNS",
		"gcp":  "GCP",
		"id":   "ID",
		"ip":   "IP",
		"ttl":  "TTL",
		"url":  "URL",
		"ipv4": "IPv4",
		"ipv6": "IPv6",
	}
	parts := strings.FieldsFunc(fieldName, func(r rune) bool {
		return r == '_' || r == '.'
	})
	for i, part := range parts {
		if part == "" {
			continue
		}
		lower := strings.ToLower(part)
		if acronym, ok := acronyms[lower]; ok {
			parts[i] = acronym
			continue
		}
		parts[i] = strings.ToUpper(part[:1]) + strings.ToLower(part[1:])
	}
	return strings.Join(parts, " ")
}

func sanitizeGuideText(text string) string {
	text = markdownLinkRegex.ReplaceAllString(text, "$1")
	text = strings.ReplaceAll(text, "`", "")
	text = multiSpaceRegex.ReplaceAllString(text, " ")
	return strings.TrimSpace(text)
}

func quotedFieldNames(text string) []string {
	matches := codeQuotedFieldRegex.FindAllStringSubmatch(text, -1)
	fields := make([]string, 0, len(matches))
	for _, match := range matches {
		if len(match) < 2 {
			continue
		}
		fieldName := strings.TrimSpace(match[1])
		if !isLikelyFieldName(fieldName) || slices.Contains(fields, fieldName) {
			continue
		}
		fields = append(fields, fieldName)
	}
	return fields
}

func buildChoiceOptionValue(fieldNames []string) string {
	if len(fieldNames) == 0 {
		return "option"
	}
	return strings.Join(fieldNames, "__")
}

func buildChoiceOptionTitle(description string, fieldNames []string) string {
	title := codeQuotedFieldRegex.ReplaceAllString(description, "")
	title = strings.ReplaceAll(title, " ,", ",")
	title = strings.ReplaceAll(title, "  ", " ")
	title = strings.TrimSpace(strings.Trim(title, "-:"))
	title = sanitizeGuideText(title)
	if title != "" {
		return title
	}
	labels := make([]string, 0, len(fieldNames))
	for _, fieldName := range fieldNames {
		labels = append(labels, humanizeFieldName(fieldName))
	}
	return strings.Join(labels, " + ")
}

func normalizeChoiceGroups(groups []providerChoiceGroup) []providerChoiceGroup {
	for i := range groups {
		authGroup := isAuthenticationChoiceGroup(groups[i].Label, groups[i].Help)
		if authGroup {
			groups[i].Label = "Authentication"
			if len(groups[i].Options) > 1 {
				groups[i].Help = "Choose exactly one authentication method."
			} else {
				groups[i].Help = "Authentication is required for this provider."
			}
		} else if groups[i].Help == "" {
			groups[i].Help = "Choose one option."
		}

		for j := range groups[i].Options {
			option := &groups[i].Options[j]
			option.Title = normalizeChoiceOptionTitle(option.Title, option.Fields, authGroup)
			option.Description = normalizeChoiceOptionDescription(option.Description, option.Fields, authGroup)
		}
	}
	return groups
}

func normalizeChoiceOptionTitle(title string, fieldNames []string, authGroup bool) string {
	if authGroup {
		switch {
		case slices.Equal(fieldNames, []string{"token"}):
			return "API token"
		case slices.Equal(fieldNames, []string{"email", "key"}):
			return "Email + global API key"
		case slices.Equal(fieldNames, []string{"user_service_key"}):
			return "User service key"
		}
	}

	if title != "" {
		return title
	}

	labels := make([]string, 0, len(fieldNames))
	for _, fieldName := range fieldNames {
		labels = append(labels, humanizeFieldName(fieldName))
	}
	return strings.Join(labels, " + ")
}

func normalizeChoiceOptionDescription(description string, fieldNames []string, authGroup bool) string {
	if authGroup {
		switch {
		case slices.Equal(fieldNames, []string{"token"}):
			return "Use a provider API token."
		case slices.Equal(fieldNames, []string{"email", "key"}):
			return "Use account email together with the global API key."
		case slices.Equal(fieldNames, []string{"user_service_key"}):
			return "Use the provider user service key."
		}
	}

	description = sanitizeGuideText(description)
	description = codeQuotedFieldRegex.ReplaceAllString(description, "")
	description = strings.ReplaceAll(description, " ,", ",")
	description = strings.TrimSpace(strings.Trim(description, "-:"))
	if description == "" {
		return "Choose this option for the matching fields."
	}
	return description
}

func normalizeHeadingChoiceTitle(title string) string {
	title = sanitizeGuideText(title)
	title = strings.TrimPrefix(title, "OR ")
	title = strings.TrimPrefix(title, "Or ")
	title = strings.TrimSpace(title)
	if strings.HasPrefix(strings.ToLower(title), "using ") {
		title = strings.TrimSpace(title[6:])
	}
	if title == "" {
		return "Alternative mode"
	}
	return title
}

func slugChoiceValue(title string) string {
	title = strings.ToLower(title)
	var b strings.Builder
	lastUnderscore := false
	for _, r := range title {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			lastUnderscore = false
			continue
		}
		if !lastUnderscore {
			b.WriteRune('_')
			lastUnderscore = true
		}
	}
	value := strings.Trim(b.String(), "_")
	if value == "" {
		return "option"
	}
	return value
}

func isLikelyFieldName(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '.' {
			continue
		}
		return false
	}
	return true
}

func inferChoiceGroupLabel(help string) string {
	lower := strings.ToLower(help)
	switch {
	case strings.Contains(lower, "api key"),
		strings.Contains(lower, "api keys"),
		strings.Contains(lower, "authentication"),
		strings.Contains(lower, "token"):
		return "Authentication"
	default:
		return "Choose one option"
	}
}

func isAuthenticationChoiceGroup(label, help string) bool {
	lower := strings.ToLower(label + " " + help)
	return strings.Contains(lower, "authentication") ||
		strings.Contains(lower, "api key") ||
		strings.Contains(lower, "api keys") ||
		strings.Contains(lower, "token")
}

func choiceGroupHasOption(group providerChoiceGroup, optionValue string) bool {
	for _, option := range group.Options {
		if option.Value == optionValue {
			return true
		}
	}
	return false
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
		Rows:              rows,
		ConfigEntries:     buildConfigEntries(config.Settings, liveMap),
		ConfigJSONB64:     encodeJSONToBase64(string(configJSON)),
		SchemasJSONB64:    encodeJSONToBase64(string(schemasJSON)),
		RootURL:           h.rootURL,
		Message:           html.EscapeString(message),
		Error:             html.EscapeString(errMessage),
		Editable:          h.configEditable,
		AdminUnlocked:     false,
		PasswordIsDefault: passwordIsDefault,
		DisabledReason:    html.EscapeString(`The "CONFIG" environment variable is set and overrides config.json.`),
		StatusTableEmpty:  len(rows) == 0,
		ProviderCount:     len(h.schemas),
		ConfiguredCount:   len(config.Settings),
		CurrentPeriod:     html.EscapeString(h.currentPeriod()),
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
