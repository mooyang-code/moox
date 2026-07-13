package hostagentpb

// Host-agent RPC requests currently carry no fields, but implementing Validate
// keeps the validation filter effective when fields are added later.
func (*GetStatusReq) Validate() error   { return nil }
func (*GetSnapshotReq) Validate() error { return nil }
func (*RunOnceReq) Validate() error     { return nil }
