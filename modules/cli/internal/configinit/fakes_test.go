package configinit

import (
	"context"
	"errors"
	"sync"
)

// fakeCloud 按地址返回预置的实例；其余地址视为不在账号下。
type fakeCloud struct {
	mu        sync.Mutex
	instances map[string]Instance
	lookups   int
}

func newFakeCloud() *fakeCloud {
	return &fakeCloud{instances: map[string]Instance{
		"198.51.100.10": {Kind: "lighthouse", KindName: "轻量应用服务器", Region: "ap-guangzhou", RegionName: "广州", ZoneName: "广州三区", Name: "control", CPU: 4, MemoryGB: 4, DiskGB: 40},
		"198.51.100.11": {Kind: "lighthouse", KindName: "轻量应用服务器", Region: "ap-nanjing", RegionName: "南京", ZoneName: "南京一区", Name: "storage", CPU: 4, MemoryGB: 16, DiskGB: 100},
		"198.51.100.12": {Kind: "cvm", KindName: "云服务器", Region: "ap-hongkong", RegionName: "中国香港", ZoneName: "香港二区", Name: "hk"},
	}}
}

func (f *fakeCloud) Verify(_ context.Context, keys Keys) (Account, error) {
	if keys.SecretKey == "wrong" {
		return Account{}, errors.New("AuthFailure.SignatureFailure")
	}
	return Account{AccountID: "100012345678"}, nil
}

func (f *fakeCloud) Lookup(_ context.Context, _ Keys, address string) (Instance, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lookups++
	instance, ok := f.instances[address]
	if !ok {
		return Instance{}, errNotInAccount
	}
	return instance, nil
}

func (f *fakeCloud) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lookups
}

// fakeProber 返回预置的检测结果；hostKey 不为空且还没 Trust 时要求先确认指纹。
type fakeProber struct {
	mu      sync.Mutex
	report  ProbeReport
	err     error
	hostKey string
	trusted string
	targets []FactorServer
}

func linuxReport() ProbeReport {
	return ProbeReport{OK: true, OS: "linux", Items: []CheckItem{{Name: "系统", Status: CheckOK, Detail: "Ubuntu 26.04"}}}
}

func (p *fakeProber) Probe(_ context.Context, target FactorServer) (ProbeReport, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.targets = append(p.targets, target)
	if p.err != nil {
		return ProbeReport{}, p.err
	}
	if p.hostKey != "" && p.trusted != p.hostKey {
		return ProbeReport{HostKey: p.hostKey}, nil
	}
	return p.report, nil
}

func (p *fakeProber) Trust(_ context.Context, _ FactorServer, fingerprint string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if fingerprint != p.hostKey {
		return errors.New("fingerprint mismatch")
	}
	p.trusted = fingerprint
	return nil
}

func testEnv(cloud Cloud, prober Prober) Environment {
	cache := newCloudCache(cloud)
	return Environment{
		Lookup: cache.lookup,
		Probe: func(ctx context.Context, target FactorServer) (ProbeReport, error) {
			target, err := target.normalize()
			if err != nil {
				return ProbeReport{}, err
			}
			return prober.Probe(ctx, target)
		},
	}
}
