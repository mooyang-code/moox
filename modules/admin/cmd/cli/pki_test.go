package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"

	"github.com/mooyang-code/moox/modules/admin/internal/pki"
	"github.com/stretchr/testify/require"
)

func TestPKICLIEnsureIssueAndPublicExport(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "private")
	require.NoError(t, os.Mkdir(parent, 0o700))
	dir := filepath.Join(parent, "pki")
	run := func(sub string, flags ...string) string {
		t.Helper()
		var out, stderr bytes.Buffer
		args := append([]string{"pki", sub, "--pki-dir", dir}, flags...)
		require.NoError(t, runPKICommand(args, &out, &stderr))
		require.NotContains(t, out.String()+stderr.String(), "BEGIN")
		return out.String()
	}
	first := run("ensure-ca")
	var firstCA, again pki.CAInfo
	require.NoError(t, json.Unmarshal([]byte(first), &firstCA))
	require.True(t, firstCA.Created)
	require.NoError(t, json.Unmarshal([]byte(run("ensure-ca")), &again))
	require.False(t, again.Created)
	require.Equal(t, firstCA.CertificateInfo, again.CertificateInfo)
	output := filepath.Join(parent, "stage")
	issuedJSON := run("issue", "--host-id", "storage", "--address", "192.0.2.2", "--private-address", "10.0.0.2", "--output-dir", output)
	var issued pki.IssuedCertificate
	require.NoError(t, json.Unmarshal([]byte(issuedJSON), &issued))
	require.Equal(t, firstCA.SHA256, issued.CAFingerprint)
	require.Equal(t, filepath.Join(output, "server.key"), issued.Key)
	export := filepath.Join(parent, "export")
	run("export-ca", "--output-dir", export)
	files, err := os.ReadDir(export)
	require.NoError(t, err)
	require.Len(t, files, 1)
	require.Equal(t, "moox-ca.crt", files[0].Name())
	require.True(t, isPKICommand([]string{"admin-cli", "pki"}))
	require.False(t, isPKICommand([]string{"admin-cli", "keys"}))
}

func TestPKICLIRejectsInvalidFlagsBeforeChangingFiles(t *testing.T) {
	parent := t.TempDir()
	dir := filepath.Join(parent, "absent")
	for _, args := range [][]string{
		{"pki"}, {"pki", "ensure-ca"}, {"pki", "unknown", "--pki-dir", dir},
		{"pki", "ensure-ca", "--pki-dir", dir, "unexpected"},
		{"pki", "ensure-ca", "--pki-dir", dir, "--host-id", "control"},
		{"pki", "issue", "--pki-dir", dir, "--host-id", "control"},
		{"pki", "export-ca", "--pki-dir", dir},
	} {
		require.Error(t, runPKICommand(args, nil, nil))
		_, err := os.Lstat(dir)
		require.True(t, os.IsNotExist(err), "invalid flags must not open the persistent PKI directory")
	}
	output := filepath.Join(parent, "not-exported")
	require.ErrorIs(t, runPKICommand([]string{"pki", "export-ca", "--pki-dir", dir, "--output-dir", output}, nil, nil), pki.ErrInvalidCA)
	_, err := os.Stat(output)
	require.True(t, os.IsNotExist(err), "export cannot initialize a missing root CA")
}

// Separate test processes exercise the operating-system lock, including
// deployment commands racing before any CA files exist.
func TestPKICLIConcurrentProcessesReuseRoot(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "pki")
	executable, err := os.Executable()
	require.NoError(t, err)
	const writers = 4
	commands := make([]*exec.Cmd, writers)
	outputs := make([]bytes.Buffer, writers)
	for i := range commands {
		commands[i] = exec.Command(executable, "-test.run=^TestPKIEnsureSubprocess$")
		commands[i].Env = append(os.Environ(), "MOOX_TEST_PKI_DIR="+dir)
		commands[i].Stdout = &outputs[i]
		commands[i].Stderr = &outputs[i]
	}
	var wait sync.WaitGroup
	errors := make([]error, writers)
	for i := range commands {
		wait.Go(func() { errors[i] = commands[i].Run() })
	}
	wait.Wait()
	created, fingerprint := 0, ""
	for i, err := range errors {
		require.NoError(t, err, outputs[i].String())
		var metadata pki.CAInfo
		require.NoError(t, json.Unmarshal(outputs[i].Bytes(), &metadata))
		if fingerprint == "" {
			fingerprint = metadata.SHA256
		}
		require.Equal(t, fingerprint, metadata.SHA256)
		if metadata.Created {
			created++
		}
	}
	require.Equal(t, 1, created)
}

func TestPKIEnsureSubprocess(t *testing.T) {
	dir := os.Getenv("MOOX_TEST_PKI_DIR")
	if dir == "" {
		t.Skip("only run by the concurrent CLI test")
	}
	if err := runPKICommand([]string{"pki", "ensure-ca", "--pki-dir", dir}, os.Stdout, os.Stderr); err != nil {
		printInitError(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
}
