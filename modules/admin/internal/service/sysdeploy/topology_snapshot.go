package sysdeploy

import (
	"context"
	"slices"

	"github.com/mooyang-code/moox/modules/admin/internal/service/keys"
	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/mooyang-code/moox/packages/servicecatalog"
	"gorm.io/gorm"
)

// CompileSnapshot 在一个 SQLite 快照里读拓扑与校验公钥，编出一台主机的网关快照。快照里没有秘密：
// 每台登记了签名公钥的主机一把（连同部署在它上面、可以用它签名的调用方），加不属于主机的身份的公钥。
// 还没签发过身份包的主机没有公钥，它的请求在各网关上验不过。
func (d *TopologyDAO) CompileSnapshot(ctx context.Context, hostID string) (servicecatalog.CompiledHost, *pb.HostGatewaySnapshot, error) {
	var compiled servicecatalog.CompiledHost
	var snapshot *pb.HostGatewaySnapshot
	err := d.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		hosts, deployments, err := d.records(tx)
		if err != nil {
			return err
		}
		if !slices.ContainsFunc(hosts, func(host HostRecord) bool { return host.HostID == hostID }) {
			return gorm.ErrRecordNotFound
		}
		topology := d.topology(hosts, deployments)
		compiled, err = d.catalog.Compile(topology, hostID)
		if err != nil {
			return err
		}
		snapshot = &pb.HostGatewaySnapshot{SchemaVersion: 1, HostId: hostID, Disabled: compiled.Disabled, Directory: directoryToProto(compiled.Directory)}
		for _, route := range compiled.Routes {
			snapshot.Routes = append(snapshot.Routes, routeToProto(route))
		}
		// hosts 已按主机 ID 排序，身份按名字排序：同一份数据编出的快照逐字节相同。
		callers := d.catalog.SigningCallers(topology)
		for _, host := range hosts {
			public, err := gatewayauth.ParsePublicKey(host.SignPublicKey)
			if err != nil {
				continue
			}
			snapshot.VerificationKeys = append(snapshot.VerificationKeys, &pb.GatewayVerificationKey{HostId: host.HostID, PublicKey: public, Callers: callers[host.HostID]})
		}
		store, err := keys.NewStore(tx)
		if err != nil {
			return err
		}
		identities, err := store.Identities(ctx)
		if err != nil {
			return err
		}
		for _, identity := range identities {
			public, _ := gatewayauth.ParsePublicKey(identity.PublicKey)
			snapshot.VerificationKeys = append(snapshot.VerificationKeys, &pb.GatewayVerificationKey{Identity: identity.Identity, PublicKey: public})
		}
		snapshot.Relays = relaysToProto(compiled.Relays)
		snapshot.Hash, err = pb.SnapshotHash(snapshot)
		return err
	})
	if err != nil {
		return servicecatalog.CompiledHost{}, nil, err
	}
	return compiled, snapshot, nil
}
