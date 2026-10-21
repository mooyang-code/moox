package main

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestVersionAndUnknownCommand(t *testing.T) {
	var out bytes.Buffer
	require.NoError(t, run(context.Background(), []string{"version"}, &out))
	require.Contains(t, out.String(), "moox-importer")
	require.ErrorContains(t, run(context.Background(), []string{"bogus"}, &out), "未知命令")
	require.Error(t, run(context.Background(), nil, &out))
}

func TestStepOf(t *testing.T) {
	d, err := stepOf("1h")
	require.NoError(t, err)
	require.Equal(t, time.Hour, d)
	_, err = stepOf("2m")
	require.Error(t, err)
}

// 校验模式：向引擎取字段与标的范围，只读文件、不写入，打印校验报告。
func TestDryRunAgainstFakeEngine(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/series/fields":
			_, _ = w.Write([]byte(`{"fields":[{"name":"close","type":"double"}]}`))
		case "/v1/series/subject":
			_, _ = w.Write([]byte(`{"in_scope":true}`))
		default:
			t.Errorf("dry-run 不应访问 %s", r.URL.Path)
		}
	}))
	defer srv.Close()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "BTC.csv"), []byte("data_time,close\n2026-10-01 00:00:00,1\n2026-10-01 00:01:00,2\n"), 0o600))
	t.Setenv("MOOX_HEALTH_AUTH_ACCESS_KEY", "ak")
	t.Setenv("MOOX_HEALTH_AUTH_SECRET_KEY", "secret-secret-secret")
	var out bytes.Buffer
	err := run(context.Background(), []string{"run", "--space", "crypto", "--dataset", "spot", "--freq", "1m", "--path", dir, "--dry-run",
		"--engine", srv.URL, "--job-db", filepath.Join(dir, "importer.db")}, &out)
	require.NoError(t, err)
	require.Contains(t, out.String(), `"status":"dry_run"`)
	require.Contains(t, out.String(), `"rows":2`)

	out.Reset()
	require.NoError(t, run(context.Background(), []string{"status", "--job-db", filepath.Join(dir, "importer.db")}, &out))
	require.Contains(t, out.String(), `"jobs"`)
}
