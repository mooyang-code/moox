package proto_test

import (
	"testing"

	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	commonpb "github.com/mooyang-code/moox/packages/commonpb"
	"google.golang.org/protobuf/reflect/protoreflect"
)

func TestMetadataProtoDataNodeContract(t *testing.T) {
	file := storagepb.File_metadata_proto

	for _, messageName := range []protoreflect.Name{"DataNode", "DataNodeListItem", "DatasetSummary", "DatasetActivationCheck"} {
		if file.Messages().ByName(messageName) == nil {
			t.Fatalf("message %q is missing", messageName)
		}
	}

	assertFields(t, file.Messages().ByName("DataNode"), map[string]protoreflect.FieldNumber{
		"node_id":        1,
		"name":           2,
		"service_target": 3,
		"status":         4,
		"created_at":     5,
		"updated_at":     6,
	})
	assertFields(t, file.Messages().ByName("DatasetSummary"), map[string]protoreflect.FieldNumber{
		"space_id":      1,
		"dataset_id":    2,
		"name":          3,
		"data_kind":     4,
		"keep_duration": 5,
		"status":        6,
	})
	assertFields(t, file.Messages().ByName("DataNodeListItem"), map[string]protoreflect.FieldNumber{
		"node":     1,
		"datasets": 2,
	})
	assertFields(t, file.Messages().ByName("DatasetActivationCheck"), map[string]protoreflect.FieldNumber{
		"check_id": 1,
		"ready":    2,
		"summary":  3,
	})

	dataset := file.Messages().ByName("Dataset")
	assertFields(t, dataset, map[string]protoreflect.FieldNumber{
		"data_node_id":   17,
		"keep_duration":  18,
		"binding_locked": 19,
		"revision":       20,
	})
	listDatasetsReq := file.Messages().ByName("ListDatasetsReq")
	assertFields(t, listDatasetsReq, map[string]protoreflect.FieldNumber{
		"data_node_id": 7,
	})
	assertFields(t, file.Messages().ByName("ListDataNodesRsp"), map[string]protoreflect.FieldNumber{
		"items":       2,
		"page_result": 3,
	})
	if file.Messages().ByName("ListDataNodesRsp").Fields().ByName("dataset_count") != nil {
		t.Fatal("ListDataNodesRsp must not expose dataset_count")
	}

	service := file.Services().ByName("Metadata")
	for _, methodName := range []protoreflect.Name{
		"RegisterDataNode", "UpdateDataNode", "GetDataNode", "ListDataNodes",
		"DeleteDataNode", "RebindDatasetDataNode", "CheckDatasetActivation", "ActivateDataset",
	} {
		if service.Methods().ByName(methodName) == nil {
			t.Fatalf("Metadata RPC %q is missing", methodName)
		}
	}
}

func TestMetadataProtoCleanBreakContract(t *testing.T) {
	file := storagepb.File_metadata_proto
	for _, messageName := range []protoreflect.Name{"PrimaryStoreNode", "PrimaryStoreRoute"} {
		if file.Messages().ByName(messageName) != nil {
			t.Fatalf("removed message %q is still present", messageName)
		}
	}

	service := file.Services().ByName("Metadata")
	for _, methodName := range []protoreflect.Name{
		"CreatePrimaryStoreNode", "UpdatePrimaryStoreNode", "GetPrimaryStoreNode", "ListPrimaryStoreNodes",
		"CreatePrimaryStoreRoute", "UpdatePrimaryStoreRoute", "GetPrimaryStoreRoute", "ListPrimaryStoreRoutes",
	} {
		if service.Methods().ByName(methodName) != nil {
			t.Fatalf("removed Metadata RPC %q is still present", methodName)
		}
	}

	if device := file.Messages().ByName("Device"); device.Fields().ByName("node_id") != nil {
		t.Fatal("Device.node_id must be removed")
	} else {
		if !device.ReservedRanges().Has(2) {
			t.Fatal("Device must reserve field number 2")
		}
		if !device.ReservedNames().Has("node_id") {
			t.Fatal("Device must reserve field name node_id")
		}
	}
	listDevicesReq := file.Messages().ByName("ListDevicesReq")
	if listDevicesReq.Fields().ByName("node_id") != nil {
		t.Fatal("ListDevicesReq.node_id must be removed")
	}
	if !listDevicesReq.ReservedRanges().Has(2) {
		t.Fatal("ListDevicesReq must reserve field number 2")
	}
	if !listDevicesReq.ReservedNames().Has("node_id") {
		t.Fatal("ListDevicesReq must reserve field name node_id")
	}
}

func TestDataNodeRuntimeProtoContract(t *testing.T) {
	service := storagepb.File_data_node_proto.Services().ByName("DataNodeRuntime")
	if service == nil {
		t.Fatal("DataNodeRuntime service is missing")
	}
	for _, methodName := range []protoreflect.Name{"WriteFields", "ReadFields", "GetNodeState", "CleanupExpiredBuckets"} {
		if service.Methods().ByName(methodName) == nil {
			t.Fatalf("DataNodeRuntime RPC %q is missing", methodName)
		}
	}
}

func TestCommonProtoStorageConflictContract(t *testing.T) {
	enum := commonpb.File_moox_common_proto.Enums().ByName("ErrorCode")
	conflict := enum.Values().ByName("CONFLICT")
	if conflict == nil {
		t.Fatal("CONFLICT is missing")
	}
	if conflict.Number() != 14 {
		t.Fatalf("CONFLICT number = %d, want 14", conflict.Number())
	}
	for _, valueName := range []protoreflect.Name{"ROUTE_NOT_FOUND", "ROUTE_CROSS_DEVICE_UNSUPPORTED"} {
		if enum.Values().ByName(valueName) != nil {
			t.Fatalf("removed ErrorCode value %q is still present", valueName)
		}
	}
}

func assertFields(t *testing.T, message protoreflect.MessageDescriptor, expected map[string]protoreflect.FieldNumber) {
	t.Helper()
	for fieldName, number := range expected {
		field := message.Fields().ByName(protoreflect.Name(fieldName))
		if field == nil {
			t.Fatalf("%s.%s is missing", message.FullName(), fieldName)
		}
		if field.Number() != number {
			t.Fatalf("%s.%s number = %d, want %d", message.FullName(), fieldName, field.Number(), number)
		}
	}
}
