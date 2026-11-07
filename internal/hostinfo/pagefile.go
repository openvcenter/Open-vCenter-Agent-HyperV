package hostinfo

import (
	"strconv"
	"strings"
)

// PageFileConfig is the configured page file layout (registry PagingFiles).
type PageFileConfig struct {
	// Automatic is "Automatically manage paging file size for all drives".
	Automatic bool       `json:"automatic"`
	Files     []PageFile `json:"files"`
}

// PageFile is one configured page file. SystemManaged means Windows picks the
// size (no explicit initial/maximum).
type PageFile struct {
	Path          string `json:"path"`
	InitialMB     int    `json:"initialMb"`
	MaximumMB     int    `json:"maximumMb"`
	SystemManaged bool   `json:"systemManaged"`
}

// ParsePagingFiles parses the REG_MULTI_SZ PagingFiles entries, e.g.
// "S:\pagefile.sys 4096 8192", "C:\pagefile.sys 0 0" (system managed) or
// "?:\pagefile.sys" (automatic for all drives).
func ParsePagingFiles(entries []string) PageFileConfig {
	cfg := PageFileConfig{Files: []PageFile{}}
	for _, entry := range entries {
		fields := strings.Fields(entry)
		if len(fields) == 0 {
			continue
		}
		if strings.HasPrefix(fields[0], "?:") {
			cfg.Automatic = true
			continue
		}
		pf := PageFile{Path: fields[0], SystemManaged: true}
		if len(fields) >= 3 {
			initial, err1 := strconv.Atoi(fields[1])
			maximum, err2 := strconv.Atoi(fields[2])
			if err1 == nil && err2 == nil && (initial > 0 || maximum > 0) {
				pf.InitialMB, pf.MaximumMB, pf.SystemManaged = initial, maximum, false
			}
		}
		cfg.Files = append(cfg.Files, pf)
	}
	return cfg
}
