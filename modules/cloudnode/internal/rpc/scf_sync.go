package rpc

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"

	tencentscf "github.com/mooyang-code/moox/modules/cloudnode/internal/providers/tencentscf"
	"github.com/mooyang-code/moox/modules/cloudnode/internal/spacecontext"
	"github.com/mooyang-code/moox/modules/cloudnode/internal/store"
	pb "github.com/mooyang-code/moox/modules/cloudnode/proto/cloudnodegen"
	"github.com/mooyang-code/moox/packages/cloudprovider/tencent"
)

type scfInventoryProvider interface {
	ListFunctionInventory(context.Context, string) ([]tencentscf.DiscoveryFunction, error)
}

type scfFunctionInspection struct {
	function tencentscf.DiscoveryFunction
	info     *tencentscf.FunctionInfo
	trigger  string
	node     *store.CloudNode
	state    pb.SCFFunctionImportState
	reason   string
}

func (s *Service) PreviewSCFFunctions(ctx context.Context, req *pb.PreviewSCFFunctionsReq) (*pb.PreviewSCFFunctionsRsp, error) {
	spaceID, err := spacecontext.MustFromContext(ctx)
	if err != nil {
		return &pb.PreviewSCFFunctionsRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, err.Error())}, nil
	}
	if req == nil || strings.TrimSpace(req.GetAccountId()) == "" {
		return &pb.PreviewSCFFunctionsRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "account_id is required")}, nil
	}
	account, err := s.catalog.GetAccount(ctx, req.GetAccountId())
	if err != nil {
		return &pb.PreviewSCFFunctionsRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, err.Error())}, nil
	}
	if account == nil {
		return &pb.PreviewSCFFunctionsRsp{RetInfo: retErr(pb.ErrorCode_NOT_FOUND, "cloud account not found")}, nil
	}
	if !isTencentProvider(account.Provider) {
		return &pb.PreviewSCFFunctionsRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "unsupported cloud provider")}, nil
	}
	if s.credentialResolver == nil || s.scfClientFactory == nil {
		return &pb.PreviewSCFFunctionsRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, "cloud provider integration is not configured")}, nil
	}
	credential, err := s.credentialResolver.Resolve(ctx, *account)
	if err != nil {
		return &pb.PreviewSCFFunctionsRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, "resolve cloud credential failed")}, nil
	}
	client := s.scfClientFactory(credential)
	discovery, ok := client.(scfInventoryProvider)
	if !ok {
		return &pb.PreviewSCFFunctionsRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, "scf discovery is not supported")}, nil
	}
	inspections, regionErrors := s.scanSCFFunctions(ctx, spaceID, account.AccountID, discovery, client, tencent.SCFRegions())
	return &pb.PreviewSCFFunctionsRsp{RetInfo: retOK(), Functions: inspectionsToPB(inspections), RegionErrors: regionErrors}, nil
}

func (s *Service) scanSCFFunctions(ctx context.Context, spaceID, accountID string, discovery scfInventoryProvider, client scfProvisioner, regions []tencent.SCFRegion) ([]scfFunctionInspection, []*pb.SCFRegionScanError) {
	byRegion, regionErrors := scanSCFInventory(ctx, discovery, regions)
	var functions []tencentscf.DiscoveryFunction
	for _, region := range regions {
		functions = append(functions, byRegion[region.Code]...)
	}
	inspections := make([]scfFunctionInspection, len(functions))
	detailSem := make(chan struct{}, 4)
	var detailWG sync.WaitGroup
	for index, function := range functions {
		index, function := index, function
		detailWG.Add(1)
		go func() {
			defer detailWG.Done()
			detailSem <- struct{}{}
			inspections[index] = s.inspectSCFFunction(ctx, spaceID, accountID, client, function)
			<-detailSem
		}()
	}
	detailWG.Wait()
	counts := make(map[string]int)
	for _, item := range inspections {
		counts[item.function.FunctionName]++
	}
	for index := range inspections {
		if counts[inspections[index].function.FunctionName] > 1 {
			inspections[index].state = pb.SCFFunctionImportState_SCF_FUNCTION_IMPORT_STATE_BLOCKED
			inspections[index].reason = "same function name exists in multiple regions or namespaces"
		}
	}
	sort.Slice(inspections, func(i, j int) bool {
		left, right := inspections[i].function, inspections[j].function
		if left.Region != right.Region {
			return left.Region < right.Region
		}
		if left.Namespace != right.Namespace {
			return left.Namespace < right.Namespace
		}
		return left.FunctionName < right.FunctionName
	})
	return inspections, regionErrors
}

