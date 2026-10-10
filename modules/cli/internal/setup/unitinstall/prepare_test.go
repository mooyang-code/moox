package unitinstall

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/mooyang-code/moox/modules/cli/internal/setup/fsutil"
	"github.com/stretchr/testify/require"
)

func privateParent(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	require.NoError(t, os.Chmod(root, 0o700))
	return root
}

func TestPreparationRequestIsPrivateCanonicalAndDoesNotEchoInputs(t *testing.T) {
	root := privateParent(t)
	filename := filepath.Join(root, "request.json")
	options := PrepareOptions{Profile: "host", Environment: map[string]map[string]string{"host-agent": {"MOOX_TEST_SECRET": "private-marker-must-not-echo"}}}
	raw, err := json.Marshal(options)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filename, append(raw, '\n'), 0o600))
	loaded, err := ReadPrepareRequest(filename)
	require.NoError(t, err)
	require.Equal(t, options, loaded)
	require.NotContains(t, fmt.Sprint(loaded), "private-marker-must-not-echo")
	require.NotContains(t, fmt.Sprintf("%#v", loaded), "private-marker-must-not-echo")
	for _, raw := range []string{`{"environment":{"secret":"private-marker-must-not-echo"},"unknown":true}`, `{"profile":"private-marker-must-not-echo","profile":"host"}`, `{"profile":"host"} {"secret":"private-marker-must-not-echo"}`} {
		require.NoError(t, os.WriteFile(filename, []byte(raw), 0o600))
		_, err := ReadPrepareRequest(filename)
		require.Error(t, err)
		require.NotContains(t, err.Error(), "private-marker-must-not-echo")
	}
	require.NoError(t, os.Chmod(filename, 0o644))
	_, err = ReadPrepareRequest(filename)
	require.Error(t, err)
}

func TestConfigurationRenderingRejectsAmbiguousPrivateYAML(t *testing.T) {
	for _, raw := range []string{
		"secret: private-marker\nsecret: replaced\n",
		"outer:\n  secret: private-marker\n  secret: replaced\n",
		"secret: &value private-marker\ncopy: *value\n",
		"defaults: &value {secret: private-marker}\nconfig: {<<: *value}\n",
		"secret: private-marker\n---\nother: value\n",
		"[private-marker]\n",
		"1: private-marker\n",
		"secret: [private-marker\n",
	} {
		_, err := yamlDocument([]byte(raw))
		require.Error(t, err)
		require.NotContains(t, err.Error(), "private-marker")
	}
	node, err := yamlDocument([]byte("outer:\n  secret: private-marker\n  timeout: 30s\n"))
	require.NoError(t, err)
	require.Equal(t, "private-marker", member(member(node, "outer"), "secret").Value)
	require.Equal(t, "30s", member(member(node, "outer"), "timeout").Value)
}

func TestGeneratedLifecycleScriptTreatsHostPathsLiterally(t *testing.T) {
	parent := privateParent(t)
	host := filepath.Join(parent, "host 'quoted' $(touch PWNED) `touch PWNED2`")
	require.NoError(t, os.MkdirAll(filepath.Join(host, "current/bin"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(host, "current/bin/moox-runtime"), []byte("#!/bin/sh\nprintf '%s' \"$1\"\n"), 0o755))
	release := filepath.Join(parent, "business release")
	require.NoError(t, os.Mkdir(release, 0o700))
	root, err := fsutil.OpenPhysicalRoot(release, true)
	require.NoError(t, err)
	defer root.Close()
	require.NoError(t, writeScripts(root, PrepareOptions{Profile: "access", HostUnitRoot: host}))
	command := exec.CommandContext(t.Context(), filepath.Join(release, "status.sh"))
	command.Dir = parent
	output, err := command.CombinedOutput()
	require.NoError(t, err)
	require.Equal(t, "status", string(output))
	for _, name := range []string{"PWNED", "PWNED2"} {
		_, err := os.Lstat(filepath.Join(parent, name))
		require.True(t, os.IsNotExist(err))
	}
}
