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
	removedNode := protoreflect.Name("PrimaryStore" + "Node")
	removedRoute := protoreflect.Name("PrimaryStore" + "Route")
	for _, messageName := range []protoreflect.Name{removedNode, removedRoute} {
		if file.Messages().ByName(messageName) != nil {
			t.Fatalf("removed message %q is still present", messageName)
		}
	}

	service := file.Services().ByName("Metadata")
	removedMethods := []protoreflect.Name{
		protoreflect.Name("Create" + "PrimaryStore" + "Node"),
		protoreflect.Name("Update" + "PrimaryStore" + "Node"),
		protoreflect.Name("Get" + "PrimaryStore" + "Node"),
		protoreflect.Name("List" + "PrimaryStore" + "Nodes"),
		protoreflect.Name("Create" + "PrimaryStore" + "Route"),
		protoreflect.Name("Update" + "PrimaryStore" + "Route"),
		protoreflect.Name("Get" + "PrimaryStore" + "Route"),
		protoreflect.Name("List" + "PrimaryStore" + "Routes"),
	}
	for _, methodName := range removedMethods {
		if service.Methods().ByName(methodName) != nil {
			t.Fatalf("removed Metadata RPC %q is still present", methodName)
		}
	}

	if device := file.Messages().ByName("Device"); device.Fields().ByName("node_id") != nil {
		t.Fatal("Device.node_id must be removed")
	} else {
		assertFields(t, device, map[string]protoreflect.FieldNumber{
			"device_id": 1, "name": 2, "engine": 3, "endpoint": 4,
			"config_json": 5, "status": 6, "created_at": 7, "updated_at": 8,
			"attributes": 9,
		})
	}
	listDevicesReq := file.Messages().ByName("ListDevicesReq")
	if listDevicesReq.Fields().ByName("node_id") != nil {
		t.Fatal("ListDevicesReq.node_id must be removed")
	}
	assertFields(t, listDevicesReq, map[string]protoreflect.FieldNumber{
		"auth_info": 1, "engine": 2, "page": 3,
	})
}

func TestDataNodeRuntimeProtoContract(t *testing.T) {
	service := storagepb.File_data_node_proto.Services().ByName("DataNodeRuntime")
	if service == nil {
		t.Fatal("DataNodeRuntime service is missing")
	}
	for _, methodName := range []protoreflect.Name{"UpsertFields", "ReadFields", "GetNodeState", "CleanupExpiredBuckets"} {
		if service.Methods().ByName(methodName) == nil {
			t.Fatalf("DataNodeRuntime RPC %q is missing", methodName)
		}
	}
}

func TestStorageRuntimeOnlySupportsFieldUpserts(t *testing.T) {
	upsertMethod := protoreflect.Name("UpsertFields")
	removedMethod := protoreflect.Name("Delete" + "Fields")
	legacyWriteMethod := protoreflect.Name("Write" + "Fields")
	for _, service := range []protoreflect.ServiceDescriptor{
		storagepb.File_data_node_proto.Services().ByName("DataNodeRuntime"),
		storagepb.File_primary_store_proto.Services().ByName("PrimaryStore"),
	} {
		if service == nil {
			t.Fatal("storage runtime service is missing")
		}
		if service.Methods().ByName(upsertMethod) == nil {
			t.Fatalf("storage RPC %q is missing from %s", upsertMethod, service.FullName())
		}
		if service.Methods().ByName(removedMethod) != nil {
			t.Fatalf("removed storage RPC %q is still present on %s", removedMethod, service.FullName())
		}
		if service.Methods().ByName(legacyWriteMethod) != nil {
			t.Fatalf("ambiguous storage RPC %q is still present on %s", legacyWriteMethod, service.FullName())
		}
	}

	for _, message := range []struct {
		file protoreflect.FileDescriptor
		name protoreflect.Name
	}{
		{storagepb.File_data_node_proto, protoreflect.Name("Delete" + "FieldsReq")},
		{storagepb.File_data_node_proto, protoreflect.Name("Delete" + "FieldsRsp")},
		{storagepb.File_primary_store_proto, protoreflect.Name("PrimaryDelete" + "FieldsReq")},
		{storagepb.File_primary_store_proto, protoreflect.Name("PrimaryDelete" + "FieldsRsp")},
	} {
		if message.file.Messages().ByName(message.name) != nil {
			t.Fatalf("removed storage message %q is still present", message.name)
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