func scanSCFInventory(ctx context.Context, discovery scfInventoryProvider, regions []tencent.SCFRegion) (map[string][]tencentscf.DiscoveryFunction, []*pb.SCFRegionScanError) {
	type scanResult struct {
		region string
		items  []tencentscf.DiscoveryFunction
		err    error
	}
	results := make(chan scanResult, len(regions))
	sem := make(chan struct{}, 2)
	var wg sync.WaitGroup
	for _, region := range regions {
		region := region
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			items, err := discovery.ListFunctionInventory(ctx, region.Code)
			<-sem
			results <- scanResult{region: region.Code, items: items, err: err}
		}()
	}
	wg.Wait()
	close(results)

	byRegion := make(map[string][]tencentscf.DiscoveryFunction, len(regions))
	regionErrors := make([]*pb.SCFRegionScanError, 0)
	for result := range results {
		if result.err != nil {
			regionErrors = append(regionErrors, &pb.SCFRegionScanError{Region: result.region, Message: result.err.Error()})
			continue
		}
		byRegion[result.region] = result.items
	}
	sort.Slice(regionErrors, func(i, j int) bool { return regionErrors[i].GetRegion() < regionErrors[j].GetRegion() })

	return byRegion, regionErrors
}

func (s *Service) inspectSCFFunction(ctx context.Context, spaceID, accountID string, client scfProvisioner, function tencentscf.DiscoveryFunction) scfFunctionInspection {
	inspection := scfFunctionInspection{function: function, state: pb.SCFFunctionImportState_SCF_FUNCTION_IMPORT_STATE_BLOCKED}
	node, err := s.catalog.GetNodeIncludingDeleted(ctx, spaceID, function.FunctionName)
	if err != nil {
		inspection.reason = "read existing node failed"
		return inspection
	}
	inspection.node = node
	if info, getErr := client.GetFunction(ctx, function.FunctionRef); getErr != nil {
		inspection.reason = "read function configuration failed"
		return inspection
	} else {
		inspection.info = info
	}
	if inspection.info == nil {
		inspection.reason = "read function configuration returned empty result"
		return inspection
	}
	inspection.trigger = "invoke"
	if trigger, triggerErr := client.GetTimerTrigger(ctx, function.FunctionRef, timerTriggerName); triggerErr != nil {
		inspection.reason = "read timer trigger failed"
		return inspection
	} else if trigger != nil {
		inspection.trigger = "timer"
	}
	if !strings.EqualFold(strings.TrimSpace(firstString(inspection.info.Status, function.Status)), "active") {
		inspection.reason = "function is not Active"
		return inspection
	}
	if !strings.EqualFold(strings.TrimSpace(inspection.info.Environment["MOOX_SPACE_ID"]), spaceID) {
		inspection.reason = "function belongs to another Space"
		return inspection
	}
	if strings.TrimSpace(inspection.info.Environment["MOOX_CODE_PACKAGE_ID"]) == "" {
		inspection.reason = "MOOX_CODE_PACKAGE_ID is missing"
		return inspection
	}
	if !isMarketFetcherFunction(function.FunctionName, spaceID, inspection.info.Environment) {
		inspection.reason = "function is not a MooX market fetcher"
		return inspection
	}
	functionType := firstString(inspection.info.Type, function.Type)
	if strings.EqualFold(functionType, "http") {
		inspection.reason = "HTTP 型函数不参与 Collector 调度"
		return inspection
	}
	if !strings.EqualFold(functionType, "event") {
		inspection.reason = "function type is not Event"
		return inspection
	}
	if node == nil {
		inspection.state = pb.SCFFunctionImportState_SCF_FUNCTION_IMPORT_STATE_NEW
	} else if node.CloudAccountID != accountID || node.Region != function.Region || node.Namespace != function.Namespace || node.FunctionName != function.FunctionName {
		inspection.reason = "existing node identity belongs to another cloud account or SCF location"
		return inspection
	} else if node.IsDeleted {
		inspection.state = pb.SCFFunctionImportState_SCF_FUNCTION_IMPORT_STATE_DELETED
	} else {
		inspection.state = pb.SCFFunctionImportState_SCF_FUNCTION_IMPORT_STATE_EXISTING
	}
	return inspection
}

