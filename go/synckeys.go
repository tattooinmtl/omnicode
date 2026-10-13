// Key synchronisation: agent/settings.json -> SQLite.
//
// agent/settings.json stays the human-editable source of truth. This command
// copies each provider's endpoint and API key into the local SQLite database
// so the rest of the app can resolve credentials with plain SQL:
//
//	secrets.key        = "<PROVIDER>_API_KEY"
//	secrets.value      = the real key
//	providers.api_key_ref = "<PROVIDER>_API_KEY"  (pointer into secrets)
//
// Re-running is safe: keys are upserted, so editing settings.json and running
// this again refreshes the database.

package main

import (
	"database/sql"
	"fmt"
	"os"
	"sort"
	"strings"
)

func sortStrings(s []string) { sort.Strings(s) }

// secretKeyName is the secrets-table key for a provider id.
func secretKeyName(providerID string) string {
	// "minimax.io" -> "MINIMAX_IO_API_KEY"
	up := strings.ToUpper(providerID)
	up = strings.NewReplacer(".", "_", "-", "_", " ", "_", "/", "_").Replace(up)
	return up + "_API_KEY"
}

// syncProviderKeys imports every provider from agent/settings.json into SQLite.
// Returns the number of keys imported and the ids that had no key configured.
func syncProviderKeys() (imported int, missing []string, err error) {
	s, err := loadAgentSettings()
	if err != nil {
		return 0, nil, fmt.Errorf("read %s: %v", agentSettingsPath(), err)
	}
	if s == nil {
		return 0, nil, fmt.Errorf("no agent settings file at %s", agentSettingsPath())
	}
	if len(s.Providers) == 0 {
		return 0, nil, fmt.Errorf("agent settings contains no providers")
	}

	ids := make([]string, 0, len(s.Providers))
	for id := range s.Providers {
		ids = append(ids, id)
	}
	sortStrings(ids)

	for _, id := range ids {
		p := s.Providers[id]
		secKey := secretKeyName(id)

		// Store the key (if any) in the secrets table.
		if key := strings.TrimSpace(p.APIKey); key != "" {
			if err := upsertSecret(secKey, key); err != nil {
				return imported, missing, fmt.Errorf("store secret for %s: %v", id, err)
			}
		}

		// Upsert the provider row, pointing at the secret.
		label := firstNonEmpty(p.Label, id)
		ref := ""
		if strings.TrimSpace(p.APIKey) != "" {
			ref = secKey
		}
		defModel := ""
		if id == s.DefaultProvider {
			defModel = s.DefaultModel
		}
		if err := withDB(func(tx *sql.Tx) error {
			_, e := tx.Exec(`INSERT INTO providers
					(id, name, kind, base_url, api_key_ref, default_model, enabled, metadata)
				VALUES (?, ?, ?, ?, ?, ?, 1, ?)
				ON CONFLICT(id) DO UPDATE SET
					name=excluded.name, kind=excluded.kind, base_url=excluded.base_url,
					api_key_ref=excluded.api_key_ref, default_model=excluded.default_model,
					enabled=1, updated_at=CURRENT_TIMESTAMP`,
				id, label, "openai", p.BaseURL, ref, defModel, `{"source":"agent/settings.json"}`)
			return e
		}); err != nil {
			return imported, missing, fmt.Errorf("upsert provider %s: %v", id, err)
		}

		if ref == "" {
			missing = append(missing, id)
			continue
		}
		imported++
	}
	return imported, missing, nil
}

// cmdSyncKeys is the `omnicode sync-keys` entry point.
func cmdSyncKeys() {
	imported, missing, err := syncProviderKeys()
	if err != nil {
		fmt.Fprintf(os.Stderr, "sync-keys failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Imported %d provider key(s) into %s\n", imported, config.Database)
	if len(missing) > 0 {
		fmt.Printf("No key configured for: %s\n", strings.Join(missing, ", "))
	}
	fmt.Println("Run 'omnicode secret-list' to confirm.")
}
