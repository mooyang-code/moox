package sysdeploy

import (
	"testing"

	adminpb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	"github.com/stretchr/testify/require"
)

func TestCheckFromDeploymentProbesRemoteLoopbackOnNodeHost(t *testing.T) {
	hosts := map[string]string{"storage": "146.56.196.204"}
	deployment := &adminpb.ServiceDeployment{
		ServiceName: "storage-primary", NodeId: "storage", Status: "active",
		ExtraConfig: `{"health_url":"http://127.0.0.1:20210/readyz","health_kind":"readiness"}`,
	}
	check, err := checkFromDeployment(deployment, hosts)
	require.NoError(t, err)
	require.Equal(t, "sysdeploy:storage:storage-primary", check.CheckID)
	require.Equal(t, domain.CheckKindHTTP, check.Kind)
	require.Equal(t, "http://146.56.196.204:20210/readyz", check.URL)

	_, err = checkFromDeployment(deployment, nil)
	require.ErrorContains(t, err, "host is unknown")

	control := &adminpb.ServiceDeployment{
		ServiceName: "moox_monitor", NodeId: "control", Status: "active",
		ExtraConfig: `{"health_url":"http://127.0.0.1:11409/readyz"}`,
	}
	check, err = checkFromDeployment(control, hosts)
	require.NoError(t, err)
	require.Equal(t, "http://127.0.0.1:11409/readyz", check.URL, "the control node probes its own loopback")

	tcp := &adminpb.ServiceDeployment{ServiceName: "svc", NodeId: "storage", Status: "active", Protocol: "http", Host: "localhost", Port: 9000}
	check, err = checkFromDeployment(tcp, hosts)
	require.NoError(t, err)
	require.Equal(t, domain.CheckKindTCP, check.Kind)
	require.Equal(t, "146.56.196.204", check.TCPHost)
}

func TestPublicHostAcceptsURLsAndBareHosts(t *testing.T) {
	require.Equal(t, "146.56.196.204", publicHost("https://146.56.196.204:11001"))
	require.Equal(t, "gw.example.com", publicHost("gw.example.com:443"))
	require.Equal(t, "10.0.0.5", publicHost("10.0.0.5"))
	require.Equal(t, "", publicHost(" "))
}
