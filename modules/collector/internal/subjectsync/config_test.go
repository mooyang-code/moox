package subjectsync

import "testing"

func TestNormalizeAttributeJobsAppliesDefaults(t *testing.T) {
	jobs := []AttributeJob{{SpaceID: " crypto ", Sources: []string{"binance"}}}
	if err := NormalizeAttributeJobs(jobs); err != nil {
		t.Fatal(err)
	}
	if jobs[0].SpaceID != "crypto" || jobs[0].Timezone != "UTC" || jobs[0].Cron != "0 0 * * *" {
		t.Fatalf("jobs = %+v", jobs)
	}
}

func TestNormalizeAttributeJobsRejectsInvalidJobs(t *testing.T) {
	for name, job := range map[string]AttributeJob{
		"缺少空间":    {Sources: []string{"binance"}},
		"缺少数据源":   {SpaceID: "crypto"},
		"cron 无效": {SpaceID: "crypto", Sources: []string{"binance"}, Cron: "bad"},
		"时区无效":    {SpaceID: "crypto", Sources: []string{"binance"}, Timezone: "Mars/Olympus"},
	} {
		if err := NormalizeAttributeJobs([]AttributeJob{job}); err == nil {
			t.Errorf("%s：应当报错", name)
		}
	}
}