func inspectionsToPB(items []scfFunctionInspection) []*pb.SCFFunctionCandidate {
	result := make([]*pb.SCFFunctionCandidate, 0, len(items))
	for _, item := range items {
		packageID := ""
		runtime := item.function.Runtime
		functionType := item.function.Type
		status := item.function.Status
		if item.info != nil {
			packageID = item.info.Environment["MOOX_CODE_PACKAGE_ID"]
			runtime = firstString(item.info.Runtime, runtime)
			functionType = firstString(item.info.Type, functionType)
			status = firstString(item.info.Status, status)
		}
		bizType := ""
		if item.state != pb.SCFFunctionImportState_SCF_FUNCTION_IMPORT_STATE_BLOCKED {
			bizType = "market_fetcher"
		}
		result = append(result, &pb.SCFFunctionCandidate{
			Function: &pb.SCFFunctionRef{Region: item.function.Region, Namespace: item.function.Namespace, FunctionName: item.function.FunctionName},
			Status:   status, Runtime: runtime, FunctionType: functionType, PackageId: packageID,
			NodeId: item.function.FunctionName, TriggerType: item.trigger, BizType: bizType,
			ImportState: item.state, Importable: item.state != pb.SCFFunctionImportState_SCF_FUNCTION_IMPORT_STATE_BLOCKED,
			Reason: item.reason,
		})
	}
	return result
}

