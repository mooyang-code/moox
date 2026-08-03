package store

import (
	"testing"

	pb "github.com/mooyang-code/moox/modules/cloudnode/proto/cloudnodegen"
	"github.com/stretchr/testify/assert"
)

func TestPackageTypeAndStatusMappings(t *testing.T) {
	assert.Equal(t, "collector", packageTypeToDB(pb.PackageType_PACKAGE_TYPE_COLLECTOR))
	assert.Equal(t, "factor", packageTypeToDB(pb.PackageType_PACKAGE_TYPE_FACTOR))
	assert.Equal(t, "custom", packageTypeToDB(pb.PackageType_PACKAGE_TYPE_CUSTOM))
	assert.Equal(t, "", packageTypeToDB(pb.PackageType_PACKAGE_TYPE_UNSPECIFIED))

	assert.Equal(t, "pending", packageStatusToDB(pb.PackageStatus_PACKAGE_STATUS_PENDING))
	assert.Equal(t, "available", packageStatusToDB(pb.PackageStatus_PACKAGE_STATUS_AVAILABLE))
	assert.Equal(t, "failed", packageStatusToDB(pb.PackageStatus_PACKAGE_STATUS_FAILED))
	assert.Equal(t, "deleted", packageStatusToDB(pb.PackageStatus_PACKAGE_STATUS_DELETED))
	assert.Equal(t, "", packageStatusToDB(pb.PackageStatus_PACKAGE_STATUS_UNSPECIFIED))
}
