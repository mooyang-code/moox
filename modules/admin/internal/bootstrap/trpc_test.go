package bootstrap

import (
	"github.com/mooyang-code/moox/modules/admin/internal/gateway"
	"github.com/mooyang-code/moox/modules/admin/internal/service/sysdeploy"
)

var _ gateway.GatewayControlProvider = (sysdeploy.Service)(nil)
