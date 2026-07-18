package gateway

// storageBFFMethods is the browser-facing Storage contract. The target
// service IDs are existing HTTP deployments; the BFF does not create a new
// Storage backend or expose the internal service names to the browser.
var storageBFFMethods = map[string]string{
	"CreateDataSource":        "storage_metadata",
	"UpdateDataSource":        "storage_metadata",
	"GetDataSource":           "storage_metadata",
	"ListDataSources":         "storage_metadata",
	"UpsertSubject":           "storage_metadata",
	"UpsertSubjectSymbol":     "storage_metadata",
	"GetSubject":              "storage_metadata",
	"ListSubjects":            "storage_metadata",
	"ListSubjectSymbols":      "storage_metadata",
	"CreateDataset":           "storage_metadata",
	"UpdateDataset":           "storage_metadata",
	"GetDataset":              "storage_metadata",
	"ListDatasets":            "storage_metadata",
	"BindDatasetSubject":      "storage_metadata",
	"ListDatasetSubjects":     "storage_metadata",
	"CreateField":             "storage_metadata",
	"CreateFieldGroup":        "storage_metadata",
	"UpdateFieldGroup":        "storage_metadata",
	"ListFieldGroups":         "storage_metadata",
	"UpdateField":             "storage_metadata",
	"GetField":                "storage_metadata",
	"ListFields":              "storage_metadata",
	"BatchUpdateFields":       "storage_metadata",
	"DeleteFieldGroup":        "storage_metadata",
	"CreateFactor":            "storage_metadata",
	"UpdateFactor":            "storage_metadata",
	"GetFactor":               "storage_metadata",
	"ListFactors":             "storage_metadata",
	"UpsertDatasetColumn":     "storage_metadata",
	"ListDatasetColumns":      "storage_metadata",
	"CreateView":              "storage_metadata",
	"UpdateView":              "storage_metadata",
	"GetView":                 "storage_metadata",
	"ListViews":               "storage_metadata",
	"UpsertViewColumn":        "storage_metadata",
	"ListViewColumns":         "storage_metadata",
	"CreatePrimaryStoreNode":  "storage_metadata",
	"UpdatePrimaryStoreNode":  "storage_metadata",
	"GetPrimaryStoreNode":     "storage_metadata",
	"ListPrimaryStoreNodes":   "storage_metadata",
	"CreatePrimaryStoreRoute": "storage_metadata",
	"UpdatePrimaryStoreRoute": "storage_metadata",
	"GetPrimaryStoreRoute":    "storage_metadata",
	"ListPrimaryStoreRoutes":  "storage_metadata",
	"RegisterArchiveFile":     "storage_metadata",
	"ListArchiveFiles":        "storage_metadata",
	"WriteTimeSeriesRows":     "storage_access",
	"ReadTimeSeriesRows":      "storage_access",
	"WriteRecordRows":         "storage_access",
	"ReadRecordRows":          "storage_access",
	"QueryTimeSeriesRows":     "storage_view",
	"SearchRecordRows":        "storage_view",
}

func storageBFFServiceID(method string) (string, bool) {
	serviceID, ok := storageBFFMethods[method]
	return serviceID, ok
}
