package hostinfo

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestParsePagingFiles(t *testing.T) {
	cfg := ParsePagingFiles([]string{
		`S:\pagefile.sys 4096 8192`,
		`C:\pagefile.sys 0 0`,
		`D:\pagefile.sys`,
		``,
	})
	assert.False(t, cfg.Automatic)
	assert.Equal(t, []PageFile{
		{Path: `S:\pagefile.sys`, InitialMB: 4096, MaximumMB: 8192},
		{Path: `C:\pagefile.sys`, SystemManaged: true},
		{Path: `D:\pagefile.sys`, SystemManaged: true},
	}, cfg.Files)
}

func TestParsePagingFilesAutomatic(t *testing.T) {
	cfg := ParsePagingFiles([]string{`?:\pagefile.sys`})
	assert.True(t, cfg.Automatic)
	assert.Empty(t, cfg.Files)
}

func TestParsePagingFilesNone(t *testing.T) {
	cfg := ParsePagingFiles(nil)
	assert.False(t, cfg.Automatic)
	assert.NotNil(t, cfg.Files)
	assert.Empty(t, cfg.Files)
}
