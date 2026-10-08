package bootstrap

import (
	"github.com/mooyang-code/moox/modules/admin/internal/console"
	"github.com/mooyang-code/moox/modules/admin/internal/service/sysdeploy"
)

var _ console.GatewayControlProvider = (sysdeploy.Service)(nil)