func (s *Service) ImportSCFFunctions(ctx context.Context, req *pb.ImportSCFFunctionsReq) (*pb.ImportSCFFunctionsRsp, error) {
	spaceID, err := spacecontext.MustFromContext(ctx)
	if err != nil {
		return &pb.ImportSCFFunctionsRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, err.Error())}, nil
	}
	if req == nil || strings.TrimSpace(req.GetAccountId()) == "" {
		return &pb.ImportSCFFunctionsRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "account_id is required")}, nil
	}
	if len(req.GetFunctions()) == 0 {
		return &pb.ImportSCFFunctionsRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "functions is required")}, nil
	}
	account, err := s.catalog.GetAccount(ctx, req.GetAccountId())
	if err != nil || account == nil {
		return &pb.ImportSCFFunctionsRsp{RetInfo: retErr(pb.ErrorCode_NOT_FOUND, "cloud account not found")}, nil
	}
	if !isTencentProvider(account.Provider) {
		return &pb.ImportSCFFunctionsRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "unsupported cloud provider")}, nil
	}
	if s.credentialResolver == nil || s.scfClientFactory == nil {
		return &pb.ImportSCFFunctionsRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, "cloud provider integration is not configured")}, nil
	}
	credential, err := s.credentialResolver.Resolve(ctx, *account)
	if err != nil {
		return &pb.ImportSCFFunctionsRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, "resolve cloud credential failed")}, nil
	}
	client := s.scfClientFactory(credential)
	discovery, ok := client.(scfInventoryProvider)
	if !ok {
		return &pb.ImportSCFFunctionsRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, "scf discovery is not supported")}, nil
	}
	inventory, scanErrors := scanSCFInventory(ctx, discovery, tencent.SCFRegions())
	failedRegions := make(map[string]struct{}, len(scanErrors))
	for _, scanError := range scanErrors {
		failedRegions[scanError.GetRegion()] = struct{}{}
	}
	nameCounts := make(map[string]int)
	identity := make(map[string]struct{})
	for _, items := range inventory {
		for _, function := range items {
			nameCounts[function.FunctionName]++
			identity[scfRefKey(function.FunctionRef)] = struct{}{}
		}
	}
	seen := make(map[string]struct{}, len(req.GetFunctions()))
	response := &pb.ImportSCFFunctionsRsp{RetInfo: retOK()}
	for _, ref := range req.GetFunctions() {
		item := &pb.SCFFunctionImportResult{Function: ref}
		if ref == nil || strings.TrimSpace(ref.GetRegion()) == "" || strings.TrimSpace(ref.GetNamespace()) == "" || strings.TrimSpace(ref.GetFunctionName()) == "" {
			item.ErrorMessage = "region, namespace and function_name are required"
			response.Failed++
			response.Results = append(response.Results, item)
			continue
		}
		if !tencent.IsSCFRegion(ref.GetRegion()) {
			item.ErrorMessage = "unsupported SCF region"
			response.Failed++
			response.Results = append(response.Results, item)
			continue
		}
		if _, failed := failedRegions[ref.GetRegion()]; failed {
			item.ErrorMessage = "selected SCF region scan failed; scan again before importing"
			response.Failed++
			response.Results = append(response.Results, item)
			continue
		}
		if _, exists := seen[ref.GetFunctionName()]; exists {
			item.ErrorMessage = "duplicate function name would collide with another node"
			response.Failed++
			response.Results = append(response.Results, item)
			continue
		}
		if nameCounts[ref.GetFunctionName()] > 1 {
			item.ErrorMessage = "same function name exists in multiple regions or namespaces"
			response.Failed++
			response.Results = append(response.Results, item)
			continue
		}
		if _, exists := identity[scfRefKey(tencentscf.FunctionRef{Region: ref.GetRegion(), Namespace: ref.GetNamespace(), FunctionName: ref.GetFunctionName()})]; !exists {
			item.ErrorMessage = "function no longer exists in the cloud inventory"
			response.Failed++
			response.Results = append(response.Results, item)
			continue
		}
		seen[ref.GetFunctionName()] = struct{}{}
		result, action, err := s.importSCFFunction(ctx, spaceID, *account, client, ref)
		item.NodeId = ref.GetFunctionName()
		item.Action = action
		if err != nil {
			item.ErrorMessage = err.Error()
			response.Failed++
		} else {
			switch action {
			case "created":
				response.Created++
			case "restored":
				response.Restored++
			default:
				response.Unchanged++
			}
			_ = result
		}
		response.Results = append(response.Results, item)
	}
	return response, nil
}

func scfRefKey(ref tencentscf.FunctionRef) string {
	return ref.Region + "\x00" + ref.Namespace + "\x00" + ref.FunctionName
}

