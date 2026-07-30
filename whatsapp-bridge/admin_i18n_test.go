package main

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"testing"
)

func loadAdminCatalog(t *testing.T, locale string) map[string]string {
	t.Helper()
	data, err := adminAssets.ReadFile("admin/i18n/" + locale + ".json")
	if err != nil {
		t.Fatalf("read %s admin catalog: %v", locale, err)
	}
	var catalog map[string]string
	if err := json.Unmarshal(data, &catalog); err != nil {
		t.Fatalf("decode %s admin catalog: %v", locale, err)
	}
	for key, value := range catalog {
		if strings.TrimSpace(key) == "" || strings.TrimSpace(value) == "" {
			t.Fatalf("%s admin catalog has empty key or value: %q=%q", locale, key, value)
		}
	}
	return catalog
}

func TestAdminI18nCatalogsHaveMatchingKeys(t *testing.T) {
	portuguese := loadAdminCatalog(t, "pt-BR")
	english := loadAdminCatalog(t, "en")

	var missing []string
	for key := range portuguese {
		if _, ok := english[key]; !ok {
			missing = append(missing, "en:"+key)
		}
	}
	for key := range english {
		if _, ok := portuguese[key]; !ok {
			missing = append(missing, "pt-BR:"+key)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Fatalf("admin translation catalogs have different keys: %s", strings.Join(missing, ", "))
	}
}

func TestAdminI18nReferencesExistInBothCatalogs(t *testing.T) {
	portuguese := loadAdminCatalog(t, "pt-BR")
	english := loadAdminCatalog(t, "en")

	html, err := adminAssets.ReadFile("admin/index.html")
	if err != nil {
		t.Fatalf("read admin HTML: %v", err)
	}
	app, err := adminAssets.ReadFile("admin/app.js")
	if err != nil {
		t.Fatalf("read admin JavaScript: %v", err)
	}

	references := make(map[string]struct{})
	staticPattern := regexp.MustCompile(`data-i18n(?:-[a-z-]+)?="([^"]+)"`)
	for _, match := range staticPattern.FindAllSubmatch(html, -1) {
		references[string(match[1])] = struct{}{}
	}
	dynamicPattern := regexp.MustCompile(`\bt\("([^"]+)"`)
	for _, match := range dynamicPattern.FindAllSubmatch(app, -1) {
		references[string(match[1])] = struct{}{}
	}
	pluralPattern := regexp.MustCompile(`\btp\("([^"]+)"`)
	for _, match := range pluralPattern.FindAllSubmatch(app, -1) {
		base := string(match[1])
		references[base+".one"] = struct{}{}
		references[base+".other"] = struct{}{}
	}

	var missing []string
	for key := range references {
		if _, ok := portuguese[key]; !ok {
			missing = append(missing, fmt.Sprintf("pt-BR:%s", key))
		}
		if _, ok := english[key]; !ok {
			missing = append(missing, fmt.Sprintf("en:%s", key))
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Fatalf("admin UI references missing translations: %s", strings.Join(missing, ", "))
	}
}
