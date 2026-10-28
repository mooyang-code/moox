package storageio

import (
	"fmt"
	"strings"

	factorpb "github.com/mooyang-code/moox/modules/factor/proto/factorgen"
)

// ValidatePeriodDetail 校验因子周期明细：成员身份是 member_key（同一因子可以用不同参数出现多次），factor_id 是它引用的定义；
// member_kind 必须是已知取值；失败标的必须在冻结的全集里；complete 的周期里不能有非 complete 的成员。
// Storage 对明细是不透明字节，这些规则由因子自己保证。
func ValidatePeriodDetail(detail *factorpb.PeriodDetail, universe []string, status string) error {
	subjects := make(map[string]struct{}, len(universe))
	for _, subject := range universe {
		subjects[subject] = struct{}{}
	}
	seen := make(map[string]struct{}, len(detail.GetFactors()))
	for i, state := range detail.GetFactors() {
		if state == nil || !validToken(state.GetFactorId()) || !validToken(state.GetMemberKey()) {
			return fmt.Errorf("factor period detail: factor %d identity is invalid", i)
		}
		if _, ok := seen[state.GetMemberKey()]; ok {
			return fmt.Errorf("factor period detail: member_key %q is duplicated", state.GetMemberKey())
		}
		seen[state.GetMemberKey()] = struct{}{}
		switch state.GetMemberKind() {
		case "fixed", "var":
		default:
			return fmt.Errorf("factor period detail: member %q member_kind %q is invalid", state.GetMemberKey(), state.GetMemberKind())
		}
		if state.GetStatus() != "complete" && state.GetStatus() != "degraded" && state.GetStatus() != "skipped" {
			return fmt.Errorf("factor period detail: factor %q status %q is invalid", state.GetFactorId(), state.GetStatus())
		}
		if !validToken(state.GetSpecHash()) {
			return fmt.Errorf("factor period detail: factor %q source_hash is required", state.GetFactorId())
		}
		if !validToken(state.GetDefinitionHash()) {
			return fmt.Errorf("factor period detail: factor %q definition_hash is required", state.GetFactorId())
		}
		failed, err := uniqueTokens(state.GetFailedSubjects(), fmt.Sprintf("factor %q failed_subjects", state.GetFactorId()))
		if err != nil {
			return err
		}
		for subject := range failed {
			if _, ok := subjects[subject]; !ok {
				return fmt.Errorf("factor period detail: factor %q failed_subject %q is not in universe_subject_ids", state.GetFactorId(), subject)
			}
		}
		// gap_subjects 是 failed_subjects 的子集：因回看窗口 K 线空洞而失败的标的。
		gaps, err := uniqueTokens(state.GetGapSubjects(), fmt.Sprintf("factor %q gap_subjects", state.GetFactorId()))
		if err != nil {
			return err
		}
		for subject := range gaps {
			if _, ok := failed[subject]; !ok {
				return fmt.Errorf("factor period detail: factor %q gap_subject %q is not in failed_subjects", state.GetFactorId(), subject)
			}
		}
		if state.GetStatus() == "complete" && len(failed) != 0 {
			return fmt.Errorf("factor period detail: complete factor %q has failed subjects", state.GetFactorId())
		}
		if status == "complete" && state.GetStatus() != "complete" {
			return fmt.Errorf("factor period detail: complete status has non-complete factor %q", state.GetFactorId())
		}
	}
	return nil
}

func validToken(value string) bool {
	return strings.TrimSpace(value) != "" && strings.TrimSpace(value) == value
}

func uniqueTokens(values []string, label string) (map[string]struct{}, error) {
	seen := make(map[string]struct{}, len(values))
	for i, value := range values {
		if !validToken(value) {
			return nil, fmt.Errorf("factor period detail: %s item %d is invalid", label, i)
		}
		if _, ok := seen[value]; ok {
			return nil, fmt.Errorf("factor period detail: %s item %q is duplicated", label, value)
		}
		seen[value] = struct{}{}
	}
	return seen, nil
}