func (s *Service) importSCFFunction(ctx context.Context, spaceID string, account store.CloudAccount, client scfProvisioner, ref *pb.SCFFunctionRef) (store.CloudNode, string, error) {
	functionRef := tencentscf.FunctionRef{Region: ref.GetRegion(), Namespace: ref.GetNamespace(), FunctionName: ref.GetFunctionName()}
	unlockNode := lockSCFNode(spaceID, ref.GetFunctionName())
	defer unlockNode()
	unlockFunction := lockSCFFunction(functionRef)
	defer unlockFunction()
	info, err := client.GetFunction(ctx, functionRef)
	if err != nil {
		return store.CloudNode{}, "", fmt.Errorf("read function configuration failed")
	}
	if info == nil {
		return store.CloudNode{}, "", fmt.Errorf("read function configuration returned empty result")
	}
	if !strings.EqualFold(strings.TrimSpace(info.Status), "active") {
		return store.CloudNode{}, "", fmt.Errorf("function is not Active")
	}
	if strings.TrimSpace(info.Environment["MOOX_SPACE_ID"]) != spaceID {
		return store.CloudNode{}, "", fmt.Errorf("function belongs to another Space")
	}
	packageID := strings.TrimSpace(info.Environment["MOOX_CODE_PACKAGE_ID"])
	if packageID == "" {
		return store.CloudNode{}, "", fmt.Errorf("MOOX_CODE_PACKAGE_ID is missing")
	}
	if !isMarketFetcherFunction(ref.GetFunctionName(), spaceID, info.Environment) {
		return store.CloudNode{}, "", fmt.Errorf("function is not a MooX market fetcher")
	}
	functionType := strings.ToLower(strings.TrimSpace(info.Type))
	if functionType == "http" {
		return store.CloudNode{}, "", fmt.Errorf("HTTP 型函数不参与 Collector 调度")
	}
	if functionType != "event" {
		return store.CloudNode{}, "", fmt.Errorf("function type is not Event")
	}
	triggerType := "invoke"
	if trigger, triggerErr := client.GetTimerTrigger(ctx, functionRef, timerTriggerName); triggerErr != nil {
		return store.CloudNode{}, "", fmt.Errorf("read timer trigger failed")
	} else if trigger != nil {
		triggerType = "timer"
	}
	existing, err := s.catalog.GetNodeIncludingDeleted(ctx, spaceID, ref.GetFunctionName())
	if err != nil {
		return store.CloudNode{}, "", fmt.Errorf("read existing node failed")
	}
	if existing != nil && (existing.Region != ref.GetRegion() || existing.Namespace != ref.GetNamespace() || existing.FunctionName != ref.GetFunctionName() || existing.CloudAccountID != account.AccountID) {
		return store.CloudNode{}, "", fmt.Errorf("node identity collides with a different cloud account, SCF region or namespace")
	}
	metadataMap := map[string]any{}
	if existing != nil {
		metadataMap = parseJSONMap(existing.Metadata)
	}
	for key, value := range map[string]any{"biz_type": "market_fetcher", "function_type": functionType, "runtime": info.Runtime, "deployment_ready": true, "imported_from_scf": true} {
		metadataMap[key] = value
	}
	metadata, err := json.Marshal(metadataMap)
	if err != nil {
		return store.CloudNode{}, "", err
	}
	node := store.CloudNode{SpaceID: spaceID, NodeID: ref.GetFunctionName(), Provider: "tencent-scf", CloudAccountID: account.AccountID, PackageID: packageID, NodeType: "scf-event", TriggerType: triggerType, Region: ref.GetRegion(), Namespace: ref.GetNamespace(), FunctionName: ref.GetFunctionName(), Metadata: string(metadata), IsDeleted: false}
	if existing != nil {
		node.PackageVersion = existing.PackageVersion
		node.DeploymentID = existing.DeploymentID
	}
	if err := s.catalog.UpsertNode(ctx, node); err != nil {
		return store.CloudNode{}, "", fmt.Errorf("upsert node failed")
	}
	if existing == nil {
		return node, "created", nil
	}
	if existing.IsDeleted {
		return node, "restored", nil
	}
	return node, "unchanged", nil
}

func isMarketFetcherFunction(name, spaceID string, environment map[string]string) bool {
	name = strings.ToLower(strings.TrimSpace(name))
	space := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(spaceID), "_", "-"))
	if space != "" && strings.HasPrefix(name, "moox-fetcher-market-data-"+space) {
		return true
	}
	return strings.EqualFold(strings.TrimSpace(environment["MOOX_SPACE_ID"]), spaceID) &&
		strings.TrimSpace(environment["MOOX_FETCH_TIMEOUT_SECONDS"]) != "" &&
		strings.TrimSpace(environment["MOOX_STORAGE_RPC_GATEWAY_TARGET"]) != ""
}
