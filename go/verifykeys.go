// Verify that SQLite holds the same keys as agent/settings.json.
// Read-only: reports any drift without printing key material.
package main

import (
	"database/sql"
	"fmt"
	"os"
	"sort"
	"strings"
)

func cmdVerifyKeys() {
	s, err := loadAgentSettings()
	if err != nil || s == nil {
		fmt.Fprintf(os.Stderr, "cannot read agent settings: %v\n", err)
		os.Exit(1)
	}
	ids := make([]string, 0, len(s.Providers))
	for id := range s.Providers {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	type row struct {
		id, dbKey, ref, base, defModel string
		hasKey                         bool
	}
	var rows []row
	_ = readDB(func(db *sql.DB) error {
		for _, id := range ids {
			var r row
			r.id = id
			_ = db.QueryRow(`SELECT base_url, COALESCE(api_key_ref,''), COALESCE(default_model,''),
					(SELECT value FROM secrets WHERE key = providers.api_key_ref)
				FROM providers WHERE id = ?`, id).
				Scan(&r.base, &r.ref, &r.defModel, &r.dbKey)
			r.hasKey = r.dbKey != ""
			rows = append(rows, r)
		}
		return nil
	})

	fmt.Printf("%-12s %-8s %-8s %-6s %s\n", "PROVIDER", "URL", "REF", "KEY", "NOTE")
	bad := 0
	for _, r := range rows {
		want := strings.TrimSpace(s.Providers[r.id].APIKey)
		note := ""
		if want != r.dbKey {
			note = "KEY MISMATCH"
			bad++
		}
		url := "ok"
		if r.base != s.Providers[r.id].BaseURL {
			url = "DIFF"
			bad++
		}
		if r.base == "" {
			url = "MISSING"
			bad++
		}
		k := "none"
		if r.hasKey {
			k = "set"
		}
		fmt.Printf("%-12s %-8s %-8s %-6s %s\n", r.id, url, boolShort(r.ref != ""), k, note)
	}
	if bad == 0 {
		fmt.Println("\nAll provider keys and endpoints match agent/settings.json.")
	} else {
		fmt.Printf("\n%d mismatch(es) found.\n", bad)
	}
}

func boolShort(b bool) string {
	if b {
		return "ok"
	}
	return "none"
}
