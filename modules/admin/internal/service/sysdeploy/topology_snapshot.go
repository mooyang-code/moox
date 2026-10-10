package sysdeploy

import (
	"context"
	"slices"

	"github.com/mooyang-code/moox/modules/admin/internal/service/keys"
	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"github.com/mooyang-code/moox/packages/servicecatalog"
	"gorm.io/gorm"
)

// CompileSnapshot reads topology and verifier keys in one SQLite snapshot.
// Missing or corrupt master copies fail closed; reads never provision keys.
func (d *TopologyDAO) CompileSnapshot(ctx context.Context, hostID, encryptionKey string) (servicecatalog.CompiledHost, *pb.HostGatewaySnapshot, error) {
	var compiled servicecatalog.CompiledHost
	var snapshot *pb.HostGatewaySnapshot
	err := d.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		hosts, placements, err := d.records(tx)
		if err != nil {
			return err
		}
		if !slices.ContainsFunc(hosts, func(host HostRecord) bool { return host.HostID == hostID }) {
			return gorm.ErrRecordNotFound
		}
		compiled, err = d.catalog.Compile(d.topology(hosts, placements), hostID)
		if err != nil {
			return err
		}
		store, err := keys.NewStore(tx, encryptionKey)
		if err != nil {
			return err
		}
		verification, err := store.InternalVerification(ctx, compiled.VerificationCallers)
		if err != nil {
			return err
		}
		snapshot = &pb.HostGatewaySnapshot{SchemaVersion: 1, HostId: hostID, Disabled: compiled.Disabled, Directory: directoryToProto(compiled.Directory)}
		for _, route := range compiled.Routes {
			snapshot.Routes = append(snapshot.Routes, routeToProto(route))
		}
		for _, key := range verification {
			snapshot.VerificationKeys = append(snapshot.VerificationKeys, &pb.GatewayVerificationKey{Caller: key.Caller, KeyId: key.KeyID, Secret: slices.Clone(key.Secret)})
		}
		snapshot.Hash, err = pb.SnapshotHash(snapshot)
		return err
	})
	if err != nil {
		return servicecatalog.CompiledHost{}, nil, err
	}
	return compiled, snapshot, nil
}
