package router

const (
	mooxSkillCaller        = "moox-skill"
	mooxSkillServicePath   = "trpc.moox.storage.PrimaryStore"
	mooxSkillAllowedMethod = "ReadTimeSeriesRows"
)

func nativeCallerPolicyAllows(caller, servicePath, method string) bool {
	if caller != mooxSkillCaller {
		return true
	}
	return servicePath == mooxSkillServicePath && method == mooxSkillAllowedMethod
}

func serviceCallerPolicyAllows(caller string) bool {
	return caller != mooxSkillCaller
}
