package sysdeploy

import (
	"context"

	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/mooyang-code/moox/packages/servicecatalog"
	"gorm.io/gorm"
)

// SnapshotSchemaVersion 是网关快照的格式版本：主机、部署与公钥，不含路由。
const SnapshotSchemaVersion = 2

// Snapshot 在一个 SQLite 快照里读登记的主机、部署与公钥，原样拼成网关快照；全部主机拿到的是同一份。
// 快照里没有秘密，也没有路由：各网关按内嵌的服务目录自己算。还没签发过身份包的主机没有公钥，它的请求在各网关上验不过。
func (d *TopologyDAO) Snapshot(ctx context.Context) (*pb.HostGatewaySnapshot, error) {
	var snapshot *pb.HostGatewaySnapshot
	err := d.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var snapErr error
		snapshot, snapErr = d.SnapshotTx(ctx, tx)
		return snapErr
	})
	if err != nil {
		return nil, err
	}
	return snapshot, nil
}

// SnapshotTx 在调用方已开启的事务中构造网关快照，复用长生命周期 TopologyDAO 与 keys.Store。
func (d *TopologyDAO) SnapshotTx(ctx context.Context, tx *gorm.DB) (*pb.HostGatewaySnapshot, error) {
	snapshot := &pb.HostGatewaySnapshot{SchemaVersion: SnapshotSchemaVersion}
	// hosts、deployments 已按主键排序，身份按名字排序：同一份数据拼出的快照逐字节相同。
	hosts, deployments, err := d.records(tx)
	if err != nil {
		return nil, err
	}
	for _, host := range hosts {
		// 登记的公钥读不出来时按没有公钥处理。
		public, _ := gatewayauth.ParsePublicKey(host.SignPublicKey)
		snapshot.Hosts = append(snapshot.Hosts, &pb.SnapshotHost{HostId: host.HostID, Address: host.Address, PrivateAddress: host.PrivateAddress,
			Reachability: servicecatalog.ReachabilityName(host.Reachability), Enabled: host.Status == servicecatalog.Enabled, ExternalEntry: host.ExternalEntry, SignPublicKey: public})
	}
	for _, deployment := range deployments {
		snapshot.Deployments = append(snapshot.Deployments, &pb.SnapshotDeployment{HostId: deployment.HostID, ComponentId: deployment.ComponentID, Enabled: deployment.Status == servicecatalog.Enabled})
	}
	identities, err := d.keyStore.IdentitiesTx(ctx, tx)
	if err != nil {
		return nil, err
	}
	for _, identity := range identities {
		public, _ := gatewayauth.ParsePublicKey(identity.PublicKey)
		snapshot.Identities = append(snapshot.Identities, &pb.SnapshotIdentity{Identity: identity.Identity, SignPublicKey: public})
	}
	snapshot.Hash, err = pb.SnapshotHash(snapshot)
	if err != nil {
		return nil, err
	}
	return snapshot, nil
}
