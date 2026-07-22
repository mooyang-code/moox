package gateway

// storageBFFMethods is the browser-facing Storage contract. The target
// service IDs are existing HTTP deployments; the BFF does not create a new
// Storage backend or expose the internal service names to the browser.
var storageBFFMethods = map[string]string{
	"CreateDataSource":       "storage-primary",
	"UpdateDataSource":       "storage-primary",
	"GetDataSource":          "storage-primary",
	"ListDataSources":        "storage-primary",
	"UpsertSubject":          "storage-primary",
	"UpsertSubjectSymbol":    "storage-primary",
	"GetSubject":             "storage-primary",
	"ListSubjects":           "storage-primary",
	"ListSubjectSymbols":     "storage-primary",
	"RegisterDataSubject":    "storage-primary",
	"CreateDataset":          "storage-primary",
	"UpdateDataset":          "storage-primary",
	"GetDataset":             "storage-primary",
	"ListDatasets":           "storage-primary",
	"BindDatasetSubject":     "storage-primary",
	"ListDatasetSubjects":    "storage-primary",
	"CreateField":            "storage-primary",
	"CreateFieldGroup":       "storage-primary",
	"UpdateFieldGroup":       "storage-primary",
	"ListFieldGroups":        "storage-primary",
	"UpdateField":            "storage-primary",
	"GetField":               "storage-primary",
	"ListFields":             "storage-primary",
	"BatchUpdateFields":      "storage-primary",
	"DeleteFieldGroup":       "storage-primary",
	"CreateFactor":           "storage-primary",
	"UpdateFactor":           "storage-primary",
	"GetFactor":              "storage-primary",
	"ListFactors":            "storage-primary",
	"UpsertDatasetColumn":    "storage-primary",
	"ListDatasetColumns":     "storage-primary",
	"CreateView":             "storage-primary",
	"UpdateView":             "storage-primary",
	"GetView":                "storage-primary",
	"ListViews":              "storage-primary",
	"UpsertViewColumn":       "storage-primary",
	"ListViewColumns":        "storage-primary",
	"GetDataNode":            "storage-primary",
	"ListDataNodes":          "storage-primary",
	"UpdateDataNode":         "storage-primary",
	"DeleteDataNode":         "storage-primary",
	"CheckDatasetActivation": "storage-primary",
	"ActivateDataset":        "storage-primary",
	"RebindDatasetDataNode":  "storage-primary",
	"RegisterArchiveFile":    "storage-primary",
	"ListArchiveFiles":       "storage-primary",
	"MergeTimeSeriesRows":    "storage-primary",
	"ReadTimeSeriesRows":     "storage-primary",
	"MergeRecordRows":        "storage-primary",
	"ReadRecordRows":         "storage-primary",
	"QueryTimeSeriesRows":    "storage-view",
	"SearchRecordRows":       "storage-view",
}

func storageBFFServiceID(method string) (string, bool) {
	serviceID, ok := storageBFFMethods[method]
	return serviceID, ok
}

func storageBFFServicePath(serviceID, method string) string {
	if serviceID == "storage-view" {
		return "trpc.moox.storage.DataView"
	}
	for _, candidate := range []string{"MergeTimeSeriesRows", "ReadTimeSeriesRows", "MergeRecordRows", "ReadRecordRows"} {
		if method == candidate {
			return "trpc.moox.storage.PrimaryStore"
		}
	}
	return "trpc.moox.storage.Metadata"
}
