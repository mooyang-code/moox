package command

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"

	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	cloudtencent "github.com/mooyang-code/moox/packages/cloudprovider/tencent"
)

// fetchPlanFunctionPrefix 是采集计划云函数的名字前缀，和 Collector 的命名一致。
const fetchPlanFunctionPrefix = "moox-fetch-plan-"

// setupPlanFunction 是云上一个采集计划函数。
type setupPlanFunction struct {
	Region    string `json:"region"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
}

// setupFunctionCloud 是清理云函数要用的云接口，每个地域一个。
type setupFunctionCloud interface {
	ListSCFNamespaces(context.Context) ([]string, error)
	ListSCFFunctions(context.Context, string) ([]string, error)
	DeleteSCFFunction(context.Context, string, string) error
}

// isPlanNamespace 只认 MooX 的命名空间：moox-<空间>-plan，以及放满后的溢出空间 moox-<空间>-ns<N>。
func isPlanNamespace(name string) bool {
	if !strings.HasPrefix(name, "moox-") {
		return false
	}
	if strings.HasSuffix(name, "-plan") {
		return true
	}
	index := strings.LastIndex(name, "-ns")
	return index > 0 && name[index+3:] != "" && strings.Trim(name[index+3:], "0123456789") == ""
}

// defaultSetupRegionCloud 用 moox.toml 的凭据：Admin 里的云账号清除后就没了。
func defaultSetupRegionCloud(manifest setupconfig.Manifest, region string) (setupFunctionCloud, error) {
	return cloudtencent.NewNetworkClient(cloudtencent.ClientOptions{SecretID: manifest.TencentCloud.SecretID, SecretKey: manifest.TencentCloud.SecretKey, Region: region})
}

// listSetupPlanFunctions 遍历所有地域：关闭 [scf_plan] 时不知道函数在哪。读不了的地域要报出来，否则会漏删。
func listSetupPlanFunctions(ctx context.Context, regionCloud func(string) (setupFunctionCloud, error)) ([]setupPlanFunction, []string, error) {
	regions := cloudtencent.SCFRegions()
	found := make([][]setupPlanFunction, len(regions))
	failed := make([]string, len(regions))
	var workers sync.WaitGroup
	for i, region := range regions {
		workers.Add(1)
		go func() {
			defer workers.Done()
			cloud, err := regionCloud(region.Code)
			if err == nil {
				found[i], err = listRegionPlanFunctions(ctx, cloud, region.Code)
			}
			if err != nil {
				failed[i] = region.Code
			}
		}()
	}
	workers.Wait()
	functions := slices.Concat(found...)
	slices.SortFunc(functions, func(a, b setupPlanFunction) int {
		return strings.Compare(a.Region+"/"+a.Namespace+"/"+a.Name, b.Region+"/"+b.Namespace+"/"+b.Name)
	})
	return functions, slices.DeleteFunc(failed, func(region string) bool { return region == "" }), ctx.Err()
}

func listRegionPlanFunctions(ctx context.Context, cloud setupFunctionCloud, region string) ([]setupPlanFunction, error) {
	namespaces, err := cloud.ListSCFNamespaces(ctx)
	if err != nil {
		return nil, err
	}
	var functions []setupPlanFunction
	for _, namespace := range namespaces {
		if !isPlanNamespace(namespace) {
			continue
		}
		names, err := cloud.ListSCFFunctions(ctx, namespace)
		if err != nil {
			return nil, err
		}
		for _, name := range names {
			if strings.HasPrefix(name, fetchPlanFunctionPrefix) {
				functions = append(functions, setupPlanFunction{Region: region, Namespace: namespace, Name: name})
			}
		}
	}
	return functions, nil
}

// setupFunctionsDigest 绑定用户确认过的函数清单。
func setupFunctionsDigest(functions []setupPlanFunction) string {
	digest := sha256.New()
	for _, function := range functions {
		fmt.Fprintln(digest, function.Region, function.Namespace, function.Name)
	}
	return hex.EncodeToString(digest.Sum(nil))[:16]
}

// deleteSetupPlanFunctions 删除确认过的函数，失败的继续删完再一起报。
func deleteSetupPlanFunctions(ctx context.Context, regionCloud func(string) (setupFunctionCloud, error), functions []setupPlanFunction) error {
	var failures []error
	for _, function := range functions {
		cloud, err := regionCloud(function.Region)
		if err == nil {
			err = cloud.DeleteSCFFunction(ctx, function.Namespace, function.Name)
		}
		if err != nil {
			failures = append(failures, fmt.Errorf("%s/%s/%s: %w", function.Region, function.Namespace, function.Name, err))
		}
	}
	return errors.Join(failures...)
}
